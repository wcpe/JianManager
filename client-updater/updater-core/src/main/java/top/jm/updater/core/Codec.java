package top.jm.updater.core;

import com.github.luben.zstd.ZstdInputStream;

import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;

/**
 * 制品解码（契约 §2 artifact.codec）：{@code zstd} 流式解压 / {@code none} 原样。
 */
final class Codec {

    private Codec() {
    }

    /** 按 codec 解码制品字节为原始内容。 */
    static byte[] decode(byte[] artifact, String codec) throws IOException {
        if (codec == null || "none".equalsIgnoreCase(codec)) {
            return artifact;
        }
        if ("zstd".equalsIgnoreCase(codec)) {
            return decompressZstd(artifact);
        }
        throw new IOException("不支持的 codec: " + codec);
    }

    private static byte[] decompressZstd(byte[] compressed) throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream(Math.max(64, compressed.length * 3));
        try (InputStream in = new ZstdInputStream(new ByteArrayInputStream(compressed))) {
            byte[] buf = new byte[64 * 1024];
            int n;
            while ((n = in.read(buf)) != -1) {
                out.write(buf, 0, n);
            }
        }
        return out.toByteArray();
    }
}
