package top.jm.updater.core;

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
    void productionTrustStoreRejectsUntilKeysFilled() throws Exception {
        // 生产内置信任根当前为空（FR-087 公钥未回填）→ 任何 manifest 都因未知 keyId 被拒，符合预期。
        TestSigner signer = new TestSigner("k1");
        Manifest manifest = Manifest.parse(signer.sign(sampleManifest()));
        assertFalse(Signatures.production().verify(manifest),
                "公钥未回填时生产验签应拒绝（fail-static），不放行未签内容");
    }
}
