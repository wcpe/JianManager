package top.jm.updater.core;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;

/**
 * 生产 manifest/制品拉取（契约 §4，ADR-022）。
 *
 * <p>携带 {@code X-Client-Key}（拉取密钥）+ {@code X-Machine-Id}（机器码）；
 * manifest 走 {@code /client-channels/{channel}/manifest}，制品走 {@code /client-artifacts/{sha256}}。
 * 仅用 {@code java.net.http}（契约「仅 JDK 自带能力」）。
 */
final class HttpTransport implements Transport {

    private final HttpClient client;
    private final String endpoint;
    private final String channel;
    private final String clientKey;
    private final String machineId;
    private final String coreVersion;

    HttpTransport(String endpoint, String channel, String clientKey, String machineId,
                  String coreVersion, Duration connectTimeout) {
        this.endpoint = trimTrailingSlash(endpoint);
        this.channel = channel;
        this.clientKey = clientKey;
        this.machineId = machineId;
        this.coreVersion = coreVersion;
        this.client = HttpClient.newBuilder()
                .connectTimeout(connectTimeout)
                .followRedirects(HttpClient.Redirect.NORMAL)
                .build();
    }

    @Override
    public String fetchManifest() throws IOException {
        URI uri = URI.create(endpoint + "/client-channels/" + channel + "/manifest");
        HttpRequest.Builder b = HttpRequest.newBuilder(uri)
                .timeout(Duration.ofSeconds(30))
                .header("X-Client-Key", nullToEmpty(clientKey))
                .header("X-Machine-Id", nullToEmpty(machineId))
                .GET();
        if (coreVersion != null && !coreVersion.isEmpty()) {
            b.header("X-Client-Core-Version", coreVersion);
        }
        HttpResponse<String> resp = send(b.build(), HttpResponse.BodyHandlers.ofString());
        if (resp.statusCode() != 200) {
            throw new IOException("manifest 拉取失败 HTTP " + resp.statusCode());
        }
        return resp.body();
    }

    @Override
    public byte[] fetchArtifact(String artifactSha256) throws IOException {
        URI uri = URI.create(endpoint + "/client-artifacts/" + artifactSha256);
        HttpRequest req = HttpRequest.newBuilder(uri)
                .timeout(Duration.ofMinutes(5))
                .header("X-Client-Key", nullToEmpty(clientKey))
                .header("X-Machine-Id", nullToEmpty(machineId))
                .GET()
                .build();
        HttpResponse<byte[]> resp = send(req, HttpResponse.BodyHandlers.ofByteArray());
        int code = resp.statusCode();
        if (code != 200 && code != 206) {
            throw new IOException("制品拉取失败 HTTP " + code + " sha256=" + artifactSha256);
        }
        return resp.body();
    }

    @Override
    public void postTelemetry(String jsonBody) {
        try {
            URI uri = URI.create(endpoint + "/client-telemetry");
            HttpRequest req = HttpRequest.newBuilder(uri)
                    .timeout(Duration.ofSeconds(10))
                    .header("X-Client-Key", nullToEmpty(clientKey))
                    .header("X-Machine-Id", nullToEmpty(machineId))
                    .header("Content-Type", "application/json")
                    .POST(HttpRequest.BodyPublishers.ofString(jsonBody, StandardCharsets.UTF_8))
                    .build();
            client.send(req, HttpResponse.BodyHandlers.discarding());
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        } catch (Exception e) {
            // best-effort：遥测失败绝不影响更新/游戏（契约 §4.3）。
        }
    }

    private <T> HttpResponse<T> send(HttpRequest req, HttpResponse.BodyHandler<T> handler) throws IOException {
        try {
            return client.send(req, handler);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IOException("拉取被中断", e);
        }
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
