package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * updater-core reconcile 端到端测试（FR-090 / FR-256 简化后）：用本地临时 gameDir + 内存 Transport，
 * 不依赖真端点。覆盖增量/减量、托管区/玩家区隔离、sync 策略、fail-static、平台过滤、并发锁。
 *
 * <p>FR-256 起 manifest 不再签名（去 TestSigner），CAS/验签/防降级相关测试已删；
 * 保留 reconcile / 快筛 / clean-all / 平台过滤 / 单实例锁 / 非法路径测试。
 */
class UpdaterTest {

    private byte[] bytes(String s) {
        return s.getBytes(StandardCharsets.UTF_8);
    }

    private Updater updater(Path gameDir, Transport transport) {
        return new Updater(gameDir, transport, false);
    }

    /** 构造 manifest 并挂到 transport（FR-256 起 manifest 不再签名，直接 canonical JSON）。 */
    private void install(TestFixtures.MemoryTransport transport,
                         long version, List<String> managedDirs,
                         List<TestFixtures.FileSpec> specs) throws Exception {
        Map<String, Object> manifest = TestFixtures.buildManifest(
                "skyblock-s1", version, managedDirs, specs, transport);
        transport.manifestJson = Json.canonical(manifest);
    }

    @Test
    void incrementDownloadsMissingFile(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        byte[] foo = bytes("foo jar content");
        install(transport, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/foo.jar", foo)));

        int rc = updater(gameDir, transport).run();

        assertEquals(Updater.OK, rc);
        Path written = gameDir.resolve("mods/foo.jar");
        assertTrue(Files.isRegularFile(written));
        assertArrayEquals(foo, Files.readAllBytes(written));
        assertEquals(1, transport.artifactFetchCount, "缺失文件应下载一次");
        Path logFile = gameDir.resolve(".jm-updater/logs/updater.log");
        assertTrue(Files.isRegularFile(logFile), "updater-core 应写入本地日志文件");
        String log = new String(Files.readAllBytes(logFile), StandardCharsets.UTF_8);
        assertTrue(log.contains("开始拉取 manifest"), "日志应包含 manifest 拉取节点");
        assertTrue(log.contains("更新成功，放行游戏 version=1"), "日志应包含成功放行节点");
    }

    @Test
    void quickSkipWhenFileAlreadyMatches(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        byte[] foo = bytes("already present");
        // 本地预置一致文件。
        Files.createDirectories(gameDir.resolve("mods"));
        Files.write(gameDir.resolve("mods/foo.jar"), foo);

        install(transport, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/foo.jar", foo)));

        int rc = updater(gameDir, transport).run();

        assertEquals(Updater.OK, rc);
        assertEquals(0, transport.artifactFetchCount, "md5/size 快筛命中应跳过下载");
    }

    @Test
    void decrementRemovesStaleFileInManagedDir(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        // 本地有 manifest 未列的旧文件。
        Files.createDirectories(gameDir.resolve("mods"));
        Files.write(gameDir.resolve("mods/stale.jar"), bytes("old mod"));

        byte[] keep = bytes("keep this");
        install(transport, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/keep.jar", keep)));

        int rc = updater(gameDir, transport).run();

        assertEquals(Updater.OK, rc);
        assertFalse(Files.exists(gameDir.resolve("mods/stale.jar")), "托管区内 manifest 未列文件应被减量删除");
        assertTrue(Files.isRegularFile(gameDir.resolve("mods/keep.jar")));
    }

    @Test
    void playerZoneNeverTouched(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        // 玩家区文件（managedDirs 之外）。
        Files.createDirectories(gameDir.resolve("saves/world"));
        Files.write(gameDir.resolve("saves/world/level.dat"), bytes("player save"));
        Files.write(gameDir.resolve("options.txt"), bytes("player options"));
        Files.createDirectories(gameDir.resolve("screenshots"));
        Files.write(gameDir.resolve("screenshots/shot.png"), bytes("png"));

        byte[] mod = bytes("mod");
        install(transport, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/a.jar", mod)));

        int rc = updater(gameDir, transport).run();

        assertEquals(Updater.OK, rc);
        assertArrayEquals(bytes("player save"), Files.readAllBytes(gameDir.resolve("saves/world/level.dat")));
        assertArrayEquals(bytes("player options"), Files.readAllBytes(gameDir.resolve("options.txt")));
        assertArrayEquals(bytes("png"), Files.readAllBytes(gameDir.resolve("screenshots/shot.png")));
    }

    // ── FR-255：clean-all（"*" 哨兵）+ 自定义排除 ──────────────────────────

    /** 带 cleanExclude 的 install 辅助（FR-255）。 */
    private void installWithExclude(TestFixtures.MemoryTransport transport,
                                    long version, List<String> managedDirs, List<String> cleanExclude,
                                    List<TestFixtures.FileSpec> specs) throws Exception {
        Map<String, Object> manifest = TestFixtures.buildManifest(
                "skyblock-s1", version, managedDirs, cleanExclude, specs, transport);
        transport.manifestJson = Json.canonical(manifest);
    }

    @Test
    void cleanAllDeletesExtraFilesButPreservesPlayerZone(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();

        // 本地有各类「多余」文件：托管外的旧 mod、玩家存档、截图。
        Files.createDirectories(gameDir.resolve("mods"));
        Files.write(gameDir.resolve("mods/stale.jar"), bytes("old mod"));
        Files.createDirectories(gameDir.resolve("saves/world"));
        Files.write(gameDir.resolve("saves/world/level.dat"), bytes("player save"));
        Files.write(gameDir.resolve("options.txt"), bytes("player options"));
        Files.write(gameDir.resolve("random.txt"), bytes("orphan file"));

        byte[] keep = bytes("keep this");
        // managedDirs=["*"]：clean-all 模式。
        installWithExclude(transport, 1,
                Collections.singletonList("*"), null,
                Collections.singletonList(new TestFixtures.FileSpec("mods/keep.jar", keep)));

        int rc = updater(gameDir, transport).run();

        assertEquals(Updater.OK, rc);
        // 托管区内 manifest 未列文件应被删。
        assertFalse(Files.exists(gameDir.resolve("mods/stale.jar")), "clean-all 应删除多余文件");
        assertFalse(Files.exists(gameDir.resolve("random.txt")), "clean-all 应删除托管外多余文件");
        assertTrue(Files.isRegularFile(gameDir.resolve("mods/keep.jar")));
        // 玩家区必须完好。
        assertArrayEquals(bytes("player save"), Files.readAllBytes(gameDir.resolve("saves/world/level.dat")));
        assertArrayEquals(bytes("player options"), Files.readAllBytes(gameDir.resolve("options.txt")));
    }

    @Test
    void cleanAllPreservesCustomExclude(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();

        // 玩家自装的 mod 目录（运营排除）+ 一个应被删的多余文件。
        Files.createDirectories(gameDir.resolve("mymods"));
        Files.write(gameDir.resolve("mymods/custom.jar"), bytes("player installed"));
        Files.write(gameDir.resolve("orphan.txt"), bytes("should be deleted"));

        byte[] keep = bytes("keep");
        installWithExclude(transport, 1,
                Collections.singletonList("*"), Collections.singletonList("mymods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/keep.jar", keep)));

        int rc = updater(gameDir, transport).run();

        assertEquals(Updater.OK, rc);
        // cleanExclude 命中的目录下文件永不删。
        assertArrayEquals(bytes("player installed"), Files.readAllBytes(gameDir.resolve("mymods/custom.jar")));
        // 未列且未排除的多余文件应被删。
        assertFalse(Files.exists(gameDir.resolve("orphan.txt")));
    }

    @Test
    void cleanAllPreservesPlayerZoneAndExcludeSimultaneously(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();

        // 玩家区 + 自定义排除 + 多余文件三种并存。
        Files.write(gameDir.resolve("options.txt"), bytes("player options"));
        Files.createDirectories(gameDir.resolve("mymods"));
        Files.write(gameDir.resolve("mymods/x.jar"), bytes("custom"));
        Files.write(gameDir.resolve("orphan.txt"), bytes("orphan"));

        byte[] keep = bytes("keep");
        installWithExclude(transport, 1,
                Collections.singletonList("*"),
                Arrays.asList("mymods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/keep.jar", keep)));

        int rc = updater(gameDir, transport).run();

        assertEquals(Updater.OK, rc);
        // 玩家区完好。
        assertArrayEquals(bytes("player options"), Files.readAllBytes(gameDir.resolve("options.txt")));
        // 自定义排除完好。
        assertArrayEquals(bytes("custom"), Files.readAllBytes(gameDir.resolve("mymods/x.jar")));
        // 多余文件被删。
        assertFalse(Files.exists(gameDir.resolve("orphan.txt")));
    }

    @Test
    void syncOnceWritesOnlyWhenMissing(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        // 本地已有玩家改过的 config（once 语义：不覆盖）。
        Files.createDirectories(gameDir.resolve("config"));
        Files.write(gameDir.resolve("config/prefs.toml"), bytes("player edited"));

        install(transport, 1, Collections.singletonList("config"),
                Collections.singletonList(
                        new TestFixtures.FileSpec("config/prefs.toml", bytes("default")).sync("once")));

        int rc = updater(gameDir, transport).run();

        assertEquals(Updater.OK, rc);
        assertArrayEquals(bytes("player edited"), Files.readAllBytes(gameDir.resolve("config/prefs.toml")),
                "sync=once 且本地存在时不得覆盖玩家修改");
        assertEquals(0, transport.artifactFetchCount);
    }

    @Test
    void syncOnceWritesWhenAbsent(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        byte[] def = bytes("default config");
        install(transport, 1, Collections.singletonList("config"),
                Collections.singletonList(
                        new TestFixtures.FileSpec("config/prefs.toml", def).sync("once")));

        int rc = updater(gameDir, transport).run();

        assertEquals(Updater.OK, rc);
        assertArrayEquals(def, Files.readAllBytes(gameDir.resolve("config/prefs.toml")),
                "sync=once 且本地缺失时应写入默认");
    }

    @Test
    void syncStrictOverwritesEditedFile(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        Files.createDirectories(gameDir.resolve("config"));
        Files.write(gameDir.resolve("config/pack.toml"), bytes("tampered"));

        byte[] authoritative = bytes("authoritative pack config");
        install(transport, 1, Collections.singletonList("config"),
                Collections.singletonList(
                        new TestFixtures.FileSpec("config/pack.toml", authoritative).sync("strict")));

        int rc = updater(gameDir, transport).run();

        assertEquals(Updater.OK, rc);
        assertArrayEquals(authoritative, Files.readAllBytes(gameDir.resolve("config/pack.toml")),
                "sync=strict 应强制与 manifest 一致");
    }

    @Test
    void syncIgnoreNeitherWritesNorRemoves(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        Files.createDirectories(gameDir.resolve("config"));
        Files.write(gameDir.resolve("config/local.toml"), bytes("local untouched"));

        install(transport, 1, Collections.singletonList("config"),
                Collections.singletonList(
                        new TestFixtures.FileSpec("config/local.toml", bytes("server version")).sync("ignore")));

        int rc = updater(gameDir, transport).run();

        assertEquals(Updater.OK, rc);
        assertArrayEquals(bytes("local untouched"), Files.readAllBytes(gameDir.resolve("config/local.toml")),
                "sync=ignore 既不写也不删");
        assertEquals(0, transport.artifactFetchCount);
    }

    @Test
    void unreachableEndpointFailStatic(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        transport.manifestUnreachable = true;

        // 本地预置一个旧文件，验证断网时它被保留（带本地版本进游戏）。
        Files.createDirectories(gameDir.resolve("mods"));
        Files.write(gameDir.resolve("mods/existing.jar"), bytes("local"));

        int rc = updater(gameDir, transport).run();
        assertEquals(Updater.FAIL_STATIC, rc, "端点不可达必须 fail-static");
        assertArrayEquals(bytes("local"), Files.readAllBytes(gameDir.resolve("mods/existing.jar")),
                "断网时本地文件应保留");
    }

    @Test
    void platformFilterSkipsForeignPlatformFiles(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        String foreign = Platform.current() == Platform.WINDOWS ? "linux" : "windows";

        install(transport, 1, Collections.singletonList("mods"), Arrays.asList(
                new TestFixtures.FileSpec("mods/universal.jar", bytes("all")).platform(null),
                new TestFixtures.FileSpec("mods/foreign.jar", bytes("other os")).platform(foreign)));

        int rc = updater(gameDir, transport).run();

        assertEquals(Updater.OK, rc);
        assertTrue(Files.isRegularFile(gameDir.resolve("mods/universal.jar")), "全平台文件应写入");
        assertFalse(Files.exists(gameDir.resolve("mods/foreign.jar")), "异平台文件应跳过");
    }

    @Test
    void singleInstanceLockBlocksConcurrent(@TempDir Path gameDir) throws Exception {
        Path stateDir = gameDir.resolve(".jm-updater");
        try (SingleInstanceLock held = SingleInstanceLock.tryAcquire(stateDir)) {
            assertTrue(held != null);
            TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
            install(transport, 1, Collections.singletonList("mods"),
                    Collections.singletonList(new TestFixtures.FileSpec("mods/a.jar", bytes("x"))));

            // 锁被占用时第二个 updater 应退让（BUSY），不并发改目录。
            int rc = updater(gameDir, transport).run();
            assertEquals(Updater.BUSY, rc, "已被占用时应退让放行");
            assertFalse(Files.exists(gameDir.resolve("mods/a.jar")));
        }
    }

    @Test
    void escapePathInManifestRejectedAsError(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        // 构造含逃逸路径的文件（manifest 即便内容合法，也不得写出 gameDir）。
        Map<String, Object> manifest = TestFixtures.buildManifest(
                "skyblock-s1", 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("../../evil.jar", bytes("evil"))),
                transport);
        transport.manifestJson = Json.canonical(manifest);

        int rc = updater(gameDir, transport).run();
        assertEquals(Updater.FAIL_STATIC, rc, "非法逃逸路径应记错误并 fail-static");
        assertFalse(Files.exists(gameDir.getParent().resolve("evil.jar")));
    }
}
