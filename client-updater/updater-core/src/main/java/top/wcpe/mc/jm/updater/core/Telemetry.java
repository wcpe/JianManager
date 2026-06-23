package top.wcpe.mc.jm.updater.core;

import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Map;

/**
 * 客户端遥测构建（FR-094，契约 §4.3）。
 *
 * <p>仅环境粗粒度（os/java/启动器）+ 更新结果/版本/耗时，**不含敏感个人数据**；机器码经 {@code X-Machine-Id}
 * 请求头携带（不入 body）。隐私可关由调用方（{@link Core}）按 opt-out 配置决定是否上报。
 */
final class Telemetry {

    private Telemetry() {
    }

    /** 构建遥测 JSON。{@code rc} 为 {@link Updater} 返回码；fromVersion/toVersion 为 reconcile 前后已见版本。 */
    static String build(String channel, int rc, long fromVersion, long toVersion, long durationMs) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("channel", channel == null ? "" : channel);
        m.put("result", result(rc));
        m.put("fromVersion", fromVersion);
        m.put("toVersion", toVersion);
        m.put("os", System.getProperty("os.name", ""));
        m.put("javaVersion", System.getProperty("java.version", ""));
        m.put("launcher", launcher());
        m.put("durationMs", durationMs);
        // bootSuccess：updater 自报口径（reconcile 成功）；细粒度游戏启动确认见 FR-091 boot-confirm。
        m.put("bootSuccess", rc == Updater.OK);
        return Json.canonical(m);
    }

    /** Updater 返回码 → 遥测 result（契约 §4.3）。 */
    static String result(int rc) {
        if (rc == Updater.OK) {
            return "success";
        }
        if (rc == Updater.FAIL_STATIC) {
            return "fail-static";
        }
        return "error";
    }

    /** 启动器粗粒度识别（best-effort，从 JVM 命令行启发式）。 */
    static String launcher() {
        String cmd = System.getProperty("sun.java.command", "").toLowerCase(Locale.ROOT);
        if (cmd.contains("hmcl")) {
            return "HMCL";
        }
        if (cmd.contains("pcl")) {
            return "PCL2";
        }
        return "unknown";
    }
}
