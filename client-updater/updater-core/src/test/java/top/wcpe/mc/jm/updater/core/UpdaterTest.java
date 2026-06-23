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
 * updater-core reconcile 端到端测试（FR-090）：用本地临时 gameDir + 内存 Transport + 注入测试公钥，
 * 不依赖真端点。覆盖增量/减量、托管区/玩家区隔离、sync 策略、防降级、fail-static、CAS、平台过滤、并发锁。
 */
class UpdaterTest {

    private static final String KEY_ID = "k1";

    private byte[] bytes(String s) {
        return s.getBytes(StandardCharsets.UTF_8);
    }

    private Updater updater(Path gameDir, Transport transport, TestSigner signer) {
        return new Updater(gameDir, transport, Signatures.withTrustStore(signer.trustStore()));
    }

    /** 构造 + 签名 manifest 并挂到 transport。 */
    private void install(TestFixtures.MemoryTransport transport, TestSigner signer,
                         long version, List<String> managedDirs,
                         List<TestFixtures.FileSpec> specs) throws Exception {
        Map<String, Object> manifest = TestFixtures.buildManifest(
                "skyblock-s1", version, managedDirs, specs, transport);
        transport.manifestJson = signer.sign(manifest);
    }

    @Test
    void incrementDownloadsMissingFile(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        byte[] foo = bytes("foo jar content");
        install(transport, signer, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/foo.jar", foo)));

        int rc = updater(gameDir, transport, signer).run();

        assertEquals(Updater.OK, rc);
        Path written = gameDir.resolve("mods/foo.jar");
        assertTrue(Files.isRegularFile(written));
        assertArrayEquals(foo, Files.readAllBytes(written));
        assertEquals(1, transport.artifactFetchCount, "缺失文件应下载一次");
    }

    @Test
    void quickSkipWhenFileAlreadyMatches(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        byte[] foo = bytes("already present");
        // 本地预置一致文件。
        Files.createDirectories(gameDir.resolve("mods"));
        Files.write(gameDir.resolve("mods/foo.jar"), foo);

        install(transport, signer, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/foo.jar", foo)));

        int rc = updater(gameDir, transport, signer).run();

        assertEquals(Updater.OK, rc);
        assertEquals(0, transport.artifactFetchCount, "md5/size 快筛命中应跳过下载");
    }

    @Test
    void decrementRemovesStaleFileInManagedDir(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        // 本地有 manifest 未列的旧文件。
        Files.createDirectories(gameDir.resolve("mods"));
        Files.write(gameDir.resolve("mods/stale.jar"), bytes("old mod"));

        byte[] keep = bytes("keep this");
        install(transport, signer, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/keep.jar", keep)));

        int rc = updater(gameDir, transport, signer).run();

        assertEquals(Updater.OK, rc);
        assertFalse(Files.exists(gameDir.resolve("mods/stale.jar")), "托管区内 manifest 未列文件应被减量删除");
        assertTrue(Files.isRegularFile(gameDir.resolve("mods/keep.jar")));
    }

    @Test
    void playerZoneNeverTouched(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        // 玩家区文件（managedDirs 之外）。
        Files.createDirectories(gameDir.resolve("saves/world"));
        Files.write(gameDir.resolve("saves/world/level.dat"), bytes("player save"));
        Files.write(gameDir.resolve("options.txt"), bytes("player options"));
        Files.createDirectories(gameDir.resolve("screenshots"));
        Files.write(gameDir.resolve("screenshots/shot.png"), bytes("png"));

        byte[] mod = bytes("mod");
        install(transport, signer, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/a.jar", mod)));

        int rc = updater(gameDir, transport, signer).run();

        assertEquals(Updater.OK, rc);
        assertArrayEquals(bytes("player save"), Files.readAllBytes(gameDir.resolve("saves/world/level.dat")));
        assertArrayEquals(bytes("player options"), Files.readAllBytes(gameDir.resolve("options.txt")));
        assertArrayEquals(bytes("png"), Files.readAllBytes(gameDir.resolve("screenshots/shot.png")));
    }

    @Test
    void syncOnceWritesOnlyWhenMissing(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        // 本地已有玩家改过的 config（once 语义：不覆盖）。
        Files.createDirectories(gameDir.resolve("config"));
        Files.write(gameDir.resolve("config/prefs.toml"), bytes("player edited"));

        install(transport, signer, 1, Collections.singletonList("config"),
                Collections.singletonList(
                        new TestFixtures.FileSpec("config/prefs.toml", bytes("default")).sync("once")));

        int rc = updater(gameDir, transport, signer).run();

        assertEquals(Updater.OK, rc);
        assertArrayEquals(bytes("player edited"), Files.readAllBytes(gameDir.resolve("config/prefs.toml")),
                "sync=once 且本地存在时不得覆盖玩家修改");
        assertEquals(0, transport.artifactFetchCount);
    }

    @Test
    void syncOnceWritesWhenAbsent(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        byte[] def = bytes("default config");
        install(transport, signer, 1, Collections.singletonList("config"),
                Collections.singletonList(
                        new TestFixtures.FileSpec("config/prefs.toml", def).sync("once")));

        int rc = updater(gameDir, transport, signer).run();

        assertEquals(Updater.OK, rc);
        assertArrayEquals(def, Files.readAllBytes(gameDir.resolve("config/prefs.toml")),
                "sync=once 且本地缺失时应写入默认");
    }

    @Test
    void syncStrictOverwritesEditedFile(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        Files.createDirectories(gameDir.resolve("config"));
        Files.write(gameDir.resolve("config/pack.toml"), bytes("tampered"));

        byte[] authoritative = bytes("authoritative pack config");
        install(transport, signer, 1, Collections.singletonList("config"),
                Collections.singletonList(
                        new TestFixtures.FileSpec("config/pack.toml", authoritative).sync("strict")));

        int rc = updater(gameDir, transport, signer).run();

        assertEquals(Updater.OK, rc);
        assertArrayEquals(authoritative, Files.readAllBytes(gameDir.resolve("config/pack.toml")),
                "sync=strict 应强制与 manifest 一致");
    }

    @Test
    void syncIgnoreNeitherWritesNorRemoves(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        Files.createDirectories(gameDir.resolve("config"));
        Files.write(gameDir.resolve("config/local.toml"), bytes("local untouched"));

        install(transport, signer, 1, Collections.singletonList("config"),
                Collections.singletonList(
                        new TestFixtures.FileSpec("config/local.toml", bytes("server version")).sync("ignore")));

        int rc = updater(gameDir, transport, signer).run();

        assertEquals(Updater.OK, rc);
        assertArrayEquals(bytes("local untouched"), Files.readAllBytes(gameDir.resolve("config/local.toml")),
                "sync=ignore 既不写也不删");
        assertEquals(0, transport.artifactFetchCount);
    }

    @Test
    void antiDowngradeRejectsLowerVersion(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        // 先成功更新到 version 5。
        TestFixtures.MemoryTransport t1 = new TestFixtures.MemoryTransport();
        install(t1, signer, 5, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/a.jar", bytes("v5"))));
        assertEquals(Updater.OK, updater(gameDir, t1, signer).run());

        // 再投放 version 3（合法签名的旧 manifest，重放攻击）。
        TestFixtures.MemoryTransport t2 = new TestFixtures.MemoryTransport();
        install(t2, signer, 3, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/evil.jar", bytes("v3 rollback"))));
        int rc = updater(gameDir, t2, signer).run();

        assertEquals(Updater.FAIL_STATIC, rc, "version 低于已见最高版本必须拒绝（防降级）");
        assertFalse(Files.exists(gameDir.resolve("mods/evil.jar")), "防降级拒绝后不得改目录");
    }

    @Test
    void sameVersionReapplyAllowed(@TempDir Path gameDir) throws Exception {
        // version 相等（== lastSeen）应允许（幂等重跑），仅严格更低才拒。
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        install(transport, signer, 7, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/a.jar", bytes("v7"))));
        assertEquals(Updater.OK, updater(gameDir, transport, signer).run());

        // 同 version 再跑。
        TestFixtures.MemoryTransport t2 = new TestFixtures.MemoryTransport();
        install(t2, signer, 7, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/a.jar", bytes("v7"))));
        assertEquals(Updater.OK, updater(gameDir, t2, signer).run());
    }

    @Test
    void invalidSignatureFailStatic(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        install(transport, signer, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/a.jar", bytes("x"))));
        // 篡改签名后内容。
        transport.manifestJson = transport.manifestJson.replace("skyblock-s1", "hacked-channel");

        int rc = updater(gameDir, transport, signer).run();
        assertEquals(Updater.FAIL_STATIC, rc, "验签失败必须 fail-static");
        assertFalse(Files.exists(gameDir.resolve("mods/a.jar")), "验签失败不得改目录");
    }

    @Test
    void unreachableEndpointFailStatic(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        transport.manifestUnreachable = true;

        // 本地预置一个旧文件，验证断网时它被保留（带本地版本进游戏）。
        Files.createDirectories(gameDir.resolve("mods"));
        Files.write(gameDir.resolve("mods/existing.jar"), bytes("local"));

        int rc = updater(gameDir, transport, signer).run();
        assertEquals(Updater.FAIL_STATIC, rc, "端点不可达必须 fail-static");
        assertArrayEquals(bytes("local"), Files.readAllBytes(gameDir.resolve("mods/existing.jar")),
                "断网时本地文件应保留");
    }

    @Test
    void platformFilterSkipsForeignPlatformFiles(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        String foreign = Platform.current() == Platform.WINDOWS ? "linux" : "windows";

        install(transport, signer, 1, Collections.singletonList("mods"), Arrays.asList(
                new TestFixtures.FileSpec("mods/universal.jar", bytes("all")).platform(null),
                new TestFixtures.FileSpec("mods/foreign.jar", bytes("other os")).platform(foreign)));

        int rc = updater(gameDir, transport, signer).run();

        assertEquals(Updater.OK, rc);
        assertTrue(Files.isRegularFile(gameDir.resolve("mods/universal.jar")), "全平台文件应写入");
        assertFalse(Files.exists(gameDir.resolve("mods/foreign.jar")), "异平台文件应跳过");
    }

    @Test
    void casHitAvoidsRedownload(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        byte[] shared = bytes("shared content across versions");

        // version 1：下载并入 CAS。
        TestFixtures.MemoryTransport t1 = new TestFixtures.MemoryTransport();
        install(t1, signer, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/a.jar", shared)));
        assertEquals(Updater.OK, updater(gameDir, t1, signer).run());
        assertEquals(1, t1.artifactFetchCount);

        // 删除已写文件，使下次需要重新放置；但同内容已在 CAS。
        Files.delete(gameDir.resolve("mods/a.jar"));

        // version 2：同内容出现在不同路径，应命中 CAS、零下载。
        TestFixtures.MemoryTransport t2 = new TestFixtures.MemoryTransport();
        install(t2, signer, 2, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/b.jar", shared)));
        assertEquals(Updater.OK, updater(gameDir, t2, signer).run());
        assertEquals(0, t2.artifactFetchCount, "相同内容应命中 CAS，免重新下载");
        assertArrayEquals(shared, Files.readAllBytes(gameDir.resolve("mods/b.jar")));
    }

    @Test
    void singleInstanceLockBlocksConcurrent(@TempDir Path gameDir) throws Exception {
        Path stateDir = gameDir.resolve(".jm-updater");
        try (SingleInstanceLock held = SingleInstanceLock.tryAcquire(stateDir)) {
            assertTrue(held != null);
            TestSigner signer = new TestSigner(KEY_ID);
            TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
            install(transport, signer, 1, Collections.singletonList("mods"),
                    Collections.singletonList(new TestFixtures.FileSpec("mods/a.jar", bytes("x"))));

            // 锁被占用时第二个 updater 应退让（BUSY），不并发改目录。
            int rc = updater(gameDir, transport, signer).run();
            assertEquals(Updater.BUSY, rc, "已被占用时应退让放行");
            assertFalse(Files.exists(gameDir.resolve("mods/a.jar")));
        }
    }

    @Test
    void escapePathInManifestRejectedAsError(@TempDir Path gameDir) throws Exception {
        TestSigner signer = new TestSigner(KEY_ID);
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        // 构造含逃逸路径的文件（攻击者即便签名合法，也不得写出 gameDir）。
        Map<String, Object> manifest = TestFixtures.buildManifest(
                "skyblock-s1", 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("../../evil.jar", bytes("evil"))),
                transport);
        transport.manifestJson = signer.sign(manifest);

        int rc = updater(gameDir, transport, signer).run();
        assertEquals(Updater.FAIL_STATIC, rc, "非法逃逸路径应记错误并 fail-static");
        assertFalse(Files.exists(gameDir.getParent().resolve("evil.jar")));
    }
}
