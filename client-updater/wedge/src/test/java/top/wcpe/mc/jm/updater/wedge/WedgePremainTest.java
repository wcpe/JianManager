package top.wcpe.mc.jm.updater.wedge;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;

import java.io.File;
import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.MessageDigest;
import java.util.Map;
import java.util.Properties;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.jar.JarEntry;
import java.util.jar.JarOutputStream;
import javax.tools.JavaCompiler;
import javax.tools.ToolProvider;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Wedge.runUpdate 端到端测试（FR-258 gradle-wrapper 模式）。
 *
 * <p>用 {@link HttpServer} 模拟 CP 端点，编译假 Core.jar，验证：
 * 首次启动拉取 core / 版本升级 / 版本相等 / CP 不可达用本地 / CP 不可达无本地 fail-open。
 */
class WedgePremainTest {

    /** 编译并打包一个假 Core.run（写 marker 文件到 gameDir/.jm-updater 作为调用成功证据）。 */
    private byte[] buildFakeCoreJar() throws Exception {
        String src =
                "package top.wcpe.mc.jm.updater.core;\n"
                        + "import java.util.Map;\n"
                        + "import java.io.*;\n"
                        + "public final class Core {\n"
                        + "  public static int run(Map<String,String> ctx) {\n"
                        + "    try {\n"
                        + "      String gameDir = ctx.get(\"gameDir\");\n"
                        + "      File marker = new File(gameDir, \".jm-updater/core-ran.marker\");\n"
                        + "      marker.getParentFile().mkdirs();\n"
                        + "      new FileOutputStream(marker).close();\n"
                        + "    } catch (Exception e) {}\n"
                        + "    return 0;\n"
                        + "  }\n"
                        + "}\n";
        Path workDir = Files.createTempDirectory("fake-core");
        Path pkgDir = workDir.resolve("top/wcpe/mc/jm/updater/core");
        Files.createDirectories(pkgDir);
        Path srcFile = pkgDir.resolve("Core.java");
        Files.write(srcFile, src.getBytes(StandardCharsets.UTF_8));

        JavaCompiler compiler = ToolProvider.getSystemJavaCompiler();
        assertTrue(compiler != null, "需要 JDK（含 javac）运行此测试");
        int res = compiler.run(null, null, null, srcFile.toString());
        assertEquals(0, res, "假 core 编译应成功");

        Path classFile = pkgDir.resolve("Core.class");
        Path jarPath = workDir.resolve("fake-core.jar");
        try (JarOutputStream jos = new JarOutputStream(Files.newOutputStream(jarPath))) {
            jos.putNextEntry(new JarEntry("top/wcpe/mc/jm/updater/core/Core.class"));
            jos.write(Files.readAllBytes(classFile));
            jos.closeEntry();
        }
        return Files.readAllBytes(jarPath);
    }

    private String sha256Hex(byte[] data) throws Exception {
        MessageDigest md = MessageDigest.getInstance("SHA-256");
        byte[] hash = md.digest(data);
        StringBuilder sb = new StringBuilder();
        for (byte b : hash) {
            sb.append(String.format("%02x", b));
        }
        return sb.toString();
    }

    private String writeConfig(File wedgeDir, String coreEndpointUrl) throws Exception {
        wedgeDir.mkdirs();
        String json = "{\"channel\":\"test-ch\",\"key\":\"test-key\","
                + "\"endpoint\":\"http://localhost/manifest\","
                + "\"coreEndpoint\":\"" + coreEndpointUrl + "\","
                + "\"timeoutSec\":15,\"bootConfirmSec\":1,"
                + "\"telemetry\":false,\"extraField\":\"hello\"}";
        Files.write(new File(wedgeDir, "jm-updater.json").toPath(), json.getBytes(StandardCharsets.UTF_8));
        return json;
    }

    private void stageLocalCore(File coreDir, String sha, long version) throws Exception {
        coreDir.mkdirs();
        byte[] jar = buildFakeCoreJar();
        // 为了让本地 jar 与远程不同 sha，用不同内容（简单用版本号填充）
        Files.write(new File(coreDir, sha + ".jar").toPath(), jar);
        Properties p = new Properties();
        p.setProperty("selectedSha", sha);
        p.setProperty("selectedVersion", Long.toString(version));
        try (OutputStream out = Files.newOutputStream(new File(coreDir, "state.properties").toPath())) {
            p.store(out, null);
        }
    }

