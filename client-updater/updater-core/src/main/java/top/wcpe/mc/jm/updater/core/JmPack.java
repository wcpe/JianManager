package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Map;

/**
 * 自有 {@code .jmpack} 分发容器解包（FR-097，见 ADR-021/022）。格式见服务端 {@code service/jmpack.go}：
 *
 * <pre>
 * magic "JMPACK"(6) | formatVersion(1) | flags(1) | metaLen(uint32 BE) | meta JSON | payload | keyIdLen(1)+keyId+sig(64)
 * </pre>
 *
 * <p>解包：验 magic/版本 → <b>Ed25519 验签</b>（覆盖 magic→payload 末原始字节，内置公钥）→ 逐文件按 codec 解压 →
 * <b>sha256 校验解压后内容</b>。任一步失败抛 {@link JmPackException}（调用方 fail-static）。
 * 首期不解密（flags 加密位预留）。
 */
final class JmPack {

    private static final byte[] MAGIC = "JMPACK".getBytes(StandardCharsets.US_ASCII);
    private static final int FORMAT_VERSION = 1;
    private static final int SIG_LEN = 64;

    private JmPack() {
    }

    /** 解包结果条目：路径 + 解压并校验后的原始内容。 */
    static final class Entry {
        final String path;
        final byte[] content;

        Entry(String path, byte[] content) {
            this.path = path;
            this.content = content;
        }
    }

    /** 解包失败（格式非法 / 验签失败 / 解压或 sha256 校验失败）。 */
    static final class JmPackException extends Exception {
        JmPackException(String message) {
            super(message);
        }
    }

    /**
     * 解包 + 验签 + 逐文件解压 + sha256 校验。
     *
     * @param data .jmpack 字节
     * @param sigs 信任根（内置公钥）
     * @return 解压并校验后的文件条目
     * @throws JmPackException 任一步失败
     */
    @SuppressWarnings("unchecked")
    static List<Entry> unpack(byte[] data, Signatures sigs) throws JmPackException {
        if (data == null || data.length < 12 || !regionEquals(data, 0, MAGIC)) {
            throw new JmPackException(".jmpack 魔数非法");
        }
        if ((data[6] & 0xff) != FORMAT_VERSION) {
            throw new JmPackException(".jmpack 不支持的格式版本: " + (data[6] & 0xff));
        }
        int metaLen = beUint32(data, 8);
        int metaEnd = 12 + metaLen;
        if (metaLen < 0 || metaEnd > data.length) {
            throw new JmPackException(".jmpack meta 长度越界");
        }

        Object parsed;
        try {
            parsed = Json.parse(new String(data, 12, metaLen, StandardCharsets.UTF_8));
        } catch (RuntimeException e) {
            throw new JmPackException(".jmpack meta 解析失败: " + e.getMessage());
        }
        if (!(parsed instanceof Map)) {
            throw new JmPackException(".jmpack meta 非对象");
        }
        Object filesObj = ((Map<String, Object>) parsed).get("files");
        if (!(filesObj instanceof List)) {
            throw new JmPackException(".jmpack meta 缺 files");
        }
        List<Object> files = (List<Object>) filesObj;

        long payloadLen = 0;
        for (Object o : files) {
            payloadLen += asLong(((Map<String, Object>) o).get("clen"));
        }
        int payloadStart = metaEnd;
        long payloadEndL = (long) payloadStart + payloadLen;
        if (payloadEndL > data.length) {
            throw new JmPackException(".jmpack payload 越界");
        }
        int payloadEnd = (int) payloadEndL;

        // 尾部签名段：keyIdLen(1) + keyId + sig(64)。
        if (payloadEnd >= data.length) {
            throw new JmPackException(".jmpack 缺签名段");
        }
        int keyIdLen = data[payloadEnd] & 0xff;
        int sigStart = payloadEnd + 1 + keyIdLen;
        if (sigStart + SIG_LEN > data.length) {
            throw new JmPackException(".jmpack 签名段越界");
        }
        String keyId = new String(data, payloadEnd + 1, keyIdLen, StandardCharsets.US_ASCII);
        byte[] sig = Arrays.copyOfRange(data, sigStart, sigStart + SIG_LEN);
        byte[] signed = Arrays.copyOfRange(data, 0, payloadEnd);
        if (!sigs.verifyRaw(keyId, signed, sig)) {
            throw new JmPackException(".jmpack 验签失败 keyId=" + keyId);
        }

        List<Entry> out = new ArrayList<>(files.size());
        for (Object o : files) {
            Map<String, Object> f = (Map<String, Object>) o;
            String path = String.valueOf(f.get("path"));
            String codec = f.get("codec") == null ? "none" : String.valueOf(f.get("codec"));
            String wantSha = String.valueOf(f.get("sha256"));
            int offset = (int) asLong(f.get("offset"));
            int clen = (int) asLong(f.get("clen"));
            int start = payloadStart + offset;
            int end = start + clen;
            if (offset < 0 || clen < 0 || end > payloadEnd) {
                throw new JmPackException(".jmpack 文件段越界: " + path);
            }
            byte[] blob = Arrays.copyOfRange(data, start, end);
            byte[] content;
            try {
                content = Codec.decode(blob, codec);
            } catch (IOException e) {
                throw new JmPackException(".jmpack 解压失败 " + path + ": " + e.getMessage());
            }
            if (!wantSha.equalsIgnoreCase(Hashes.sha256(content))) {
                throw new JmPackException(".jmpack 内容 sha256 不符: " + path);
            }
            out.add(new Entry(path, content));
        }
        return out;
    }

    private static boolean regionEquals(byte[] data, int off, byte[] expect) {
        if (off + expect.length > data.length) {
            return false;
        }
        for (int i = 0; i < expect.length; i++) {
            if (data[off + i] != expect[i]) {
                return false;
            }
        }
        return true;
    }

    private static int beUint32(byte[] d, int off) {
        return ((d[off] & 0xff) << 24) | ((d[off + 1] & 0xff) << 16)
                | ((d[off + 2] & 0xff) << 8) | (d[off + 3] & 0xff);
    }

    private static long asLong(Object o) {
        if (o instanceof Number) {
            return ((Number) o).longValue();
        }
        if (o == null) {
            return 0;
        }
        return Long.parseLong(String.valueOf(o));
    }
}
