package com.jianmanager.bridge;

import com.google.gson.Gson;
import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import org.java_websocket.client.WebSocketClient;
import org.java_websocket.handshake.ServerHandshake;

import java.net.URI;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.util.Map;
import java.util.concurrent.atomic.AtomicBoolean;

/**
 * 平台插件桥 WebSocket 客户端（FR-103 / ADR-012）。
 *
 * <p>连入 Worker 的 {@code /ws/plugin-bridge}：连上后发 {@code hello}，
 * 之后把玩家/服务器事件经 {@link #sendEvent} 上报，并处理平台经 WS 下发的指令
 * （转给 {@link PlatformAdapter} 执行）。断线后由后台守护线程按固定间隔自动重连，
 * 直到 {@link #shutdown()}。
 *
 * <p>消息格式见 ARCHITECTURE 6.2.1：上行 {@code {type:"event",event,instanceId,data,ts}}，
 * 下行 {@code {type:"command",id,action,args}}。
 */
public final class BridgeClient {
    private final BridgeConfig config;
    private final PlatformAdapter adapter;
    private final Gson gson = new Gson();

    private final AtomicBoolean running = new AtomicBoolean(false);
    private volatile WebSocketClient ws;
    private Thread reconnectThread;

    public BridgeClient(BridgeConfig config, PlatformAdapter adapter) {
        this.config = config;
        this.adapter = adapter;
    }

    /** 启动连接 + 后台重连守护线程。重复调用无副作用。 */
    public synchronized void start() {
        if (!config.isUsable()) {
            adapter.logWarn("插件桥未配置或未启用（缺少 wsUrl/token），不连接");
            return;
        }
        if (!running.compareAndSet(false, true)) {
            return;
        }
        reconnectThread = new Thread(this::reconnectLoop, "jianmanager-bridge-ws");
        reconnectThread.setDaemon(true);
        reconnectThread.start();
    }

    /** 关闭连接并停止重连。 */
    public synchronized void shutdown() {
        running.set(false);
        WebSocketClient cur = ws;
        if (cur != null) {
            cur.close();
        }
        if (reconnectThread != null) {
            reconnectThread.interrupt();
        }
    }

    /** 当前是否已连接（用于平台侧状态展示）。 */
    public boolean isConnected() {
        WebSocketClient cur = ws;
        return cur != null && cur.isOpen();
    }

    /**
     * 上报一条事件。eventName 如 player_join/player_quit/player_chat/server_status；
     * data 为事件载荷（会序列化为 JSON）。未连接时静默丢弃（事件为尽力而为）。
     */
    public void sendEvent(String eventName, Map<String, ?> data) {
        WebSocketClient cur = ws;
        if (cur == null || !cur.isOpen()) {
            return;
        }
        JsonObject msg = new JsonObject();
        msg.addProperty("type", "event");
        msg.addProperty("event", eventName);
        msg.addProperty("instanceId", config.instanceUuid());
        msg.add("data", gson.toJsonTree(data));
        msg.addProperty("ts", System.currentTimeMillis() / 1000);
        try {
            cur.send(gson.toJson(msg));
        } catch (Exception e) {
            adapter.logWarn("上报事件失败: " + e.getMessage());
        }
    }

    // reconnectLoop 守护线程：建连 → 阻塞直到断开 → 间隔后重连，直到 shutdown。
    private void reconnectLoop() {
        while (running.get()) {
            try {
                connectOnce();
            } catch (Exception e) {
                adapter.logWarn("插件桥连接异常: " + e.getMessage());
            }
            if (!running.get()) {
                break;
            }
            try {
                Thread.sleep(Math.max(1000L, config.reconnectDelayMillis()));
            } catch (InterruptedException ie) {
                Thread.currentThread().interrupt();
                break;
            }
        }
    }

