package com.jianmanager.bridge.bungee;

import com.jianmanager.bridge.BridgeClient;
import com.jianmanager.bridge.BridgeConfig;
import net.md_5.bungee.api.plugin.Plugin;
import net.md_5.bungee.config.Configuration;
import net.md_5.bungee.config.ConfigurationProvider;
import net.md_5.bungee.config.YamlConfiguration;

import java.io.File;
import java.io.IOException;
import java.io.InputStream;
import java.nio.file.Files;
import java.util.concurrent.TimeUnit;

/**
 * JianManager 插件桥（BungeeCord/Waterfall）入口（FR-103 / ADR-012）。
 *
 * <p>启用时读取 config.yml 中的 wsUrl/token/instanceUuid，连入 Worker 的
 * {@code /ws/plugin-bridge}，上报跨服玩家事件并执行平台下发的指令；停用时断开。
 * 插件只与 Worker 通信，不直连 Control Plane / 数据库。
 */
public final class JianManagerBridgePlugin extends Plugin {

    private BridgeClient client;

    @Override
    public void onEnable() {
        Configuration cfg = loadConfig();

        BridgeConfig bridgeConfig = new BridgeConfig(
                cfg.getString("wsUrl", ""),
                cfg.getString("token", ""),
                cfg.getString("instanceUuid", ""),
                cfg.getBoolean("enabled", true),
                cfg.getLong("reconnectDelayMillis", 5000L)
        );

        BungeePlatformAdapter adapter = new BungeePlatformAdapter(this);
        this.client = new BridgeClient(bridgeConfig, adapter);

        getProxy().getPluginManager().registerListener(this, new BridgeListeners(client));

        // 周期上报代理在线玩家（跨服总数 + 玩家名），每 30s 一次
        getProxy().getScheduler().schedule(this, () -> reportServerStatus(client), 5, 30, TimeUnit.SECONDS);

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

    // reportServerStatus 上报代理当前在线人数与玩家名列表（跨后端聚合）。
    private void reportServerStatus(BridgeClient client) {
        if (client == null || !client.isConnected()) {
            return;
        }
        java.util.List<String> players = getProxy().getPlayers().stream()
                .map(p -> p.getName())
                .toList();
        client.sendEvent("server_status", java.util.Map.of(
                "online", players.size(),
                "players", players
        ));
    }

    // loadConfig 从插件数据目录读取 config.yml，不存在则从 jar 内默认资源释放。
    private Configuration loadConfig() {
        try {
            if (!getDataFolder().exists() && !getDataFolder().mkdirs()) {
                getLogger().warning("无法创建插件数据目录");
            }
            File file = new File(getDataFolder(), "config.yml");
            if (!file.exists()) {
                try (InputStream in = getResourceAsStream("config.yml")) {
                    if (in != null) {
                        Files.copy(in, file.toPath());
                    }
                }
            }
            return ConfigurationProvider.getProvider(YamlConfiguration.class).load(file);
        } catch (IOException e) {
            getLogger().warning("读取插件桥配置失败: " + e.getMessage());
            return new Configuration();
        }
    }
}
