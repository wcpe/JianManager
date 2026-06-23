package top.jm.updater.core;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * updater-core 自更新（FR-091）：用内存 Transport 喂 manifest.agent.core 制品 + 注入 selftest 桩，
 * 验证下载/sha256 校验/selftest/暂存 pending 的状态机，不依赖真 jar（真 jar selftest 见 CoreSelfTestRealJarTest）。
 */
class SelfUpdaterTest {

    private static final CoreSelfTest OK = jar -> true;
    private static final CoreSelfTest FAIL = jar -> false;

    private byte[] bytes(String s) {
        return s.getBytes(StandardCharsets.UTF_8);
    }

    /** 构造一份带 agent.core 段的 manifest（core 制品注册进 transport），返回解析后的 Manifest。 */
    private Manifest manifestWithCore(TestFixtures.MemoryTransport t, long coreVersion,
                                      byte[] coreJar, String codec, String platformTag) {
        Map<String, Object> m = TestFixtures.buildManifest("ch", 10,
                Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/a.jar", bytes("x"))), t);
        byte[] art = TestFixtures.encode(coreJar, codec);
        String artSha = Hashes.sha256(art);
        t.artifacts.put(artSha, art);

        Map<String, Object> artMap = new LinkedHashMap<>();
        artMap.put("sha256", artSha);
        artMap.put("size", (long) art.length);
        artMap.put("codec", codec);
        Map<String, Object> platEntry = new LinkedHashMap<>();
        platEntry.put("artifact", artMap);
        Map<String, Object> platforms = new LinkedHashMap<>();
        platforms.put(platformTag, platEntry);
        Map<String, Object> core = new LinkedHashMap<>();
        core.put("version", coreVersion);
        core.put("platforms", platforms);
        Map<String, Object> agent = new LinkedHashMap<>();
        agent.put("core", core);
        m.put("agent", agent);
        return Manifest.parse(Json.canonical(m));
    }

    private SelfUpdater selfUpdater(Path gameDir, TestFixtures.MemoryTransport t, CoreSelfTest st) {
        Path stateDir = gameDir.resolve(".jm-updater");
        return new SelfUpdater(stateDir, t, st, Logger.create(stateDir));
    }

    private CoreSelectStore stateOf(Path gameDir) {
        return CoreSelectStore.load(gameDir.resolve(".jm-updater").resolve("core"));
    }

    @Test
    void manifestParsesAgentCoreSegment() {
        TestFixtures.MemoryTransport t = new TestFixtures.MemoryTransport();
        Manifest m = manifestWithCore(t, 5, bytes("core-jar-v5"), "zstd", "windows");
        assertEquals(5, m.agentCoreVersion);
        assertNotNull(m.agentCoreArtifact("windows"));
        assertEquals("none", "none"); // codec 取值见制品
        // 未声明平台返回 null。
        org.junit.jupiter.api.Assertions.assertNull(m.agentCoreArtifact("macos"));
    }

    @Test
    void manifestWithoutAgentCoreYieldsNoVersion() {
        TestFixtures.MemoryTransport t = new TestFixtures.MemoryTransport();
        Map<String, Object> m = TestFixtures.buildManifest("ch", 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/a.jar", bytes("x"))), t);
        Manifest parsed = Manifest.parse(Json.canonical(m));
        assertEquals(-1, parsed.agentCoreVersion);
        org.junit.jupiter.api.Assertions.assertNull(parsed.agentCoreArtifact("windows"));
    }

    @Test
    void stagesPendingWhenCoreVersionNewer(@TempDir Path gameDir) {
        TestFixtures.MemoryTransport t = new TestFixtures.MemoryTransport();
        byte[] coreJar = bytes("new-core-bytes-v6");
        String tag = Platform.current().tag();
        Manifest m = manifestWithCore(t, 6, coreJar, "zstd", tag);

        boolean staged = selfUpdater(gameDir, t, OK).maybeUpdate(m, 0, Platform.current());

        assertTrue(staged, "更高版本应暂存 pending");
        CoreSelectStore s = stateOf(gameDir);
        assertEquals(6, s.pendingVersion());
        assertEquals(Hashes.sha256(coreJar), s.pendingSha(), "pending sha 应为解码后 jar 的 sha256");
        Path jar = gameDir.resolve(".jm-updater").resolve("core").resolve(s.pendingSha() + ".jar");
        assertTrue(Files.isRegularFile(jar), "pending core jar 应已落地");
    }

    @Test
    void noUpdateWhenNotNewer(@TempDir Path gameDir) {
        TestFixtures.MemoryTransport t = new TestFixtures.MemoryTransport();
        Manifest m = manifestWithCore(t, 3, bytes("v3"), "zstd", Platform.current().tag());
        // 运行版本已是 3 → 不更新。
        assertFalse(selfUpdater(gameDir, t, OK).maybeUpdate(m, 3, Platform.current()));
        assertFalse(stateOf(gameDir).hasPending());
    }

    @Test
    void rejectsWhenSelfTestFails(@TempDir Path gameDir) {
        TestFixtures.MemoryTransport t = new TestFixtures.MemoryTransport();
        Manifest m = manifestWithCore(t, 6, bytes("bad-core"), "zstd", Platform.current().tag());

        assertFalse(selfUpdater(gameDir, t, FAIL).maybeUpdate(m, 0, Platform.current()),
                "selftest 不通过不得暂存");
        assertFalse(stateOf(gameDir).hasPending(), "selftest 失败后不得留 pending");
        Path coreDir = gameDir.resolve(".jm-updater").resolve("core");
        // 不应残留 .jar（.tmp 已删）。
        if (Files.isDirectory(coreDir)) {
            try {
                long jars = Files.list(coreDir).filter(p -> p.toString().endsWith(".jar")).count();
                assertEquals(0, jars, "selftest 失败不得落定 jar");
            } catch (Exception e) {
                throw new RuntimeException(e);
            }
        }
    }

    @Test
    void skipsWhenPendingAlreadyInFlight(@TempDir Path gameDir) {
        TestFixtures.MemoryTransport t = new TestFixtures.MemoryTransport();
        // 先暂存 v6。
        assertTrue(selfUpdater(gameDir, t,
                OK).maybeUpdate(manifestWithCore(t, 6, bytes("v6"), "zstd", Platform.current().tag()),
                0, Platform.current()));
        // 再来 v7：已有未决 pending → 串行跳过，仍是 v6。
        TestFixtures.MemoryTransport t2 = new TestFixtures.MemoryTransport();
        assertFalse(selfUpdater(gameDir, t2,
                OK).maybeUpdate(manifestWithCore(t2, 7, bytes("v7"), "zstd", Platform.current().tag()),
                0, Platform.current()));
        assertEquals(6, stateOf(gameDir).pendingVersion());
    }

    @Test
    void skipsFailedVersionUntilHigher(@TempDir Path gameDir) throws Exception {
        // 模拟 wedge 在上次 trial 未确认时记下 failedVersion=6（坏 core）。
        Path coreDir = gameDir.resolve(".jm-updater").resolve("core");
        Files.createDirectories(coreDir);
        java.util.Properties p = new java.util.Properties();
        p.setProperty("failedVersion", "6");
        try (java.io.OutputStream out = Files.newOutputStream(coreDir.resolve("state.properties"))) {
            p.store(out, "seed failedVersion");
        }

        // 同一失败版本 v6 → 不得重暂存（否则形成 boot-loop，FR-091 真机修）。
        TestFixtures.MemoryTransport t = new TestFixtures.MemoryTransport();
        assertFalse(selfUpdater(gameDir, t, OK).maybeUpdate(
                manifestWithCore(t, 6, bytes("v6"), "zstd", Platform.current().tag()), 0, Platform.current()),
                "曾 trial 失败的版本不得被重暂存");
        assertFalse(stateOf(gameDir).hasPending());

        // 更高版本 v7（修复版）→ 允许暂存。
        TestFixtures.MemoryTransport t2 = new TestFixtures.MemoryTransport();
        assertTrue(selfUpdater(gameDir, t2, OK).maybeUpdate(
                manifestWithCore(t2, 7, bytes("v7"), "zstd", Platform.current().tag()), 0, Platform.current()),
                "更高版本（修复版）应允许暂存");
        assertEquals(7, stateOf(gameDir).pendingVersion());
    }

    @Test
    void skipsWhenNoArtifactForPlatform(@TempDir Path gameDir) {
        TestFixtures.MemoryTransport t = new TestFixtures.MemoryTransport();
        String foreign = Platform.current() == Platform.WINDOWS ? "linux" : "windows";
        Manifest m = manifestWithCore(t, 6, bytes("v6"), "zstd", foreign);
        assertFalse(selfUpdater(gameDir, t, OK).maybeUpdate(m, 0, Platform.current()),
                "本平台无 core 制品应跳过");
        assertFalse(stateOf(gameDir).hasPending());
    }
}
