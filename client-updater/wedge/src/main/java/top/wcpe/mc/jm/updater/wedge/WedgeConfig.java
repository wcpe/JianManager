package top.wcpe.mc.jm.updater.wedge;

import java.io.File;
import java.io.IOException;
import java.nio.charset.Charset;
import java.nio.file.Files;
import java.util.Map;

/**
 * 楔子配置（契约 §6.2）：楔子同目录 {@code jm-updater.json}
 * = {@code {channel, key, endpoint, coreEndpoint, timeoutSec}}。
 *
 * <p>FR-258 起：去掉 {@code coreJar}/{@code coreVersion}/{@code signPublicKey}/{@code signKeyId}，
 * 新增 {@code coreEndpoint}（CP 端 core 版本查询端点）。楔子首次启动自动拉取 core jar。
 * {@code timeoutSec} 默认 120（契约 §6.3）。
 */
final class WedgeConfig {

    static final int DEFAULT_TIMEOUT_SEC = 120;
    /** boot-confirm 看门狗默认等待秒数（FR-091）：游戏存活此长即判首启成功。 */
    static final int DEFAULT_BOOT_CONFIRM_SEC = 30;

    final String channel;
    final String key;
    final String endpoint;
    /** CP 端 core 版本查询端点（FR-258 gradle-wrapper 模式）。 */
    final String coreEndpoint;
    final int timeoutSec;
    /** boot-confirm 看门狗等待秒数（FR-091）；默认 {@link #DEFAULT_BOOT_CONFIRM_SEC}。 */
    final int bootConfirmSec;
    /** 遥测上报开关（FR-094 隐私 opt-out）；默认 true，置 false 关闭上报。 */
    final boolean telemetry;

    WedgeConfig(String channel, String key, String endpoint, String coreEndpoint,
                int timeoutSec, int bootConfirmSec, boolean telemetry) {
        this.channel = channel;
        this.key = key;
        this.endpoint = endpoint;
        this.coreEndpoint = coreEndpoint;
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
        int timeout = parseIntOr(m.get("timeoutSec"), DEFAULT_TIMEOUT_SEC);
        int bootConfirmSec = parseIntOr(m.get("bootConfirmSec"), DEFAULT_BOOT_CONFIRM_SEC);
        boolean telemetry = !"false".equalsIgnoreCase(trimOrEmpty(m.get("telemetry"))); // 缺省/非 false 即开启。
        return new WedgeConfig(m.get("channel"), m.get("key"), m.get("endpoint"), m.get("coreEndpoint"),
                timeout, bootConfirmSec, telemetry);
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
}
