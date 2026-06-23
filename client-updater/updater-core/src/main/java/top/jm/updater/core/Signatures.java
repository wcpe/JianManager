package top.jm.updater.core;

import java.security.GeneralSecurityException;
import java.security.KeyFactory;
import java.security.PublicKey;
import java.security.Signature;
import java.security.spec.X509EncodedKeySpec;
import java.util.Base64;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * manifest Ed25519 验签（契约 §3，ADR-022）。信任根 = 内置公钥；签名缺失/不符一律拒绝。
 *
 * <p>内置 {@code keyId → 公钥} 映射支持密钥轮换（主 k1 + 备 k2…，ADR-022 决策 8）。
 * 测试可经 {@link #withTrustStore(Map)} 注入测试公钥而不污染生产内置集。
 *
 * <p>用 JDK15+ 内置 EdDSA（{@code KeyFactory.getInstance("Ed25519")}），零三方依赖。
 */
final class Signatures {

    /** keyId → X.509(SubjectPublicKeyInfo) DER 公钥字节，base64 编码。 */
    private final Map<String, byte[]> trustStore;

    private Signatures(Map<String, byte[]> trustStore) {
        this.trustStore = trustStore;
    }

    /**
     * 生产内置信任根。占位公钥在 FR-087 服务端签名密钥生成后回填（见 {@code KEY_K1}）。
     * 当前为空——任何 manifest 验签都会因「未知 keyId」被拒（fail-static），符合「端点未实现前不放行未签内容」。
     */
    static Signatures production() {
        Map<String, byte[]> store = new LinkedHashMap<>();
        // TODO(FR-087): 回填服务端签名公钥（X.509 DER, base64）。
        //   store.put("k1", Base64.getDecoder().decode(KEY_K1));
        //   store.put("k2", Base64.getDecoder().decode(KEY_K2));
        return new Signatures(Collections.unmodifiableMap(store));
    }

    /** 用指定信任根构造（测试注入用）。键=keyId，值=X.509 公钥 DER 字节。 */
    static Signatures withTrustStore(Map<String, byte[]> store) {
        return new Signatures(new LinkedHashMap<>(store));
    }

    /** 是否持有该 keyId 的公钥。 */
    boolean hasKey(String keyId) {
        return trustStore.containsKey(keyId);
    }

    /**
     * 验证 manifest 签名。
     *
     * @return true=签名有效；false=算法不支持/未知 keyId/签名不符（调用方据此 fail-static）。
     */
    boolean verify(Manifest manifest) {
        if (manifest.sigValue == null || manifest.sigKeyId == null) {
            return false;
        }
        if (!"Ed25519".equals(manifest.sigAlg)) {
            return false;
        }
        byte[] pubDer = trustStore.get(manifest.sigKeyId);
        if (pubDer == null) {
            return false;
        }
        try {
            PublicKey pub = KeyFactory.getInstance("Ed25519")
                    .generatePublic(new X509EncodedKeySpec(pubDer));
            Signature sig = Signature.getInstance("Ed25519");
            sig.initVerify(pub);
            sig.update(manifest.signingBytes());
            byte[] sigBytes = Base64.getDecoder().decode(manifest.sigValue);
            return sig.verify(sigBytes);
        } catch (GeneralSecurityException | IllegalArgumentException e) {
            return false;
        }
    }
}
