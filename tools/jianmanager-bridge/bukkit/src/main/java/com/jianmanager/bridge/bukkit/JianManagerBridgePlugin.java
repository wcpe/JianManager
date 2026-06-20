package com.jianmanager.bridge.bukkit;

import com.jianmanager.bridge.BridgeClient;
import com.jianmanager.bridge.BridgeConfig;
import org.bukkit.configuration.file.FileConfiguration;
import org.bukkit.plugin.java.JavaPlugin;

/**
 * JianManager 插件桥（Bukkit/Spigot/Paper）入口（FR-103 / ADR-012）。
 *
 * <p>启用时读取 config.yml 中的 wsUrl/token/instanceUuid，连入 Worker 的
 * {@code /ws/plugin-bridge}，上报玩家/服务器事件并执行平台下发的指令；停用时断开连接。
 * 插件只与 Worker 通信，不直连 Control Plane / 数据库。
 */
public final class JianManagerBridgePlugin extends JavaPlugin {

    private BridgeClient client;

    @Override
    public void onEnable() {
        saveDefaultConfig();
        FileConfiguration cfg = getConfig();

        BridgeConfig bridgeConfig = new BridgeConfig(
                cfg.getString("wsUrl", ""),
                cfg.getString("token", ""),
                cfg.getString("instanceUuid", ""),
                cfg.getBoolean("enabled", true),
                cfg.getLong("reconnectDelayMillis", 5000L)
        );

        BukkitPlatformAdapter adapter = new BukkitPlatformAdapter(this);
        this.client = new BridgeClient(bridgeConfig, adapter);

        // 注册事件监听（join/quit/chat），上报给桥
        getServer().getPluginManager().registerEvents(new BridgeListeners(this, client), this);

        // 周期上报服务器状态（在线人数 + 玩家列表），20 ticks = 1s，这里每 30s 一次
        getServer().getScheduler().runTaskTimer(this, () -> reportServerStatus(client), 100L, 600L);

        client.start();
        getLogger().info("JianManager 插件桥已启用");
    }

    @Override
    public void onDisable() {
        if (client != null) {
            client.shutdown();
        }
        getLogger().info("JianManager 插件桥已停用");
    }

    // reportServerStatus 上报当前在线人数与玩家名列表。
    private void reportServerStatus(BridgeClient client) {
        if (client == null || !client.isConnected()) {
            return;
        }
        java.util.List<String> players = getServer().getOnlinePlayers().stream()
                .map(p -> p.getName())
                .toList();
        client.sendEvent("server_status", java.util.Map.of(
                "online", players.size(),
                "players", players
        ));
    }
}
