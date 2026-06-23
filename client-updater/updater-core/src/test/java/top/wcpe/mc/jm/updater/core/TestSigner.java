package top.wcpe.mc.jm.updater.core;

import java.nio.charset.StandardCharsets;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.PrivateKey;
import java.security.PublicKey;
import java.security.Signature;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * 测试辅助：生成 Ed25519 密钥对，并按契约 §3（去 sig → canonical JSON → Ed25519 签名）签 manifest。
 * 模拟 FR-087 服务端签名流程，使 updater-core 的 {@link Signatures} 可用注入的测试公钥验签。
 */
final class TestSigner {

    final KeyPair keyPair;
    final String keyId;

    TestSigner(String keyId) throws Exception {
        this.keyId = keyId;
        KeyPairGenerator kpg = KeyPairGenerator.getInstance("Ed25519");
        this.keyPair = kpg.generateKeyPair();
    }

    /** 公钥 X.509 DER 字节（注入 {@link Signatures#withTrustStore} 用）。 */
    byte[] publicKeyDer() {
        PublicKey pub = keyPair.getPublic();
        return pub.getEncoded();
    }

    /** {@code keyId → 公钥 DER} 信任根。 */
    Map<String, byte[]> trustStore() {
        Map<String, byte[]> m = new LinkedHashMap<>();
        m.put(keyId, publicKeyDer());
        return m;
    }

    /**
     * 对一个 manifest 对象树（不含 sig）签名，返回完整带 sig 的 JSON 文本。
     */
    String sign(Map<String, Object> manifestWithoutSig) throws Exception {
        byte[] signingBytes = Json.canonical(manifestWithoutSig).getBytes(StandardCharsets.UTF_8);
        Signature sig = Signature.getInstance("Ed25519");
        sig.initSign(keyPair.getPrivate());
        sig.update(signingBytes);
        byte[] signature = sig.sign();

        Map<String, Object> sigObj = new LinkedHashMap<>();
        sigObj.put("alg", "Ed25519");
        sigObj.put("keyId", keyId);
        sigObj.put("value", Base64.getEncoder().encodeToString(signature));

        Map<String, Object> full = new LinkedHashMap<>(manifestWithoutSig);
        full.put("sig", sigObj);
        return Json.canonical(full);
    }

    PrivateKey privateKey() {
        return keyPair.getPrivate();
    }
}
