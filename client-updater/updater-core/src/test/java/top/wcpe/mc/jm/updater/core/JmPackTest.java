package top.wcpe.mc.jm.updater.core;

import com.github.luben.zstd.Zstd;
import org.junit.jupiter.api.Test;

import java.io.ByteArrayOutputStream;
import java.nio.charset.StandardCharsets;
import java.security.PrivateKey;
import java.security.Signature;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

/**
 * .jmpack 解包（FR-097）：①跨语言兼容——解 Go 服务端产出的 golden 向量（dev 密钥 k1，见 service/jmpack_test.go
 * 的 JMPACK_GOLDEN_BASE64）②Java 自建 zstd/none 往返 ③篡改/魔数拒。
 */
class JmPackTest {

    // Go service.PackJmPack 产出（内容 "jmpack-golden-content"、path mods/hello.jar、codec none、dev 密钥 k1）。
    private static final String GOLDEN =
            "Sk1QQUNLAQAAAACfeyJmaWxlcyI6W3sicGF0aCI6Im1vZHMvaGVsbG8uamFyIiwic2hhMjU2IjoiZWI4ZDZhNjI4NWYzYmI4NDIxY2I4YWYxNzBhMGM3NDg1NWE0MjMwNzk3Zjg4NjA0YzM5NGQwOTg3MGM1YWMzNSIsInNpemUiOjIxLCJjb2RlYyI6Im5vbmUiLCJvZmZzZXQiOjAsImNsZW4iOjIxfV19am1wYWNrLWdvbGRlbi1jb250ZW50AmsxErmx/2VGCJreiy8ysTLi63KltQPqkpjRk58TWcf7ytNTaKshO8+LiplSBdiS4MF9PgPrm6YM0OdO5lmkfd4tCg==";

    @Test
    void unpacksGoGoldenVector() throws Exception {
        byte[] data = Base64.getDecoder().decode(GOLDEN);
        List<JmPack.Entry> out = JmPack.unpack(data, Signatures.production());
        assertEquals(1, out.size());
        assertEquals("mods/hello.jar", out.get(0).path);
        assertEquals("jmpack-golden-content", new String(out.get(0).content, StandardCharsets.UTF_8),
                "应能解 Go 服务端产出的 .jmpack（跨语言字节兼容）");
    }

    @Test
    void roundTripZstdAndNone() throws Exception {
        TestSigner signer = new TestSigner("k1");
        byte[] data = buildPack(Arrays.asList(
                new String[] {"mods/a.jar", "hello-a-content", "zstd"},
                new String[] {"config/b.txt", "plain-b-content", "none"}),
                signer.privateKey(), "k1");

        List<JmPack.Entry> out = JmPack.unpack(data, Signatures.withTrustStore(signer.trustStore()));
        assertEquals(2, out.size());
        assertEquals("hello-a-content", new String(out.get(0).content, StandardCharsets.UTF_8), "zstd 解压内容应一致");
        assertEquals("plain-b-content", new String(out.get(1).content, StandardCharsets.UTF_8));
    }

    @Test
    void tamperedSignatureRejected() throws Exception {
        TestSigner signer = new TestSigner("k1");
        byte[] data = buildPack(Arrays.<String[]>asList(new String[] {"mods/a.jar", "x", "none"}),
                signer.privateKey(), "k1");
        data[data.length - 1] ^= 0xff; // 翻转签名末字节。
        assertThrows(JmPack.JmPackException.class,
                () -> JmPack.unpack(data, Signatures.withTrustStore(signer.trustStore())));
    }

    @Test
    void unknownKeyRejected() throws Exception {
        TestSigner signer = new TestSigner("k1");
        byte[] data = buildPack(Arrays.<String[]>asList(new String[] {"mods/a.jar", "x", "none"}),
                signer.privateKey(), "k1");
        // 用生产信任根（k1=dev 公钥，与本测试随机密钥不符）→ 验签失败。
        assertThrows(JmPack.JmPackException.class, () -> JmPack.unpack(data, Signatures.production()));
    }

    @Test
    void badMagicRejected() {
        byte[] data = new byte[64];
        data[0] = 'X';
        assertThrows(JmPack.JmPackException.class, () -> JmPack.unpack(data, Signatures.production()));
    }

    /** 测试用 .jmpack 打包器，镜像 service/jmpack.go 格式：magic+version+flags+metaLen(BE)+meta+payload+keyIdLen+keyId+sig。 */
    private byte[] buildPack(List<String[]> files, PrivateKey priv, String keyId) throws Exception {
        List<Map<String, Object>> metaFiles = new ArrayList<>();
        ByteArrayOutputStream payload = new ByteArrayOutputStream();
        long offset = 0;
        for (String[] f : files) {
            byte[] raw = f[1].getBytes(StandardCharsets.UTF_8);
            byte[] blob = "zstd".equals(f[2]) ? Zstd.compress(raw) : raw;
            Map<String, Object> mf = new LinkedHashMap<>();
            mf.put("path", f[0]);
            mf.put("sha256", Hashes.sha256(raw));
            mf.put("size", (long) raw.length);
            mf.put("codec", f[2]);
            mf.put("offset", offset);
            mf.put("clen", (long) blob.length);
            metaFiles.add(mf);
            payload.write(blob);
            offset += blob.length;
        }
        Map<String, Object> meta = new LinkedHashMap<>();
        meta.put("files", metaFiles);
        byte[] metaJson = Json.canonical(meta).getBytes(StandardCharsets.UTF_8);

        ByteArrayOutputStream head = new ByteArrayOutputStream();
        head.write("JMPACK".getBytes(StandardCharsets.US_ASCII));
        head.write(1); // formatVersion
        head.write(0); // flags
        int n = metaJson.length;
        head.write(new byte[] {(byte) (n >>> 24), (byte) (n >>> 16), (byte) (n >>> 8), (byte) n});
        head.write(metaJson);
        head.write(payload.toByteArray());
        byte[] body = head.toByteArray();

        Signature s = Signature.getInstance("Ed25519");
        s.initSign(priv);
        s.update(body);
        byte[] sig = s.sign();

        ByteArrayOutputStream out = new ByteArrayOutputStream();
        out.write(body);
        out.write(keyId.length());
        out.write(keyId.getBytes(StandardCharsets.US_ASCII));
        out.write(sig);
        return out.toByteArray();
    }
}
