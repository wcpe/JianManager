package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * 服务端 manifest 兼容性固化测试（FR-087 契约硬验证）。
 *
 * <p>{@link #GOLDEN} 是 JM 服务端 {@code ManifestSigner}（开发签名密钥 k1）对契约 §2 样例
 * **真实输出**的签名 manifest JSON（由 Go 侧 service 测试 emit、原样粘入）。本测试证明：
 * <ol>
 *   <li>服务端输出的字段名/嵌套/类型可被 {@link Manifest#parse} 解析（两线接口对齐）；</li>
 *   <li>服务端 Ed25519 签名可被生产内置公钥 {@link Signatures#production()}（已回填 k1）验证——
 *       即服务端签名与客户端验签对同一份 canonical JSON 字节计算，逐位一致。</li>
 * </ol>
 *
 * <p>更新规则：服务端 canonical/字段若变更，须重跑 Go 侧 emit 并替换 {@link #GOLDEN}，双线同步。
 */
class ServerManifestCompatTest {

    /** JM 服务端真实输出（dev 私钥 k1 签名，contract §2 样例）。勿手改，须由服务端 emit 同步。 */
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
            + "  \"sig\": {\n"
            + "    \"alg\": \"Ed25519\",\n"
            + "    \"keyId\": \"k1\",\n"
            + "    \"value\": \"QzQE5n5erhS7r3xPHceNNXvT5WoUsVOyYeV7ytNX26R7ZZ0pha/LaUzziI/iwcqksH8uKX3cijLvLg8iJBYCBA==\"\n"
            + "  },\n"
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
        assertEquals("k1", m.sigKeyId);
        assertEquals("Ed25519", m.sigAlg);
    }

    @Test
    void verifiesWithProductionPublicKey() {
        // 服务端真实签名，须被回填了 k1 公钥的生产信任根验证通过——两线签名/验签逐位对齐的硬证明。
        Manifest m = Manifest.parse(GOLDEN);
        assertTrue(Signatures.production().verify(m),
                "服务端 manifest 签名必须可被生产内置公钥验证（FR-087 契约一致性）");
    }

    // ── FR-255：cleanExclude 跨端对照 ──────────────────────────────────────

    /**
     * 含 cleanExclude + managedDirs["*"] 的签名 manifest（Go 侧 dev 私钥 k1 签名）。
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
            + "  \"sig\": {\n"
            + "    \"alg\": \"Ed25519\",\n"
            + "    \"keyId\": \"k1\",\n"
            + "    \"value\": \"yY9lHKS5e0He8GhgA4flBpcs0h7hC+ejnkDJS9iKFv0IowQ4UhtVtbfmEpIdGWK3XuBDaXe1DUok9bDBnSvMBw==\"\n"
            + "  },\n"
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
        assertEquals("k1", m.sigKeyId);
    }

    @Test
    void verifiesCleanExcludeManifestWithProductionPublicKey() {
        // 含 cleanExclude 的 manifest 签名须被生产内置公钥验证——
        // 证明 Go manifestToTree 与 Java signingBytes 对同一份 canonical JSON 逐位一致。
        Manifest m = Manifest.parse(GOLDEN_CLEAN_EXCLUDE);
        assertTrue(Signatures.production().verify(m),
                "含 cleanExclude 的 manifest 签名必须可被生产内置公钥验证（FR-255 跨端 canonical 对齐）");
    }

    @Test
    void oldManifestWithoutCleanExcludeStillParses() {
        // 老 manifest（无 cleanExclude）解析后 cleanExclude 为空列表（向后兼容）。
        Manifest m = Manifest.parse(GOLDEN);
        assertTrue(m.cleanExclude.isEmpty(), "无 cleanExclude 的老 manifest 须解析为空列表");
    }
}
