package top.jm.updater.wedge;

import java.io.File;
import java.io.IOException;
import java.nio.charset.Charset;
import java.nio.file.Files;
import java.util.Map;

/**
 * 楔子配置（契约 §6.2）：楔子同目录 {@code jm-updater.json}
 * = {@code {channel, key, endpoint, coreJar, timeoutSec}}。
 *
 * <p>{@code coreJar} 默认 {@code updater-core.jar}，{@code timeoutSec} 默认 120（契约 §6.3）。
 */
final class WedgeConfig {

    static final String DEFAULT_CORE_JAR = "updater-core.jar";
    static final int DEFAULT_TIMEOUT_SEC = 120;
    /** boot-confirm 看门狗默认等待秒数（FR-091）：游戏存活此长即判首启成功。 */
    static final int DEFAULT_BOOT_CONFIRM_SEC = 30;

    final String channel;
    final String key;
    final String endpoint;
    final String coreJar;
    final int timeoutSec;
    /** 基础包内置 core 版本（FR-091 自更新比对基准）；默认 0。 */
    final long coreVersion;
    /** boot-confirm 看门狗等待秒数（FR-091）；默认 {@link #DEFAULT_BOOT_CONFIRM_SEC}。 */
    final int bootConfirmSec;
    /** 遥测上报开关（FR-094 隐私 opt-out）；默认 true，置 false 关闭上报。 */
    final boolean telemetry;

    WedgeConfig(String channel, String key, String endpoint, String coreJar, int timeoutSec,
                long coreVersion, int bootConfirmSec, boolean telemetry) {
        this.channel = channel;
        this.key = key;
        this.endpoint = endpoint;
        this.coreJar = coreJar;
        this.timeoutSec = timeoutSec;
        this.coreVersion = coreVersion;
        this.bootConfirmSec = bootConfirmSec;
        this.telemetry = telemetry;
    }

    /** 从配置文件加载；文件缺失/字段缺省按默认值。 */
    static WedgeConfig load(File configFile) throws IOException {
        String text = new String(Files.readAllBytes(configFile.toPath()), Charset.forName("UTF-8"));
        return fromJson(text);
    }

    /** 从 JSON 文本解析（便于单测）。 */
    static WedgeConfig fromJson(String json) {
        Map<String, String> m = MiniJson.parseFlatObject(json);
        String coreJar = m.get("coreJar");
        if (coreJar == null || coreJar.trim().isEmpty()) {
            coreJar = DEFAULT_CORE_JAR;
        }
        int timeout = parseIntOr(m.get("timeoutSec"), DEFAULT_TIMEOUT_SEC);
        long coreVersion = parseLongOr(m.get("coreVersion"), 0);
        int bootConfirmSec = parseIntOr(m.get("bootConfirmSec"), DEFAULT_BOOT_CONFIRM_SEC);
        boolean telemetry = !"false".equalsIgnoreCase(trimOrEmpty(m.get("telemetry"))); // 缺省/非 false 即开启。
        return new WedgeConfig(m.get("channel"), m.get("key"), m.get("endpoint"), coreJar, timeout,
                coreVersion, bootConfirmSec, telemetry);
    }

    private static String trimOrEmpty(String s) {
        return s == null ? "" : s.trim();
    }

    private static int parseIntOr(String s, int def) {
        if (s == null || s.trim().isEmpty()) {
            return def;
        }
        try {
            return Integer.parseInt(s.trim());
        } catch (NumberFormatException e) {
            return def;
        }
    }

    private static long parseLongOr(String s, long def) {
        if (s == null || s.trim().isEmpty()) {
            return def;
        }
        try {
            return Long.parseLong(s.trim());
        } catch (NumberFormatException e) {
            return def;
        }
    }
}
