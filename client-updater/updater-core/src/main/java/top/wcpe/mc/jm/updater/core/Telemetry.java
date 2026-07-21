package top.wcpe.mc.jm.updater.core;

import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Map;
import java.util.TimeZone;

/**
 * 客户端遥测构建（FR-094 / FR-360，契约 §4.3）。
 *
 * <p>环境粗粒度 + 更新结果/版本/耗时；required 含 coreVersion/arch，diagnostic 含
 * javaVendor/locale/timezone/memoryTier。机器码经 {@code X-Machine-Id} 请求头携带（不入 body）。
 * 隐私可关由调用方（{@link Core}）按 opt-out 配置决定是否上报。
 */
final class Telemetry {

    private Telemetry() {
    }

    /**
     * 构建遥测 JSON。
     *
     * @param coreVersion 本进程 updater-core 构建展示版本（required）
     */
    static String build(String channel, int rc, long fromVersion, long toVersion, long durationMs, String coreVersion) {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("channel", channel == null ? "" : channel);
        m.put("result", result(rc));
        m.put("fromVersion", fromVersion);
        m.put("toVersion", toVersion);
        m.put("coreVersion", coreVersion == null ? "" : coreVersion);
        m.put("os", normalizeOs(System.getProperty("os.name", "")));
        m.put("arch", normalizeArch(System.getProperty("os.arch", "")));
        m.put("javaVersion", System.getProperty("java.version", ""));
        m.put("javaVendor", truncate(System.getProperty("java.vendor", ""), 64));
        m.put("launcher", launcher());
        m.put("locale", localeTag());
        m.put("timezone", TimeZone.getDefault().getID());
        m.put("memoryTier", memoryTier(Runtime.getRuntime().maxMemory()));
        m.put("durationMs", durationMs);
        // bootSuccess：updater 自报口径（reconcile 成功）；细粒度游戏启动确认见 FR-091 boot-confirm。
        m.put("bootSuccess", rc == Updater.OK);
        return Json.canonical(m);
    }

    /** 兼容旧调用签名（测试/历史）；coreVersion 置空。 */
    static String build(String channel, int rc, long fromVersion, long toVersion, long durationMs) {
        return build(channel, rc, fromVersion, toVersion, durationMs, "");
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

    /** os.name → windows/macos/linux/unknown。 */
    static String normalizeOs(String raw) {
        if (raw == null || raw.isEmpty()) {
            return "unknown";
        }
        String s = raw.toLowerCase(Locale.ROOT);
        if (s.contains("win")) {
            return "windows";
        }
        if (s.contains("mac") || s.contains("darwin")) {
            return "macos";
        }
        if (s.contains("nux") || s.contains("nix") || s.contains("aix")) {
            return "linux";
        }
        return "unknown";
    }

    /** os.arch → amd64/arm64/x86/unknown。 */
    static String normalizeArch(String raw) {
        if (raw == null || raw.isEmpty()) {
            return "unknown";
        }
        String s = raw.toLowerCase(Locale.ROOT);
        if (s.equals("amd64") || s.equals("x86_64") || s.equals("x64")) {
            return "amd64";
        }
        if (s.equals("aarch64") || s.equals("arm64")) {
            return "arm64";
        }
        if (s.equals("x86") || s.equals("i386") || s.equals("i686")) {
            return "x86";
        }
        return "unknown";
    }

    /**
     * 按 maxMemory 分档（不上报精确字节）。
     * le2g / le4g / le8g / gt8g / unknown
     */
    static String memoryTier(long maxMemoryBytes) {
        if (maxMemoryBytes <= 0) {
            return "unknown";
        }
        long g2 = 2L * 1024 * 1024 * 1024;
        long g4 = 4L * 1024 * 1024 * 1024;
        long g8 = 8L * 1024 * 1024 * 1024;
        if (maxMemoryBytes <= g2) {
            return "le2g";
        }
        if (maxMemoryBytes <= g4) {
            return "le4g";
        }
        if (maxMemoryBytes <= g8) {
            return "le8g";
        }
        return "gt8g";
    }

    static String localeTag() {
        try {
            return Locale.getDefault().toLanguageTag();
        } catch (RuntimeException e) {
            return "";
        }
    }

    static String truncate(String s, int max) {
        if (s == null) {
            return "";
        }
        if (s.length() <= max) {
            return s;
        }
        return s.substring(0, max);
    }
}
