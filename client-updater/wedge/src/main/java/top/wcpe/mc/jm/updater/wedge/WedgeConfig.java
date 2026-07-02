package top.wcpe.mc.jm.updater.wedge;

import java.io.File;
import java.io.IOException;
import java.io.UnsupportedEncodingException;
import java.net.URLEncoder;
import java.nio.charset.Charset;
import java.nio.file.Files;
import java.util.Map;

/**
 * 楔子配置（契约 §6.2）：楔子同目录 {@code jm-updater.json}
 * = {@code {channel, key, endpoint, timeoutSec}}。
 *
 * <p>FR-258 起：去掉 {@code coreJar}/{@code coreVersion}/{@code signPublicKey}/{@code signKeyId}；
 * 本轮修复起继续移除 {@code coreEndpoint}，只允许 {@code endpoint} 配 API 根路径（如 {@code /api/v1}）。
 * 楔子据 {@code endpoint + channel} 自动拼接 updater-core 端点。{@code timeoutSec} 默认 120（契约 §6.3）。
 */
final class WedgeConfig {

    static final int DEFAULT_TIMEOUT_SEC = 120;
    /** boot-confirm 看门狗默认等待秒数（FR-091）：游戏存活此长即判首启成功。 */
    static final int DEFAULT_BOOT_CONFIRM_SEC = 30;

    final String channel;
    final String key;
    final String endpoint;
    final int timeoutSec;
    /** boot-confirm 看门狗等待秒数（FR-091）；默认 {@link #DEFAULT_BOOT_CONFIRM_SEC}。 */
    final int bootConfirmSec;
    /** 遥测上报开关（FR-094 隐私 opt-out）；默认 true，置 false 关闭上报。 */
    final boolean telemetry;

    WedgeConfig(String channel, String key, String endpoint,
                int timeoutSec, int bootConfirmSec, boolean telemetry) {
        this.channel = channel;
        this.key = key;
        this.endpoint = normalizeEndpoint(endpoint);
        this.timeoutSec = timeoutSec;
        this.bootConfirmSec = bootConfirmSec;
        this.telemetry = telemetry;
    }

    /** 从配置文件加载；文件缺失/字段缺省按默认值。 */
    static WedgeConfig load(File configFile) throws IOException {
        String text = new String(Files.readAllBytes(configFile.toPath()), Charset.forName("UTF-8"));
        return fromJson(text);
    }

    /** 从 JSON 文本解析（便于单测）。旧配置中的 coreJar/coreVersion/signPublicKey/signKeyId 字段被忽略。 */
    static WedgeConfig fromJson(String json) {
        Map<String, String> m = MiniJson.parseFlatObject(json);
        if (hasText(m.get("coreEndpoint"))) {
            throw new IllegalArgumentException("jm-updater.json 禁止配置 coreEndpoint，请只配置 API 根 endpoint");
        }
        int timeout = parseIntOr(m.get("timeoutSec"), DEFAULT_TIMEOUT_SEC);
        int bootConfirmSec = parseIntOr(m.get("bootConfirmSec"), DEFAULT_BOOT_CONFIRM_SEC);
        boolean telemetry = !"false".equalsIgnoreCase(trimOrEmpty(m.get("telemetry"))); // 缺省/非 false 即开启。
        return new WedgeConfig(m.get("channel"), m.get("key"), m.get("endpoint"),
                timeout, bootConfirmSec, telemetry);
    }

    /** 拼出当前频道的 updater-core 端点。 */
    String updaterCoreEndpoint() {
        if (!hasText(endpoint) || !hasText(channel)) {
            return "";
        }
        return endpoint + "/client-channels/" + urlEncodePathSegment(channel) + "/updater-core";
    }

    private static boolean hasText(String s) {
        return s != null && !s.trim().isEmpty();
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

    private static String normalizeEndpoint(String endpoint) {
        String value = trimOrEmpty(endpoint);
        while (value.endsWith("/")) {
            value = value.substring(0, value.length() - 1);
        }
        if (!value.isEmpty() && !value.endsWith("/api/v1")) {
            throw new IllegalArgumentException("endpoint 必须是 API 根路径，示例：http://127.0.0.1:18370/api/v1");
        }
        return value;
    }

    private static String urlEncodePathSegment(String value) {
        try {
            return URLEncoder.encode(value, "UTF-8").replace("+", "%20");
        } catch (UnsupportedEncodingException e) {
            throw new IllegalStateException("UTF-8 不可用", e);
        }
    }
}
