package top.wcpe.mc.jm.updater.core;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.time.Duration;

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
    private final String coreVersion;
    private final int connectTimeoutMs;

    HttpTransport(String endpoint, String channel, String clientKey, String machineId,
                  String coreVersion, Duration connectTimeout) {
        this.endpoint = trimTrailingSlash(endpoint);
        this.channel = channel;
        this.clientKey = clientKey;
        this.machineId = machineId;
        this.coreVersion = coreVersion;
        this.connectTimeoutMs = (int) Math.max(0, connectTimeout.toMillis());
    }

    @Override
    public String fetchManifest() throws IOException {
        HttpURLConnection c = open(endpoint + "/client-channels/" + channel + "/manifest", "GET", 30_000);
        if (coreVersion != null && !coreVersion.isEmpty()) {
            c.setRequestProperty("X-Client-Core-Version", coreVersion);
        }
        try {
            int code = c.getResponseCode();
            if (code != 200) {
                throw new IOException("manifest 拉取失败 HTTP " + code);
            }
            return new String(readAll(c.getInputStream()), StandardCharsets.UTF_8);
        } finally {
            c.disconnect();
        }
    }

    @Override
    public byte[] fetchArtifact(String artifactSha256) throws IOException {
        HttpURLConnection c = open(endpoint + "/client-artifacts/" + artifactSha256, "GET", 300_000);
        try {
            int code = c.getResponseCode();
            if (code != 200 && code != 206) {
                throw new IOException("制品拉取失败 HTTP " + code + " sha256=" + artifactSha256);
            }
            return readAll(c.getInputStream());
        } finally {
            c.disconnect();
        }
    }

    @Override
    public void postTelemetry(String jsonBody) {
        HttpURLConnection c = null;
        try {
            c = open(endpoint + "/client-telemetry", "POST", 10_000);
            c.setRequestProperty("Content-Type", "application/json");
            c.setDoOutput(true);
            byte[] body = jsonBody.getBytes(StandardCharsets.UTF_8);
            try (OutputStream out = c.getOutputStream()) {
                out.write(body);
            }
            c.getResponseCode(); // 触发发送；结果忽略。
        } catch (Exception e) {
            // best-effort：遥测失败绝不影响更新/游戏（契约 §4.3）。
        } finally {
            if (c != null) {
                c.disconnect();
            }
        }
    }

    /** 打开连接并设方法/超时/通用请求头（拉取密钥 + 机器码）。 */
    private HttpURLConnection open(String url, String method, int readTimeoutMs) throws IOException {
        HttpURLConnection c = (HttpURLConnection) new URL(url).openConnection();
        c.setRequestMethod(method);
        c.setConnectTimeout(connectTimeoutMs > 0 ? connectTimeoutMs : 15_000);
        c.setReadTimeout(readTimeoutMs);
        c.setInstanceFollowRedirects(true);
        c.setRequestProperty("X-Client-Key", nullToEmpty(clientKey));
        c.setRequestProperty("X-Machine-Id", nullToEmpty(machineId));
        return c;
    }

    /** 读尽输入流（Java 8 无 InputStream.readAllBytes，手写缓冲循环）。 */
    private static byte[] readAll(InputStream in) throws IOException {
        ByteArrayOutputStream bos = new ByteArrayOutputStream();
        byte[] buf = new byte[8192];
        int n;
        try {
            while ((n = in.read(buf)) != -1) {
                bos.write(buf, 0, n);
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
}
