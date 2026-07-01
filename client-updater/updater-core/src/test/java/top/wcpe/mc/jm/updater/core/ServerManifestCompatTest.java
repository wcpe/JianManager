package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * 服务端 manifest 兼容性固化测试（FR-087 契约硬验证）。
 *
 * <p>{@link #GOLDEN} 是 JM 服务端对契约 §2 样例**真实输出**的 manifest JSON（由 Go 侧
 * service 测试 emit、原样粘入）。本测试证明服务端输出的字段名/嵌套/类型可被
 * {@link Manifest#parse} 解析（两线接口对齐）。
 *
 * <p>FR-256 起 manifest 不再携带签名段（信任模型改为 HTTPS + 拉取密钥鉴权），故 GOLDEN
 * 已去掉 sig 段，验签相关断言/方法已删除，仅保留解析兼容性校验。
 *
 * <p>更新规则：服务端字段若变更，须重跑 Go 侧 emit 并替换 {@link #GOLDEN}，双线同步。
 */
class ServerManifestCompatTest {

    /** JM 服务端真实输出（dev 私钥已去，contract §2 样例，FR-256 起无 sig 段）。勿手改，须由服务端 emit 同步。 */
    private static final String GOLDEN = "{\n"
            + "  \"agent\": {\n"
            + "    \"core\": {\n"
            + "      \"platforms\": {\n"
            + "        \"windows\": {\n"
            + "          \"artifact\": {\n"
            + "            \"codec\": \"zstd\",\n"
            + "            \"sha256\": \"c1\",\n"
            + "            \"size\": 100\n"
            + "          }\n"
            + "        }\n"
            + "      },\n"
            + "      \"version\": 5\n"
            + "    },\n"
            + "    \"wedge\": {\n"
            + "      \"version\": 3\n"
            + "    }\n"
            + "  },\n"
            + "  \"channel\": \"skyblock-s1\",\n"
            + "  \"files\": [\n"
            + "    {\n"
            + "      \"artifact\": {\n"
            + "        \"codec\": \"zstd\",\n"
            + "        \"sha256\": \"ef56\",\n"
            + "        \"size\": 45678\n"
            + "      },\n"
            + "      \"md5\": \"cd34\",\n"
            + "      \"path\": \"mods/foo.jar\",\n"
            + "      \"platform\": null,\n"
            + "      \"sha256\": \"ab12\",\n"
            + "      \"size\": 123456,\n"
            + "      \"sync\": \"strict\"\n"
            + "    },\n"
            + "    {\n"
            + "      \"artifact\": {\n"
            + "        \"codec\": \"none\",\n"
            + "        \"sha256\": \"aa00\",\n"
            + "        \"size\": 20\n"
            + "      },\n"
            + "      \"md5\": \"7766\",\n"
            + "      \"path\": \"config/opt.txt\",\n"
            + "      \"platform\": \"windows\",\n"
            + "      \"sha256\": \"9988\",\n"
            + "      \"size\": 12,\n"
            + "      \"sync\": \"once\"\n"
            + "    }\n"
            + "  ],\n"
            + "  \"issuedAt\": \"2026-06-23T10:00:00Z\",\n"
            + "  \"managedDirs\": [\n"
            + "    \"mods\",\n"
            + "    \"config\"\n"
            + "  ],\n"
            + "  \"schemaVersion\": 1,\n"
            + "  \"version\": 42\n"
            + "}";

    @Test
    void parsesServerOutput() {
        Manifest m = Manifest.parse(GOLDEN);
        assertEquals(1, m.schemaVersion);
        assertEquals("skyblock-s1", m.channel);
        assertEquals(42L, m.version);
        assertEquals(2, m.files.size());
        assertEquals("mods/foo.jar", m.files.get(0).path);
        assertEquals("ab12", m.files.get(0).sha256);
        assertEquals("ef56", m.files.get(0).artifactSha256);
        assertEquals("zstd", m.files.get(0).artifactCodec);
        assertEquals("strict", m.files.get(0).sync);
        assertNull(m.files.get(0).platform, "全平台文件 platform 须解析为 null");
        assertEquals("windows", m.files.get(1).platform);
        assertEquals("once", m.files.get(1).sync);
        assertEquals(2, m.managedDirs.size());
        // agent.core 段保留（楔子消费，FR-258）。
        assertEquals(5L, m.agentCoreVersion, "agent.core.version 须解析");
    }

    // ── FR-255：cleanExclude 跨端对照 ──────────────────────────────────────

    /**
     * 含 cleanExclude + managedDirs["*"] 的 manifest（Go 侧 dev 私钥已去，FR-256 起无 sig）。
     * 由 Go TestGenCleanExcludeGolden 生成，双线同步——勿手改。
     */
    private static final String GOLDEN_CLEAN_EXCLUDE = "{\n"
            + "  \"agent\": {\n"
            + "    \"wedge\": {\n"
            + "      \"version\": 3\n"
            + "    }\n"
            + "  },\n"
            + "  \"channel\": \"skyblock-s1\",\n"
            + "  \"cleanExclude\": [\n"
            + "    \"mods/keep\",\n"
            + "    \"custom\"\n"
            + "  ],\n"
            + "  \"files\": [\n"
            + "    {\n"
            + "      \"artifact\": {\n"
            + "        \"codec\": \"zstd\",\n"
            + "        \"sha256\": \"ef56\",\n"
            + "        \"size\": 45678\n"
            + "      },\n"
            + "      \"md5\": \"cd34\",\n"
            + "      \"path\": \"mods/foo.jar\",\n"
            + "      \"platform\": null,\n"
            + "      \"sha256\": \"ab12\",\n"
            + "      \"size\": 123456,\n"
            + "      \"sync\": \"strict\"\n"
            + "    }\n"
            + "  ],\n"
            + "  \"issuedAt\": \"2026-06-23T10:00:00Z\",\n"
            + "  \"managedDirs\": [\n"
            + "    \"*\"\n"
            + "  ],\n"
            + "  \"schemaVersion\": 1,\n"
            + "  \"version\": 42\n"
            + "}";

    @Test
    void parsesCleanExcludeManifest() {
        Manifest m = Manifest.parse(GOLDEN_CLEAN_EXCLUDE);
        assertEquals(1, m.schemaVersion);
        assertEquals(1, m.managedDirs.size());
        assertEquals("*", m.managedDirs.get(0), "managedDirs 含 '*' 哨兵");
        assertEquals(2, m.cleanExclude.size(), "cleanExclude 须解析为两项");
        assertEquals("mods/keep", m.cleanExclude.get(0));
        assertEquals("custom", m.cleanExclude.get(1));
    }

    @Test
    void oldManifestWithoutCleanExcludeStillParses() {
        // 老 manifest（无 cleanExclude）解析后 cleanExclude 为空列表（向后兼容）。
        Manifest m = Manifest.parse(GOLDEN);
        assertTrue(m.cleanExclude.isEmpty(), "无 cleanExclude 的老 manifest 须解析为空列表");
    }
}
