package com.jianmanager.bridge.bungee;

import com.jianmanager.bridge.PlatformAdapter;
import net.md_5.bungee.api.ProxyServer;
import net.md_5.bungee.api.chat.TextComponent;
import net.md_5.bungee.api.connection.ProxiedPlayer;
import net.md_5.bungee.api.plugin.Plugin;

/**
 * BungeeCord 平台适配：把插件桥下发的指令落到代理 API（FR-103 / ADR-012）。
 *
 * <p>BungeeCord 核心仅提供「断开连接（踢出）」能力；封禁/白名单不在核心 API 内，
 * 通常由后端服或代理侧的封禁插件承担。故本适配 kick 用原生 API 精确执行；
 * ban/unban/whitelist 以「踢出 + 转发为代理控制台命令」尽力而为
 * （网络若装了对应命令的封禁/白名单插件即生效），并据玩家是否在线回报。
 * BungeeCord API 可跨线程安全调用，无需切主线程。
 */
public final class BungeePlatformAdapter implements PlatformAdapter {

    private final Plugin plugin;

    public BungeePlatformAdapter(Plugin plugin) {
        this.plugin = plugin;
    }

    @Override
    public boolean kick(String player, String reason) {
        ProxiedPlayer p = ProxyServer.getInstance().getPlayer(player);
        if (p == null) {
            return false;
        }
        p.disconnect(new TextComponent(reason == null || reason.isBlank() ? "你已被管理员踢出" : reason));
        return true;
    }

    @Override
    public boolean ban(String player, String reason) {
        // 先踢出（精确），再尝试转发代理控制台命令（依赖封禁插件提供 ban 命令）。
        kick(player, reason);
        return dispatchConsole("ban " + player + (reason == null || reason.isBlank() ? "" : " " + reason));
    }

    @Override
    public boolean unban(String player) {
        return dispatchConsole("pardon " + player);
    }

    @Override
    public boolean whitelistAdd(String player) {
        return dispatchConsole("whitelist add " + player);
    }

    @Override
    public boolean whitelistRemove(String player) {
        return dispatchConsole("whitelist remove " + player);
    }

    @Override
    public void logInfo(String message) {
        plugin.getLogger().info(message);
    }

    @Override
    public void logWarn(String message) {
        plugin.getLogger().warning(message);
    }

    // dispatchConsole 以代理控制台身份执行命令；命令是否被某插件接收无法可靠探知，
    // 这里返回是否成功提交（不抛异常）。
    private boolean dispatchConsole(String command) {
        try {
            ProxyServer.getInstance().getPluginManager()
                    .dispatchCommand(ProxyServer.getInstance().getConsole(), command);
            return true;
        } catch (Exception e) {
            plugin.getLogger().warning("转发控制台命令失败: " + e.getMessage());
            return false;
        }
    }
}
