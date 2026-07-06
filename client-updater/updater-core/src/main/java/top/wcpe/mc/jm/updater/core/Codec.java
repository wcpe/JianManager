package top.wcpe.mc.jm.updater.core;

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

    /**
     * 流式解码制品（FR-257）：返回解码后的 InputStream，调用方边读边写盘（64KB 缓冲），
     * 不再全量读进 byte[]。{@code none} 原样返回源流；{@code zstd} 包一层 {@link ZstdInputStream}
     * （zstd-jni 原生支持流式解压）。调用方负责关闭返回的流（会连带关闭源流）。
     */
    static InputStream decodeStream(InputStream artifact, String codec) throws IOException {
        if (codec == null || "none".equalsIgnoreCase(codec)) {
            return artifact;
        }
        if ("zstd".equalsIgnoreCase(codec)) {
            return new ZstdInputStream(artifact);
        }
        throw new IOException("不支持的 codec: " + codec);
    }

    /** 按 zstd 字典解 patch-from 制品，字典即本地旧文件内容。 */
    static InputStream decodePatchStream(InputStream patch, String codec, byte[] oldContent) throws IOException {
        if (!"zstd-patch".equalsIgnoreCase(codec)) {
            throw new IOException("不支持的 patch codec: " + codec);
        }
        return new ZstdInputStream(patch).setDict(oldContent);
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
