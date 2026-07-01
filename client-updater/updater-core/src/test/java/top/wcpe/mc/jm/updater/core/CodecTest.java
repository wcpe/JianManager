package top.wcpe.mc.jm.updater.core;

import com.github.luben.zstd.Zstd;
import org.junit.jupiter.api.Test;

import java.io.ByteArrayInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.util.Arrays;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

class CodecTest {

    @Test
    void decodesZstd() throws Exception {
        byte[] original = "hello jm updater zstd payload".getBytes(StandardCharsets.UTF_8);
        byte[] compressed = Zstd.compress(original);
        assertArrayEquals(original, Codec.decode(compressed, "zstd"));
    }

    @Test
    void decodesNonePassthrough() throws Exception {
        byte[] original = {1, 2, 3, 4, 5};
        assertArrayEquals(original, Codec.decode(original, "none"));
        assertArrayEquals(original, Codec.decode(original, null));
    }

    @Test
    void decodesLargeZstdPayload() throws Exception {
        byte[] original = new byte[512 * 1024];
        for (int i = 0; i < original.length; i++) {
            original[i] = (byte) (i % 251);
        }
        byte[] compressed = Zstd.compress(original);
        assertArrayEquals(original, Codec.decode(compressed, "zstd"));
    }

    @Test
    void rejectsUnknownCodec() {
        assertThrows(java.io.IOException.class,
                () -> Codec.decode(new byte[]{1}, "brotli"));
    }

    // ── FR-257：流式解码（decodeStream）──────────────────────────────────

    @Test
    void decodesZstdStream() throws Exception {
        byte[] original = "hello jm updater zstd stream payload".getBytes(StandardCharsets.UTF_8);
        byte[] compressed = Zstd.compress(original);
        try (InputStream in = Codec.decodeStream(new ByteArrayInputStream(compressed), "zstd")) {
            assertArrayEquals(original, readAll(in));
        }
    }

    @Test
    void decodesNoneStreamPassthrough() throws Exception {
        byte[] original = {1, 2, 3, 4, 5};
        try (InputStream in = Codec.decodeStream(new ByteArrayInputStream(original), "none")) {
            assertArrayEquals(original, readAll(in));
        }
        try (InputStream in = Codec.decodeStream(new ByteArrayInputStream(original), null)) {
            assertArrayEquals(original, readAll(in));
        }
    }

    @Test
    void decodesLargeZstdStream() throws Exception {
        byte[] original = new byte[512 * 1024];
        for (int i = 0; i < original.length; i++) {
            original[i] = (byte) (i % 251);
        }
        byte[] compressed = Zstd.compress(original);
        try (InputStream in = Codec.decodeStream(new ByteArrayInputStream(compressed), "zstd")) {
            assertArrayEquals(original, readAll(in));
        }
    }

    @Test
    void rejectsUnknownCodecStream() {
        assertThrows(IOException.class,
                () -> Codec.decodeStream(new ByteArrayInputStream(new byte[]{1}), "brotli"));
    }

    /** Java 8 无 InputStream.readAllBytes，手写读尽。 */
    private static byte[] readAll(InputStream in) throws IOException {
        java.io.ByteArrayOutputStream bos = new java.io.ByteArrayOutputStream();
        byte[] buf = new byte[8192];
        int n;
        while ((n = in.read(buf)) != -1) {
            bos.write(buf, 0, n);
        }
        return bos.toByteArray();
    }
}
