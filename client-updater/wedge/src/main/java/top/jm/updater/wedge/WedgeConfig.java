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

    final String channel;
    final String key;
    final String endpoint;
    final String coreJar;
    final int timeoutSec;

    WedgeConfig(String channel, String key, String endpoint, String coreJar, int timeoutSec) {
        this.channel = channel;
        this.key = key;
        this.endpoint = endpoint;
        this.coreJar = coreJar;
        this.timeoutSec = timeoutSec;
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
        int timeout = DEFAULT_TIMEOUT_SEC;
        String ts = m.get("timeoutSec");
        if (ts != null && !ts.trim().isEmpty()) {
            try {
                timeout = Integer.parseInt(ts.trim());
            } catch (NumberFormatException ignore) {
                // 非法值用默认。
            }
        }
        return new WedgeConfig(m.get("channel"), m.get("key"), m.get("endpoint"), coreJar, timeout);
    }
}