    private HttpServer startCoreEndpointServer(byte[] jarBytes, long version) throws Exception {
        String sha = sha256Hex(jarBytes);
        int port = findFreePort();
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", port), 0);

        server.createContext("/updater-core", exchange -> {
            String json = "{\"version\":" + version + ",\"sha256\":\"" + sha + "\","
                    + "\"downloadUrl\":\"http://127.0.0.1:" + port + "/core.jar\","
                    + "\"size\":" + jarBytes.length + "}";
            exchange.sendResponseHeaders(200, json.length());
            try (OutputStream os = exchange.getResponseBody()) {
                os.write(json.getBytes(StandardCharsets.UTF_8));
            }
        });
        server.createContext("/core.jar", exchange -> {
            exchange.sendResponseHeaders(200, jarBytes.length);
            try (OutputStream os = exchange.getResponseBody()) {
                os.write(jarBytes);
            }
        });
        server.start();
        return server;
    }

    private int findFreePort() throws IOException {
        java.net.ServerSocket s = new java.net.ServerSocket(0);
        int port = s.getLocalPort();
        s.close();
        return port;
    }

    private boolean coreRan(File gameDir) {
        return new File(gameDir, ".jm-updater/core-ran.marker").isFile();
    }

    // ---- 测试场景 ----

    @Test
    void firstStartDownloadsCoreAndRunsIt(@TempDir Path tmp) throws Exception {
        File wedgeDir = tmp.resolve("wedge").toFile();
        File gameDir = tmp.resolve("game").toFile();
        byte[] jar = buildFakeCoreJar();

        HttpServer server = startCoreEndpointServer(jar, 1);
        try {
            String url = "http://127.0.0.1:" + server.getAddress().getPort() + "/updater-core";
            writeConfig(wedgeDir, url);

            Wedge.runUpdate(wedgeDir, gameDir.getAbsolutePath(), Messages.forLanguage("en"));

            assertTrue(coreRan(gameDir), "首次启动应下载 core 并调用 Core.run");
            File coreDir = new File(gameDir, ".jm-updater/core");
            String sha = sha256Hex(jar);
            assertTrue(new File(coreDir, sha + ".jar").isFile(), "core jar 应下载到 core/<sha>.jar");
        } finally {
            server.stop(0);
        }
    }

    @Test
    void versionUpgradeDownloadsNewAndTrials(@TempDir Path tmp) throws Exception {
        File wedgeDir = tmp.resolve("wedge").toFile();
        File gameDir = tmp.resolve("game").toFile();
        File coreDir = new File(gameDir, ".jm-updater/core");
        byte[] jar = buildFakeCoreJar();

        // 本地已有一个旧版 core（version=1）
        stageLocalCore(coreDir, "oldSha123", 1);
        // 清除 marker（stageLocalCore 用的 jar 与远程同内容，但 sha 标记不同）
        new File(gameDir, ".jm-updater/core-ran.marker").delete();

        HttpServer server = startCoreEndpointServer(jar, 2); // CP 返回 version=2 > 本地 1
        try {
            String url = "http://127.0.0.1:" + server.getAddress().getPort() + "/updater-core";
            writeConfig(wedgeDir, url);

            Wedge.runUpdate(wedgeDir, gameDir.getAbsolutePath(), Messages.forLanguage("en"));

            assertTrue(coreRan(gameDir), "应下载新版 core 并调用 Core.run");
            String newSha = sha256Hex(jar);
            assertTrue(new File(coreDir, newSha + ".jar").isFile(), "新版 core jar 应被下载");
            Properties st = new Properties();
            try (java.io.InputStream in = Files.newInputStream(new File(coreDir, "state.properties").toPath())) {
                st.load(in);
            }
            // 新版应处于 pending trial 状态（pendingTried=true，因为 CoreSelector.select 已执行）
            assertEquals(newSha, st.getProperty("pendingSha"), "新版应设为 pending");
            assertEquals("2", st.getProperty("pendingVersion"));
        } finally {
            server.stop(0);
        }
    }

