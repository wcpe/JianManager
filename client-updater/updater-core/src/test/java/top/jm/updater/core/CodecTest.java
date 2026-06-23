package top.jm.updater.core;

import com.github.luben.zstd.Zstd;
import org.junit.jupiter.api.Test;

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
}
