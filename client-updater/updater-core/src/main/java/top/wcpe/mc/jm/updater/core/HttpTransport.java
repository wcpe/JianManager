package top.wcpe.mc.jm.updater.core;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.function.LongConsumer;

/**
 * 生产 manifest/制品拉取（契约 §4，ADR-022）。
 *
 * <p>携带 {@code X-Client-Key}（拉取密钥）+ {@code X-Machine-Id}（机器码）；
 * manifest 走 {@code /client-channels/{channel}/manifest}，制品走 {@code /client-artifacts/{sha256}}。
 * 用 {@code java.net.HttpURLConnection}（JDK 自带、**兼容 Java 8**）——updater-core 须能被低版本
 * （Java 8）MC 的 JVM 经楔子 URLClassLoader 加载，故不用 Java 11 的 {@code java.net.http}。
 */
final class HttpTransport implements Transport {

    private final String endpoint;
    private final String channel;
    private final String clientKey;
    private final String machineId;
    private final String installId;
    private final String playerName;
    private final String coreVersion;
    private final int connectTimeoutMs;
    private final HttpRetryPolicy.Sleeper sleeper;

    HttpTransport(String endpoint, String channel, String clientKey, String machineId,
                  String coreVersion, Duration connectTimeout) {
        this(endpoint, channel, clientKey, machineId, "", "", coreVersion, connectTimeout,
                new HttpRetryPolicy.ThreadSleeper());
    }

    HttpTransport(String endpoint, String channel, String clientKey, String machineId,
                  String installId, String playerName, String coreVersion, Duration connectTimeout) {
        this(endpoint, channel, clientKey, machineId, installId, playerName, coreVersion, connectTimeout,
                new HttpRetryPolicy.ThreadSleeper());
    }

    HttpTransport(String endpoint, String channel, String clientKey, String machineId,
                  String installId, String playerName, String coreVersion, Duration connectTimeout,
                  HttpRetryPolicy.Sleeper sleeper) {
        this.endpoint = trimTrailingSlash(endpoint);
        this.channel = channel;
        this.clientKey = clientKey;
        this.machineId = machineId;
        this.installId = installId;
        this.playerName = playerName;
        this.coreVersion = coreVersion;
        this.connectTimeoutMs = (int) Math.max(0, connectTimeout.toMillis());
        this.sleeper = sleeper;
    }

    @Override
    public String fetchManifest() throws IOException {
        return HttpRetryPolicy.execute("manifest", () -> {
            HttpURLConnection c = open(endpoint + "/client-channels/" + channel + "/manifest", "GET", 30_000);
            if (coreVersion != null && !coreVersion.isEmpty()) {
                c.setRequestProperty("X-Client-Core-Version", coreVersion);
            }
            try {
                int code = c.getResponseCode();
                if (code != 200) {
                    throw failure(c, code);
                }
                return new String(readAll(c.getInputStream()), StandardCharsets.UTF_8);
            } finally {
                c.disconnect();
            }
        }, sleeper, java.time.Instant.now());
    }

    @Override
    public byte[] fetchArtifact(String artifactSha256) throws IOException {
        return fetchArtifact(artifactSha256, null);
    }

    @Override
    public byte[] fetchArtifact(String artifactSha256, LongConsumer onBytes) throws IOException {
        return HttpRetryPolicy.execute("制品", () -> {
            HttpURLConnection c = open(endpoint + "/client-artifacts/" + artifactSha256, "GET", 300_000);
            try {
                int code = c.getResponseCode();
                if (code != 200 && code != 206) {
                    throw failure(c, code);
                }
                return readAll(c.getInputStream(), onBytes);
            } finally {
                c.disconnect();
            }
        }, sleeper, java.time.Instant.now());
    }

    /**
     * 流式拉取制品（FR-257）：返回 InputStream 供调用方边读边写盘，不再全量读进 byte[]。
     * {@code offset>0} 时带 {@code Range: bytes=<offset>-} 请求头从断点续传（HTTP 206）。
     * 返回的流关闭时断开底层连接（释放 socket），调用方必须关闭。
     */
    @Override
    public InputStream fetchArtifactStream(String artifactSha256, long offset) throws IOException {
        HttpURLConnection c = HttpRetryPolicy.execute("制品流式", () -> {
            HttpURLConnection conn = open(endpoint + "/client-artifacts/" + artifactSha256, "GET", 300_000);
            if (offset > 0) {
                conn.setRequestProperty("Range", "bytes=" + offset + "-");
            }
            int code = conn.getResponseCode();
            // 200=全量（服务器不支持 Range 或 offset=0）；206=部分内容（Range 命中）。
            if (code != 200 && code != 206) {
                HttpRetryPolicy.HttpFailure failure = failure(conn, code);
                conn.disconnect();
                throw failure;
            }
            return conn;
        }, sleeper, java.time.Instant.now());
        final HttpURLConnection connection = c;
        final InputStream raw = connection.getInputStream();
        // 包装：close 时断开连接，避免底层 socket 泄漏。
        return new InputStream() {
            @Override
            public int read() throws IOException {
                return raw.read();
            }

            @Override
            public int read(byte[] b, int off, int len) throws IOException {
                return raw.read(b, off, len);
            }

            @Override
            public void close() throws IOException {
                try {
                    raw.close();
                } finally {
                    connection.disconnect();
                }
            }
        };
    }

