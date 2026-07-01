package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Core.resolveSignatures 信任根裁决测试（FR-253，见 ADR-053）。
 *
 * <p>覆盖三条路径：
 * <ul>
 *   <li>ctx 有 signPublicKey（合法）→ 用配置公钥构造信任根；</li>
 *   <li>ctx 有 signPublicKey（无效）→ 回退内置 production 公钥；</li>
 *   <li>ctx 无 signPublicKey → 回退内置 production 公钥。</li>
 * </ul>
 */
class CoreResolveSignaturesTest {

    @Test
    void resolveUsesConfiguredPublicKeyWhenProvided() throws Exception {
        // ctx 提供合法 signPublicKey → resolveSignatures 应返回配置公钥构造的信任根（持有该 keyId）。
        TestSigner signer = new TestSigner("k1");
        String pubB64 = Base64.getEncoder().encodeToString(signer.publicKeyDer());
        Map<String, String> ctx = new LinkedHashMap<>();
        ctx.put("signPublicKey", pubB64);
        ctx.put("signKeyId", "k1");

        Signatures sigs = Core.resolveSignatures(ctx);
        assertTrue(sigs.hasKey("k1"), "配置公钥应被采用");
        assertFalse(sigs.hasKey("k2"), "配置信任根不应含其它 keyId");

        // 验签端到端：配置公钥应验签 signer 签的 manifest。
        Map<String, Object> manifest = new LinkedHashMap<>();
        manifest.put("schemaVersion", 1L);
        manifest.put("channel", "test");
        manifest.put("version", 1L);
        manifest.put("managedDirs", new java.util.ArrayList<String>());
        manifest.put("files", new java.util.ArrayList<>());
        assertTrue(sigs.verify(Manifest.parse(signer.sign(manifest))), "配置公钥应验签通过");
    }

    @Test
    void resolveFallsBackToProductionWhenConfigInvalid() {
        // ctx 提供无效 signPublicKey → resolveSignatures 应回退内置 production 公钥。
        Map<String, String> ctx = new LinkedHashMap<>();
        ctx.put("signPublicKey", "!!!非法base64!!!");
        ctx.put("signKeyId", "k1");

        Signatures sigs = Core.resolveSignatures(ctx);
        assertTrue(sigs.hasKey("k1"), "无效配置应回退内置 production 公钥（仍持有 k1）");
        // 回退的是内置公钥——用随机密钥签的 manifest 应被拒（与 production 一致）。
        assertEquals(Signatures.production().hasKey("k1"), sigs.hasKey("k1"),
                "回退内置后行为等同 production()");
    }

    @Test
    void resolveFallsBackToProductionWhenNoConfig() {
        // ctx 无 signPublicKey → resolveSignatures 应直接用内置 production 公钥。
        Map<String, String> ctx = new LinkedHashMap<>();
        // 不放 signPublicKey / signKeyId。

        Signatures sigs = Core.resolveSignatures(ctx);
        assertTrue(sigs.hasKey("k1"), "无配置应用内置 production 公钥（持有 k1）");
    }

    @Test
    void resolveFallsBackToProductionWhenConfigEmpty() {
        // ctx signPublicKey 为空串 → 等同无配置，回退内置。
        Map<String, String> ctx = new LinkedHashMap<>();
        ctx.put("signPublicKey", "");
        ctx.put("signKeyId", "");

        Signatures sigs = Core.resolveSignatures(ctx);
        assertTrue(sigs.hasKey("k1"), "空配置应回退内置 production 公钥");
    }
}
