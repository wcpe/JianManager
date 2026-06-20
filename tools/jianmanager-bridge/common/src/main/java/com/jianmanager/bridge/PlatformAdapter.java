package com.jianmanager.bridge;

/**
 * 平台适配接口：把平台无关的桥内核接到具体服务端（Bukkit / BungeeCord）。
 *
 * <p>桥内核（{@link BridgeClient}）收到平台下发的指令后调用本接口执行，
 * 平台侧（Bukkit/Bungee 插件）实现这些方法把动作落到各自 API（踢人/封禁/白名单）。
 * 实现需自行保证线程安全——指令在 WS 读线程回调，若平台 API 要求主线程，
 * 实现应自行调度到主线程执行。
 */
public interface PlatformAdapter {

    /** 踢出玩家。player 为玩家名，reason 可为空。返回是否成功定位到玩家。 */
    boolean kick(String player, String reason);

    /** 封禁玩家（平台支持时）。返回是否已执行。 */
    boolean ban(String player, String reason);

    /** 解封玩家。返回是否已执行。 */
    boolean unban(String player);

    /** 加入白名单。返回是否已执行。 */
    boolean whitelistAdd(String player);

    /** 移出白名单。返回是否已执行。 */
    boolean whitelistRemove(String player);

    /** 简单日志输出（info 级）。 */
    void logInfo(String message);

    /** 简单日志输出（warn 级）。 */
    void logWarn(String message);
}
