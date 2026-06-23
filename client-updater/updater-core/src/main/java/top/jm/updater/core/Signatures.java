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
     * 生产内置信任根：keyId → 公钥（X.509 SubjectPublicKeyInfo DER, base64）。
     *
     * <p>{@code k1} 为 JM 服务端 FR-087 签名公钥（与服务端 {@code ManifestSigner} 私钥成对）。
     * <strong>当前固化的是开发用密钥</strong>——生产部署须随基础整包替换为运营方独立公钥
     * （服务端经 {@code JIANMANAGER_CLIENT_SIGN_PRIVKEY} 注入对应私钥，ADR-022 决策 8）。
     * 支持主 + 备多公钥（密钥轮换：新增 k2… 经一次基础包更新淘汰旧 keyId）。
     */
    static Signatures production() {
        Map<String, byte[]> store = new LinkedHashMap<>();
        store.put("k1", Base64.getDecoder().decode(KEY_K1));
        return new Signatures(Collections.unmodifiableMap(store));
    }

    /**
     * 开发用 Ed25519 公钥（X.509 SubjectPublicKeyInfo DER, base64），keyId=k1。
     * 与服务端 {@code service.DevSignPublicKeySPKIBase64} 同值（FR-087）。生产须替换。
     */
    private static final String KEY_K1 =
            "MCowBQYDK2VwAyEAsO7B/k+2++wQtN/L0jpCXCjsGnYV5Sx2eyCk0pDzV0Y=";

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
