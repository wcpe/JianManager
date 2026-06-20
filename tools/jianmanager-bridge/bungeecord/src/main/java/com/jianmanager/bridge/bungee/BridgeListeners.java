package com.jianmanager.bridge.bungee;

import com.jianmanager.bridge.BridgeClient;
import net.md_5.bungee.api.connection.ProxiedPlayer;
import net.md_5.bungee.api.event.ChatEvent;
import net.md_5.bungee.api.event.PlayerDisconnectEvent;
import net.md_5.bungee.api.event.PostLoginEvent;
import net.md_5.bungee.api.plugin.Listener;
import net.md_5.bungee.event.EventHandler;

import java.util.Map;

/**
 * BungeeCord 事件监听：把跨服玩家加入/退出/聊天上报给插件桥（FR-103）。
 * 代理层的 join/quit 反映「进入/离开整个群组服」，提供精确的跨服感知。
 */
public final class BridgeListeners implements Listener {

    private final BridgeClient client;

    public BridgeListeners(BridgeClient client) {
        this.client = client;
    }

    @EventHandler
    public void onLogin(PostLoginEvent event) {
        ProxiedPlayer p = event.getPlayer();
        client.sendEvent("player_join", Map.of(
                "player", p.getName(),
                "uuid", p.getUniqueId().toString()
        ));
    }

    @EventHandler
    public void onDisconnect(PlayerDisconnectEvent event) {
        ProxiedPlayer p = event.getPlayer();
        client.sendEvent("player_quit", Map.of(
                "player", p.getName(),
                "uuid", p.getUniqueId().toString()
        ));
    }

    @EventHandler
    public void onChat(ChatEvent event) {
        // 仅上报玩家发出的聊天（非命令），且发送方为玩家连接
        if (event.isCommand() || event.isProxyCommand()) {
            return;
        }
        if (event.getSender() instanceof ProxiedPlayer p) {
            client.sendEvent("player_chat", Map.of(
                    "player", p.getName(),
                    "message", event.getMessage()
            ));
        }
    }
}
