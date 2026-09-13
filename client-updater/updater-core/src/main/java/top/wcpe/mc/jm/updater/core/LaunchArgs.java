package top.wcpe.mc.jm.updater.core;

import java.util.Locale;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * 启动命令行采集（FR-426 增补）：updater 以 javaagent 跑在游戏 JVM 内，
 * {@code sun.java.command} 即「主类 + 完整启动参数」（如
 * {@code net.fabricmc.loader.impl.launch.knot.KnotClient --username Amazon2 --uuid … --accessToken …}）。
 *
 * <p>抽为纯静态函数便于单测（不依赖 JVM 真实属性）；调用方（{@link Core}）在运行时读真实值。
 * <p>脱敏：{@code --accessToken} 的值是账号凭据，入库前必须替换为 {@code ***}（FR-360 隐私口径）。
 */
final class LaunchArgs {

    private LaunchArgs() {
    }

    /** 完整启动命令（主类 + 程序参数）；属性不存在（单测/JVM 外）返回空串。 */
    static String fullCommand() {
        return System.getProperty("sun.java.command", "");
    }

    /**
     * 解析 {@code --username <值>}（兼容 {@code --username=<值>}）；找不到返回空串。
     * 启动器（HMCL/PCL2 等）总会注入登录名——这是比 jm-updater.json 手填更可靠的玩家名来源。
     */
    static String parseUsername(String command) {
        if (command == null || command.isEmpty()) {
            return "";
        }
        Matcher m = Pattern.compile("--username(?:=|\\s+)([^\\s]+)").matcher(command);
        return m.find() ? m.group(1) : "";
    }

    /**
     * 脱敏：{@code --accessToken <值>} 与 {@code --accessToken=<值>} 的值替换为 {@code ***}。
     * 其余参数（含 --uuid/--gameDir 等）原样保留，保证诊断价值。
     */
    static String redact(String command) {
        if (command == null || command.isEmpty()) {
            return "";
        }
        String r = command.replaceAll("(--accessToken\\s+)([^\\s]+)", "$1***");
        r = r.replaceAll("(--accessToken=)([^\\s]+)", "$1***");
        return r;
    }

    /** 玩家名解析：显式值优先，回退 {@code --username}；两侧去空白，空串表示未知。 */
    static String resolvePlayerName(String explicit, String command) {
        if (explicit != null && !explicit.trim().isEmpty()) {
            return explicit.trim();
        }
        return parseUsername(command);
    }

    /** 统一小写仅供启动器识别等场景使用（与 Telemetry.launcher 现状一致）。 */
    static String lower(String s) {
        return s == null ? "" : s.toLowerCase(Locale.ROOT);
    }
}