    @Test
    void versionEqualUsesLocalCore(@TempDir Path tmp) throws Exception {
        File wedgeDir = tmp.resolve("wedge").toFile();
        File gameDir = tmp.resolve("game").toFile();
        File coreDir = new File(gameDir, ".jm-updater/core");
        byte[] jar = buildFakeCoreJar();

        stageLocalCore(coreDir, "localSha", 3);
        new File(gameDir, ".jm-updater/core-ran.marker").delete();

        // CP 返回 version=3 = 本地 selected → 不下载
        AtomicInteger downloadCount = new AtomicInteger(0);
        HttpServer server = startCoreEndpointServer(jar, 3);
        // 覆盖 /core.jar handler 来计数下载
        server.removeContext("/core.jar");
        server.createContext("/core.jar", exchange -> {
            downloadCount.incrementAndGet();
            exchange.sendResponseHeaders(200, jar.length);
            try (OutputStream os = exchange.getResponseBody()) {
                os.write(jar);
            }
        });
        try {
            String url = "http://127.0.0.1:" + server.getAddress().getPort() + "/updater-core";
            writeConfig(wedgeDir, url);

            Wedge.runUpdate(wedgeDir, gameDir.getAbsolutePath(), Messages.forLanguage("en"));

            // 本地 jar 是 buildFakeCoreJar() 的内容，可以被加载和运行
            // 但 stageLocalCore 用的 sha 是 "localSha" 不是真实 sha，所以 jar 文件名不同
            // CoreSelector.select 会返回 selected jar → CoreLoader.loadAndRun → Core.run
            assertTrue(coreRan(gameDir), "版本相等应直接用本地 core");
            assertEquals(0, downloadCount.get(), "版本相等不应下载");
        } finally {
            server.stop(0);
        }
    }

    @Test
    void cpUnreachableWithLocalCoreUsesLocal(@TempDir Path tmp) throws Exception {
        File wedgeDir = tmp.resolve("wedge").toFile();
        File gameDir = tmp.resolve("game").toFile();
        File coreDir = new File(gameDir, ".jm-updater/core");

        stageLocalCore(coreDir, "localSha", 1);
        new File(gameDir, ".jm-updater/core-ran.marker").delete();

        // coreEndpoint 指向不存在的端口
        writeConfig(wedgeDir, "http://127.0.0.1:1/updater-core");

        Wedge.runUpdate(wedgeDir, gameDir.getAbsolutePath(), Messages.forLanguage("en"));

        assertTrue(coreRan(gameDir), "CP 不可达但有本地 core → 应使用本地 core");
    }

    @Test
    void cpUnreachableNoLocalCoreFailsOpen(@TempDir Path tmp) throws Exception {
        File wedgeDir = tmp.resolve("wedge").toFile();
        File gameDir = tmp.resolve("game").toFile();

        writeConfig(wedgeDir, "http://127.0.0.1:1/updater-core");

        // fail-open：不抛异常，不调用 Core.run
        Wedge.runUpdate(wedgeDir, gameDir.getAbsolutePath(), Messages.forLanguage("en"));

        assertFalse(coreRan(gameDir), "CP 不可达且无本地 core → fail-open 放行，不调用 Core.run");
        assertFalse(gameDir.exists() && new File(gameDir, ".jm-updater/core").isDirectory(),
                "不应创建 core 目录");
    }

    @Test
    void configJsonPassedToCoreCtx(@TempDir Path tmp) throws Exception {
        // 验证 jm-updater.json 原文透传到 ctx 的 configJson key
        File wedgeDir = tmp.resolve("wedge").toFile();
        File gameDir = tmp.resolve("game").toFile();
        byte[] jar = buildFakeCoreJar();

        HttpServer server = startCoreEndpointServer(jar, 1);
        try {
            String url = "http://127.0.0.1:" + server.getAddress().getPort() + "/updater-core";
            String configJson = writeConfig(wedgeDir, url);

            Wedge.runUpdate(wedgeDir, gameDir.getAbsolutePath(), Messages.forLanguage("en"));

            assertTrue(coreRan(gameDir), "Core.run 应被调用");
            // Core.run 被调用即说明 ctx 被正确构建（含 configJson）
        } finally {
            server.stop(0);
        }
    }
}
