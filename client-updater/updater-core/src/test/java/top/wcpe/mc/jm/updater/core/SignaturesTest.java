package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class SignaturesTest {

    private Map<String, Object> sampleManifest() {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("schemaVersion", 1L);
        m.put("channel", "skyblock-s1");
        m.put("version", 42L);
        m.put("managedDirs", new ArrayList<>(Collections.singletonList("mods")));
        m.put("files", new ArrayList<>());
        return m;
    }

    @Test
    void acceptsValidSignature() throws Exception {
        TestSigner signer = new TestSigner("k1");
        String signed = signer.sign(sampleManifest());
        Manifest manifest = Manifest.parse(signed);

        Signatures sigs = Signatures.withTrustStore(signer.trustStore());
        assertTrue(sigs.verify(manifest), "合法签名应通过");
    }

    @Test
    void rejectsTamperedVersion() throws Exception {
        TestSigner signer = new TestSigner("k1");
        String signed = signer.sign(sampleManifest());
        // 篡改 version（攻击者改版本号但保留原签名）。
        String tampered = signed.replace("\"version\":42", "\"version\":99");
        Manifest manifest = Manifest.parse(tampered);

        Signatures sigs = Signatures.withTrustStore(signer.trustStore());
        assertFalse(sigs.verify(manifest), "篡改 version 后签名必须失效（契约 §3 覆盖 version）");
    }

    @Test
    void rejectsTamperedFiles() throws Exception {
        TestSigner signer = new TestSigner("k1");
        Map<String, Object> m = sampleManifest();
        Map<String, Object> file = new LinkedHashMap<>();
        file.put("path", "mods/foo.jar");
        file.put("sha256", "aa");
        ((java.util.List<Object>) m.get("files")).add(file);
        String signed = signer.sign(m);
        String tampered = signed.replace("mods/foo.jar", "mods/evil.jar");
        Manifest manifest = Manifest.parse(tampered);

        Signatures sigs = Signatures.withTrustStore(signer.trustStore());
        assertFalse(sigs.verify(manifest), "篡改文件路径后签名必须失效");
    }

    @Test
    void rejectsUnknownKeyId() throws Exception {
        TestSigner signer = new TestSigner("k1");
        String signed = signer.sign(sampleManifest());
        Manifest manifest = Manifest.parse(signed);

        // 信任根里没有 k1（用别的 keyId）。
        TestSigner other = new TestSigner("k2");
        Signatures sigs = Signatures.withTrustStore(other.trustStore());
        assertFalse(sigs.verify(manifest), "未知 keyId 必须拒绝");
    }

    @Test
    void rejectsWrongPublicKey() throws Exception {
        TestSigner signer = new TestSigner("k1");
        String signed = signer.sign(sampleManifest());
        Manifest manifest = Manifest.parse(signed);

        // 同 keyId 但不同密钥对（模拟伪造）。
        TestSigner imposter = new TestSigner("k1");
        Signatures sigs = Signatures.withTrustStore(imposter.trustStore());
        assertFalse(sigs.verify(manifest), "公钥不匹配必须拒绝");
    }

    @Test
    void rejectsMissingSignature() {
        Manifest manifest = Manifest.parse("{\"version\":1,\"files\":[]}");
        Signatures sigs = Signatures.withTrustStore(new LinkedHashMap<>());
        assertFalse(sigs.verify(manifest), "无签名必须拒绝");
    }

    @Test
    void productionTrustStoreRejectsForgedKey() throws Exception {
        // FR-087 已回填生产 k1 公钥；用随机生成的 k1 私钥（伪造方）签名 → 公钥不匹配必被拒。
        // 证明生产信任根只认与内置公钥成对的私钥，拒绝任何冒充 keyId 的伪造签名。
        TestSigner signer = new TestSigner("k1");
        Manifest manifest = Manifest.parse(signer.sign(sampleManifest()));
        assertFalse(Signatures.production().verify(manifest),
                "伪造 k1 私钥（与内置公钥不成对）的签名必须被生产验签拒绝");
    }

    @Test
    void productionTrustStoreHasK1() {
        // FR-087 公钥已回填：production 信任根应持有 k1（与服务端 ManifestSigner 私钥成对）。
        assertTrue(Signatures.production().hasKey("k1"),
                "生产信任根应内置 k1 公钥（FR-087 回填）");
    }
}
