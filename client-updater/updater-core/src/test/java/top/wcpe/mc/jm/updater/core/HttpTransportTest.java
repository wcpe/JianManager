package top.wcpe.mc.jm.updater.core;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;

/** HttpTransport 固定请求头与 hello 端点。 */
class HttpTransportTest {

    @Test
    void securityHelloSendsRequiredHeadersAndBody() throws Exception {
        HeaderCapture capture = new HeaderCapture();
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/client-security/hello", exchange -> {
            capture.record(exchange);
            byte[] ok = "{}".getBytes(StandardCharsets.UTF_8);
            exchange.sendResponseHeaders(202, ok.length);
            exchange.getResponseBody().write(ok);
            exchange.close();
        });
        server.start();
        try {
            String endpoint = "http://127.0.0.1:" + server.getAddress().getPort();
            HttpTransport transport = new HttpTransport(endpoint, "skyblock-s1", "key-1", "machine-1",
                    "install-1", "Alex", "12", Duration.ofSeconds(1), millis -> { });
            SecurityIdentity identity = new SecurityIdentity(
                    "skyblock-s1", "Alex", "machine-1", "install-1", "12", "3", 8L);

            transport.postSecurityHello(SecurityHello.buildBody(identity));

            assertEquals("key-1", capture.header("X-Client-Key"));
            assertEquals("machine-1", capture.header("X-Machine-Id"));
            assertEquals("install-1", capture.header("X-Install-Id"));
            assertEquals("Alex", capture.header("X-Player-Name"));
            assertEquals("JianManager-UpdaterCore/12", capture.header("User-Agent"));
            @SuppressWarnings("unchecked")
            Map<String, Object> body = (Map<String, Object>) Json.parse(capture.body);
            assertEquals("Alex", body.get("playerName"));
            assertEquals("install-1", body.get("installId"));
        } finally {
            server.stop(0);
        }
    }

    @Test
    void runtimeHeartbeatPostsToChannelEndpoint() throws Exception {
        HeaderCapture capture = new HeaderCapture();
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/client-channels/skyblock-s1/telemetry/heartbeat", exchange -> {
            capture.record(exchange);
            exchange.sendResponseHeaders(202, -1);
            exchange.close();
        });
        server.start();
        try {
            String endpoint = "http://127.0.0.1:" + server.getAddress().getPort();
            HttpTransport transport = new HttpTransport(endpoint, "skyblock-s1", "key-1", "machine-1",
                    "install-1", "Alex", "12", Duration.ofSeconds(1), millis -> { });

            transport.postRuntimeHeartbeat(RuntimeHeartbeat.build("12", 7));

            assertEquals("POST", capture.method());
            assertEquals("key-1", capture.header("X-Client-Key"));
            assertEquals("machine-1", capture.header("X-Machine-Id"));
            @SuppressWarnings("unchecked")
            Map<String, Object> body = (Map<String, Object>) Json.parse(capture.body);
            assertEquals("12", body.get("coreVersion"));
            assertEquals(7L, ((Number) body.get("localVersion")).longValue());
        } finally {
            server.stop(0);
        }
    }

    @Test
    void retryAfterUsesBackendErrorField() throws Exception {
        List<Long> delays = new ArrayList<>();
        final int[] attempts = {0};
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/client-channels/skyblock-s1/manifest", exchange -> {
            attempts[0]++;
            if (attempts[0] == 1) {
                byte[] body = "{\"error\":\"RATE_LIMITED\"}".getBytes(StandardCharsets.UTF_8);
                exchange.getResponseHeaders().set("Retry-After", "1");
                exchange.sendResponseHeaders(429, body.length);
                exchange.getResponseBody().write(body);
            } else {
                byte[] body = "{}".getBytes(StandardCharsets.UTF_8);
                exchange.sendResponseHeaders(200, body.length);
                exchange.getResponseBody().write(body);
            }
            exchange.close();
        });
        server.start();
        try {
            String endpoint = "http://127.0.0.1:" + server.getAddress().getPort();
            HttpTransport transport = new HttpTransport(endpoint, "skyblock-s1", "key-1", "machine-1",
                    "install-1", "Alex", "12", Duration.ofSeconds(1), delays::add);

            transport.fetchManifest();

            assertEquals(2, attempts[0]);
            assertEquals(1, delays.size());
            assertEquals(1000L, delays.get(0).longValue());
        } finally {
            server.stop(0);
        }
    }

    private static final class HeaderCapture {
        private HttpExchange exchange;
        private String body;

        void record(HttpExchange exchange) throws IOException {
            this.exchange = exchange;
            byte[] bytes = HttpTransportTest.readAll(exchange.getRequestBody());
            this.body = new String(bytes, StandardCharsets.UTF_8);
        }

        String header(String name) {
            return exchange.getRequestHeaders().getFirst(name);
        }

        String method() {
            return exchange.getRequestMethod();
        }
    }

    private static byte[] readAll(java.io.InputStream in) throws IOException {
        java.io.ByteArrayOutputStream out = new java.io.ByteArrayOutputStream();
        byte[] buf = new byte[1024];
        int n;
        while ((n = in.read(buf)) != -1) {
            out.write(buf, 0, n);
        }
        return out.toByteArray();
    }
}
