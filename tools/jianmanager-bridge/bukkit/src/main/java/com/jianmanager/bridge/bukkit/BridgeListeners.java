package com.jianmanager.bridge.bukkit;

import com.jianmanager.bridge.BridgeClient;
import org.bukkit.event.EventHandler;
import org.bukkit.event.Listener;
import org.bukkit.event.player.AsyncPlayerChatEvent;
import org.bukkit.event.player.PlayerJoinEvent;
import org.bukkit.event.player.PlayerQuitEvent;
import org.bukkit.plugin.java.JavaPlugin;

import java.util.Map;

/**
 * Bukkit 事件监听：把玩家加入/退出/聊天事件上报给插件桥（FR-103）。
 * 仅采集事件并上报，不改变事件处理（监听器非阻塞）。
 */
public final class BridgeListeners implements Listener {

    private final JavaPlugin plugin;
    private final BridgeClient client;

    public BridgeListeners(JavaPlugin plugin, BridgeClient client) {
        this.plugin = plugin;
        this.client = client;
    }

    @EventHandler
    public void onJoin(PlayerJoinEvent event) {
        client.sendEvent("player_join", Map.of(
                "player", event.getPlayer().getName(),
                "uuid", event.getPlayer().getUniqueId().toString()
        ));
    }

    @EventHandler
    public void onQuit(PlayerQuitEvent event) {
        client.sendEvent("player_quit", Map.of(
                "player", event.getPlayer().getName(),
                "uuid", event.getPlayer().getUniqueId().toString()
        ));
    }

    @EventHandler
    public void onChat(AsyncPlayerChatEvent event) {
        client.sendEvent("player_chat", Map.of(
                "player", event.getPlayer().getName(),
                "message", event.getMessage()
        ));
    }
}