    // connectOnce 建立一次连接并阻塞到该连接关闭。
    private void connectOnce() throws Exception {
        URI uri = buildUri();
        final Object closedLatch = new Object();
        final AtomicBoolean closed = new AtomicBoolean(false);

        WebSocketClient client = new WebSocketClient(uri) {
            @Override
            public void onOpen(ServerHandshake handshake) {
                adapter.logInfo("插件桥已连接 Worker: " + config.wsUrl());
                sendHello();
            }

            @Override
            public void onMessage(String message) {
                handleMessage(message);
            }

            @Override
            public void onClose(int code, String reason, boolean remote) {
                adapter.logWarn("插件桥连接关闭 code=" + code + " reason=" + reason);
                wakeUp();
            }

            @Override
            public void onError(Exception ex) {
                adapter.logWarn("插件桥连接错误: " + ex.getMessage());
                wakeUp();
            }

            private void wakeUp() {
                synchronized (closedLatch) {
                    closed.set(true);
                    closedLatch.notifyAll();
                }
            }
        };

        this.ws = client;
        // 阻塞建连，连失败会触发 onError/onClose
        client.connectBlocking();

        synchronized (closedLatch) {
            while (!closed.get() && running.get()) {
                closedLatch.wait(1000);
            }
        }
        client.close();
        this.ws = null;
    }

    // buildUri 拼接 wsUrl?token=...&instance=...
    private URI buildUri() {
        String base = config.wsUrl();
        String sep = base.contains("?") ? "&" : "?";
        String q = "token=" + enc(config.token()) + "&instance=" + enc(config.instanceUuid());
        return URI.create(base + sep + q);
    }

    private static String enc(String s) {
        return URLEncoder.encode(s == null ? "" : s, StandardCharsets.UTF_8);
    }

    // sendHello 连上后告知平台与插件版本。
    private void sendHello() {
        WebSocketClient cur = ws;
        if (cur == null) {
            return;
        }
        JsonObject msg = new JsonObject();
        msg.addProperty("type", "hello");
        msg.addProperty("instanceId", config.instanceUuid());
        JsonObject data = new JsonObject();
        data.addProperty("platform", adapter.getClass().getSimpleName());
        data.addProperty("pluginVersion", "0.1.0");
        msg.add("data", data);
        try {
            cur.send(gson.toJson(msg));
        } catch (Exception e) {
            adapter.logWarn("发送 hello 失败: " + e.getMessage());
        }
    }

    // handleMessage 解析下行消息：command 转适配器执行，ping 回 pong。
    private void handleMessage(String message) {
        JsonObject obj;
        try {
            obj = JsonParser.parseString(message).getAsJsonObject();
        } catch (Exception e) {
            return;
        }
        String type = optString(obj, "type");
        switch (type) {
            case "ping" -> sendSimple("pong");
            case "command" -> dispatchCommand(obj);
            default -> { /* 忽略未知类型 */ }
        }
    }

    // dispatchCommand 把指令分发到平台适配器，并回执 command_result。
    private void dispatchCommand(JsonObject obj) {
        String id = optString(obj, "id");
        String action = optString(obj, "action");
        JsonObject args = obj.has("args") && obj.get("args").isJsonObject()
                ? obj.getAsJsonObject("args")
                : new JsonObject();
        String player = optString(args, "player");
        String reason = optString(args, "reason");

        boolean ok;
        try {
            ok = switch (action) {
                case "kick" -> adapter.kick(player, reason);
                case "ban" -> adapter.ban(player, reason);
                case "unban" -> adapter.unban(player);
                case "whitelist_add" -> adapter.whitelistAdd(player);
                case "whitelist_remove" -> adapter.whitelistRemove(player);
                default -> {
                    adapter.logWarn("未知插件指令: " + action);
                    yield false;
                }
            };
        } catch (Exception e) {
            adapter.logWarn("执行指令失败 action=" + action + ": " + e.getMessage());
            ok = false;
        }
        sendCommandResult(id, ok);
    }

    private void sendCommandResult(String id, boolean ok) {
        WebSocketClient cur = ws;
        if (cur == null || !cur.isOpen()) {
            return;
        }
        JsonObject msg = new JsonObject();
        msg.addProperty("type", "command_result");
        msg.addProperty("id", id == null ? "" : id);
        JsonObject data = new JsonObject();
        data.addProperty("ok", ok);
        msg.add("data", data);
        try {
            cur.send(gson.toJson(msg));
        } catch (Exception ignored) {
        }
    }

    private void sendSimple(String type) {
        WebSocketClient cur = ws;
        if (cur == null || !cur.isOpen()) {
            return;
        }
        JsonObject msg = new JsonObject();
        msg.addProperty("type", type);
        try {
            cur.send(gson.toJson(msg));
        } catch (Exception ignored) {
        }
    }

    private static String optString(JsonObject obj, String key) {
        return obj.has(key) && !obj.get(key).isJsonNull() ? obj.get(key).getAsString() : "";
    }
}
