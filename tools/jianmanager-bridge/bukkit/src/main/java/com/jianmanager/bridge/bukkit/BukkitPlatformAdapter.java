package com.jianmanager.bridge.bukkit;

import com.jianmanager.bridge.PlatformAdapter;
import org.bukkit.BanList;
import org.bukkit.Bukkit;
import org.bukkit.OfflinePlayer;
import org.bukkit.entity.Player;
import org.bukkit.plugin.java.JavaPlugin;

import java.util.concurrent.Callable;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;

/**
 * Bukkit 平台适配：把插件桥下发的指令落到 Bukkit API（FR-103 / ADR-012）。
 *
 * <p>Bukkit API 非线程安全、多数要求主线程执行；指令在 WS 读线程回调，
 * 故所有动作经 {@link #onMain} 调度到主线程同步执行（带超时，避免读线程久阻塞）。
 */
public final class BukkitPlatformAdapter implements PlatformAdapter {

    private final JavaPlugin plugin;

    public BukkitPlatformAdapter(JavaPlugin plugin) {
        this.plugin = plugin;
    }

    @Override
    public boolean kick(String player, String reason) {
        return onMain(() -> {
            Player p = Bukkit.getPlayerExact(player);
            if (p == null) {
                return false;
            }
            p.kickPlayer(reason == null || reason.isBlank() ? "你已被管理员踢出" : reason);
            return true;
        });
    }

    @Override
    public boolean ban(String player, String reason) {
        return onMain(() -> {
            Bukkit.getBanList(BanList.Type.NAME)
                    .addBan(player, reason == null || reason.isBlank() ? "你已被管理员封禁" : reason, null, "JianManager");
            Player p = Bukkit.getPlayerExact(player);
            if (p != null) {
                p.kickPlayer(reason == null || reason.isBlank() ? "你已被管理员封禁" : reason);
            }
            return true;
        });
    }

    @Override
    public boolean unban(String player) {
        return onMain(() -> {
            Bukkit.getBanList(BanList.Type.NAME).pardon(player);
            return true;
        });
    }

    @Override
    public boolean whitelistAdd(String player) {
        return onMain(() -> {
            OfflinePlayer op = Bukkit.getOfflinePlayer(player);
            op.setWhitelisted(true);
            return true;
        });
    }

    @Override
    public boolean whitelistRemove(String player) {
        return onMain(() -> {
            OfflinePlayer op = Bukkit.getOfflinePlayer(player);
            op.setWhitelisted(false);
            return true;
        });
    }

    @Override
    public void logInfo(String message) {
        plugin.getLogger().info(message);
    }

    @Override
    public void logWarn(String message) {
        plugin.getLogger().warning(message);
    }

    // onMain 在主线程同步执行 task 并取结果，最多等 5s。
    private boolean onMain(Callable<Boolean> task) {
        if (Bukkit.isPrimaryThread()) {
            try {
                return Boolean.TRUE.equals(task.call());
            } catch (Exception e) {
                plugin.getLogger().warning("指令执行异常: " + e.getMessage());
                return false;
            }
        }
        Future<Boolean> future = Bukkit.getScheduler().callSyncMethod(plugin, task);
        try {
            return Boolean.TRUE.equals(future.get(5, TimeUnit.SECONDS));
        } catch (Exception e) {
            plugin.getLogger().warning("指令在主线程执行失败: " + e.getMessage());
            return false;
        }
    }
}
