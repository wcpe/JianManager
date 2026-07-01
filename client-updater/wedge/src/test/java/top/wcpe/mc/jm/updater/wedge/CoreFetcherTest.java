package top.wcpe.mc.jm.updater.wedge;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpHandler;
import com.sun.net.httpserver.HttpServer;

import java.io.File;
import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.MessageDigest;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.jar.JarEntry;
import java.util.jar.JarOutputStream;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * CoreFetcher 测试（FR-258）：JDK 原生 HTTP 下载 + sha256 校验。
 *
 * <p>用 {@link HttpServer}（JDK 内置）模拟 CP 端点，覆盖下载成功 / sha256 不符拒绝 / 网络超时重试。
 */
class CoreFetcherTest {

    private HttpServer startServer(HttpHandler handler) throws IOException {
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", handler);
        server.start();
        return server;
    }

    /** 构造一个假 core jar（编译后的 .class 打入 jar）。 */
    private byte[] buildFakeJar() throws Exception {
        Path tmp = Files.createTempFile("fake-core", ".jar");
        try (JarOutputStream jos = new JarOutputStream(Files.newOutputStream(tmp))) {
            jos.putNextEntry(new JarEntry("META-INF/MANIFEST.MF"));
            jos.write("Manifest-Version: 1.0\n".getBytes("UTF-8"));
            jos.closeEntry();
        }
        return Files.readAllBytes(tmp);
        // tmp 文件由 JVM 退出时清理
    }

    private String sha256Hex(byte[] data) throws Exception {
        MessageDigest md = MessageDigest.getInstance("SHA-256");
        return hexEncode(md.digest(data));
    }

    private String hexEncode(byte[] bytes) {
        char[] hex = "0123456789abcdef".toCharArray();
        char[] out = new char[bytes.length * 2];
        for (int i = 0; i < bytes.length; i++) {
            int v = bytes[i] & 0xFF;
            out[i * 2] = hex[v >>> 4];
            out[i * 2 + 1] = hex[v & 0x0F];
        }
        return new String(out);
    }

    // ---- fetchInfo ----

    @Test
    void fetchInfoParsesValidResponse() throws Exception {
        byte[] jar = buildFakeJar();
        String sha = sha256Hex(jar);
        String json = "{\"version\":3,\"sha256\":\"" + sha + "\","
                + "\"downloadUrl\":\"http://localhost/x.jar\",\"size\":" + jar.length + "}";

        HttpServer server = startServer(exchange -> {
            exchange.sendResponseHeaders(200, json.length());
            try (OutputStream os = exchange.getResponseBody()) {
                os.write(json.getBytes("UTF-8"));
            }
        });
        try {
            String url = "http://127.0.0.1:" + server.getAddress().getPort() + "/updater-core";
            CoreFetcher.CoreEndpointInfo info = CoreFetcher.fetchInfo(url, "key123");
            assertNotNull(info);
            assertEquals(3, info.version);
            assertEquals(sha, info.sha256);
            assertEquals(jar.length, info.size);
        } finally {
            server.stop(0);
        }
    }

    @Test
    void fetchInfoReturnsNullOnNon200() throws Exception {
        HttpServer server = startServer(exchange -> {
            exchange.sendResponseHeaders(500, 0);
            exchange.getResponseBody().close();
        });
        try {
            String url = "http://127.0.0.1:" + server.getAddress().getPort() + "/";
            assertNull(CoreFetcher.fetchInfo(url, "k"));
        } finally {
            server.stop(0);
        }
    }

    @Test
    void fetchInfoReturnsNullOnUnreachable() {
        // 指向一个不存在的端口
        assertNull(CoreFetcher.fetchInfo("http://127.0.0.1:1/updater-core", "k", 200, 200));
    }

    @Test
    void fetchInfoSendsClientKeyHeader() throws Exception {
        byte[] jar = buildFakeJar();
        String sha = sha256Hex(jar);
        String json = "{\"version\":1,\"sha256\":\"" + sha + "\",\"downloadUrl\":\"http://x\",\"size\":0}";

        AtomicInteger keyCount = new AtomicInteger(0);
        HttpServer server = startServer(exchange -> {
            String key = exchange.getRequestHeaders().getFirst("X-Client-Key");
            if ("mykey".equals(key)) {
                keyCount.incrementAndGet();
            }
            exchange.sendResponseHeaders(200, json.length());
            try (OutputStream os = exchange.getResponseBody()) {
                os.write(json.getBytes("UTF-8"));
            }
        });
        try {
            String url = "http://127.0.0.1:" + server.getAddress().getPort() + "/";
            CoreFetcher.fetchInfo(url, "mykey");
            assertEquals(1, keyCount.get(), "应发送 X-Client-Key 请求头");
        } finally {
            server.stop(0);
        }
    }

    // ---- downloadJar ----