    @Override
    public void postTelemetry(String jsonBody) {
        postJsonBestEffort(endpoint + "/client-telemetry", jsonBody, "遥测");
    }

    @Override
    public void postRuntimeHeartbeat(String jsonBody) {
        postJsonBestEffort(endpoint + "/client-channels/" + channel + "/telemetry/heartbeat", jsonBody, "运行态心跳");
    }

    @Override
    public void postSecurityHello(String jsonBody) {
        postJsonBestEffort(endpoint + "/client-security/hello", jsonBody, "安全画像");
    }

    private void postJsonBestEffort(String url, String jsonBody, String operation) {
        try {
            HttpRetryPolicy.execute(operation, () -> {
                HttpURLConnection c = open(url, "POST", 10_000);
                try {
                    c.setRequestProperty("Content-Type", "application/json");
                    c.setDoOutput(true);
                    byte[] body = jsonBody.getBytes(StandardCharsets.UTF_8);
                    try (OutputStream out = c.getOutputStream()) {
                        out.write(body);
                    }
                    int code = c.getResponseCode();
                    if (code < 200 || code >= 300) {
                        throw failure(c, code);
                    }
                    return null;
                } finally {
                    c.disconnect();
                }
            }, sleeper, java.time.Instant.now());
        } catch (Exception e) {
            // best-effort：上报失败绝不影响更新/游戏。
        }
    }

    /** 打开连接并设方法/超时/通用请求头（拉取密钥 + 客户端身份）。 */
    private HttpURLConnection open(String url, String method, int readTimeoutMs) throws IOException {
        HttpURLConnection c = (HttpURLConnection) new URL(url).openConnection();
        c.setRequestMethod(method);
        c.setConnectTimeout(connectTimeoutMs > 0 ? connectTimeoutMs : 15_000);
        c.setReadTimeout(readTimeoutMs);
        c.setInstanceFollowRedirects(true);
        c.setRequestProperty("X-Client-Key", nullToEmpty(clientKey));
        c.setRequestProperty("X-Machine-Id", nullToEmpty(machineId));
        c.setRequestProperty("X-Install-Id", nullToEmpty(installId));
        c.setRequestProperty("X-Player-Name", nullToEmpty(playerName));
        c.setRequestProperty("User-Agent", "JianManager-UpdaterCore/" + nullToUnknown(coreVersion));
        return c;
    }

    private HttpRetryPolicy.HttpFailure failure(HttpURLConnection c, int code) {
        return new HttpRetryPolicy.HttpFailure(code, readErrCode(c), c.getHeaderField("Retry-After"));
    }

    private String readErrCode(HttpURLConnection c) {
        String header = c.getHeaderField("X-Err-Code");
        if (header != null && !header.trim().isEmpty()) {
            return header.trim();
        }
        try {
            InputStream err = c.getErrorStream();
            if (err == null) {
                return "";
            }
            Object parsed = Json.parse(new String(readAll(err), StandardCharsets.UTF_8));
			if (parsed instanceof java.util.Map) {
				java.util.Map<?, ?> body = (java.util.Map<?, ?>) parsed;
				Object code = body.get("errCode");
				if (code == null) {
					code = body.get("error");
				}
				return code == null ? "" : String.valueOf(code);
			}
        } catch (Exception e) {
            // 错误体不可读时按无 errCode 处理。
        }
        return "";
    }

    /** 读尽输入流（Java 8 无 InputStream.readAllBytes，手写缓冲循环）。 */
    private static byte[] readAll(InputStream in) throws IOException {
        return readAll(in, null);
    }

    /** 读尽输入流，按每个分块字节数回调 {@code onBytes}（FR-099 进度，可空）。 */
    private static byte[] readAll(InputStream in, LongConsumer onBytes) throws IOException {
        ByteArrayOutputStream bos = new ByteArrayOutputStream();
        byte[] buf = new byte[8192];
        int n;
        try {
            while ((n = in.read(buf)) != -1) {
                bos.write(buf, 0, n);
                if (onBytes != null) {
                    onBytes.accept((long) n);
                }
            }
        } finally {
            in.close();
        }
        return bos.toByteArray();
    }

    private static String trimTrailingSlash(String s) {
        if (s == null) {
            return "";
        }
        return s.endsWith("/") ? s.substring(0, s.length() - 1) : s;
    }

    private static String nullToEmpty(String s) {
        return s == null ? "" : s;
    }

    private static String nullToUnknown(String s) {
        return s == null || s.isEmpty() ? "unknown" : s;
    }
}