    @Test
    void downloadJarSucceedsAndVerifiesSha256(@TempDir Path tmp) throws Exception {
        byte[] jar = buildFakeJar();
        String sha = sha256Hex(jar);
        File coreDir = tmp.resolve("core").toFile();

        HttpServer server = startServer(exchange -> {
            exchange.sendResponseHeaders(200, jar.length);
            try (OutputStream os = exchange.getResponseBody()) {
                os.write(jar);
            }
        });
        try {
            String url = "http://127.0.0.1:" + server.getAddress().getPort() + "/core.jar";
            CoreFetcher.CoreEndpointInfo parsed = CoreFetcher.CoreEndpointInfo.fromJson(
                    "{\"version\":1,\"sha256\":\"" + sha + "\",\"downloadUrl\":\"" + url + "\","
                            + "\"size\":" + jar.length + "}");

            File result = CoreFetcher.downloadJar(parsed, "k", coreDir, 5000, 5000);

            assertTrue(result.isFile(), "下载后应存在 jar 文件");
            assertEquals(new File(coreDir, sha + ".jar"), result, "文件名应为 <sha>.jar");
            assertArrayEquals(jar, Files.readAllBytes(result.toPath()));
        } finally {
            server.stop(0);
        }
    }

    @Test
    void downloadJarRejectsSha256Mismatch(@TempDir Path tmp) throws Exception {
        byte[] jar = buildFakeJar();
        String wrongSha = "0000000000000000000000000000000000000000000000000000000000000000";
        File coreDir = tmp.resolve("core").toFile();

        HttpServer server = startServer(exchange -> {
            exchange.sendResponseHeaders(200, jar.length);
            try (OutputStream os = exchange.getResponseBody()) {
                os.write(jar);
            }
        });
        try {
            String url = "http://127.0.0.1:" + server.getAddress().getPort() + "/core.jar";
            CoreFetcher.CoreEndpointInfo info = CoreFetcher.CoreEndpointInfo.fromJson(
                    "{\"version\":1,\"sha256\":\"" + wrongSha + "\",\"downloadUrl\":\"" + url + "\","
                            + "\"size\":" + jar.length + "}");

            boolean threw = false;
            try {
                CoreFetcher.downloadJar(info, "k", coreDir, 5000, 5000);
            } catch (CoreFetcher.Sha256MismatchException e) {
                threw = true;
            }
            assertTrue(threw, "sha256 不符应抛 Sha256MismatchException");
            assertFalse(new File(coreDir, wrongSha + ".jar").exists(), "sha256 不符不应留下目标 jar");
        } finally {
            server.stop(0);
        }
    }

    @Test
    void downloadJarRetriesOnNetworkError(@TempDir Path tmp) throws Exception {
        byte[] jar = buildFakeJar();
        String sha = sha256Hex(jar);
        File coreDir = tmp.resolve("core").toFile();

        AtomicInteger attempts = new AtomicInteger(0);
        HttpServer server = startServer(exchange -> {
            int n = attempts.incrementAndGet();
            if (n == 1) {
                // 第一次：模拟网络错误（关闭连接不发响应体）
                exchange.close();
                return;
            }
            // 第二次（重试）：正常返回
            exchange.sendResponseHeaders(200, jar.length);
            try (OutputStream os = exchange.getResponseBody()) {
                os.write(jar);
            }
        });
        try {
            String url = "http://127.0.0.1:" + server.getAddress().getPort() + "/core.jar";
            CoreFetcher.CoreEndpointInfo info = CoreFetcher.CoreEndpointInfo.fromJson(
                    "{\"version\":1,\"sha256\":\"" + sha + "\",\"downloadUrl\":\"" + url + "\","
                            + "\"size\":" + jar.length + "}");

            File result = CoreFetcher.downloadJar(info, "k", coreDir, 5000, 5000);

            assertTrue(result.isFile(), "重试后应下载成功");
            assertTrue(attempts.get() >= 2, "应至少尝试 2 次");
        } finally {
            server.stop(0);
        }
    }

    @Test
    void downloadJarReturnsExistingFileIfAlreadyDownloaded(@TempDir Path tmp) throws Exception {
        byte[] jar = buildFakeJar();
        String sha = sha256Hex(jar);
        File coreDir = tmp.resolve("core").toFile();
        coreDir.mkdirs();
        // 预先放入同 sha 的 jar
        Files.write(new File(coreDir, sha + ".jar").toPath(), jar);

        AtomicInteger requests = new AtomicInteger(0);
        HttpServer server = startServer(exchange -> {
            requests.incrementAndGet();
            exchange.sendResponseHeaders(200, 0);
            exchange.getResponseBody().close();
        });
        try {
            String url = "http://127.0.0.1:" + server.getAddress().getPort() + "/core.jar";
            CoreFetcher.CoreEndpointInfo info = CoreFetcher.CoreEndpointInfo.fromJson(
                    "{\"version\":1,\"sha256\":\"" + sha + "\",\"downloadUrl\":\"" + url + "\","
                            + "\"size\":" + jar.length + "}");

            File result = CoreFetcher.downloadJar(info, "k", coreDir, 5000, 5000);
            assertEquals(new File(coreDir, sha + ".jar"), result);
            assertEquals(0, requests.get(), "目标 jar 已存在不应发 HTTP 请求");
        } finally {
            server.stop(0);
        }
    }

    private static void assertArrayEquals(byte[] expected, byte[] actual) {
        org.junit.jupiter.api.Assertions.assertArrayEquals(expected, actual);
    }
}
