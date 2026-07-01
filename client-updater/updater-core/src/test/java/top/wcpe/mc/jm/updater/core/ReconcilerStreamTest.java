package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * FR-257 流式下载 + 断点续传测试：验证 Reconciler 走 {@link InputStream} 边读边写盘（64KB 缓冲）、
 * DigestInputStream 流式 sha256、zstd 流式解压、HTTP Range 断点续传，不再全量读进 byte[]。
 *
 * <p>用本地临时 gameDir + 内存 Transport 替身，不依赖真端点。
 */
class ReconcilerStreamTest {

    private byte[] bytes(String s) {
        return s.getBytes(StandardCharsets.UTF_8);
    }

    private Updater updater(Path gameDir, Transport transport) {
        return new Updater(gameDir, transport, false);
    }

    private void install(TestFixtures.MemoryTransport transport,
                         long version, List<String> managedDirs,
                         List<TestFixtures.FileSpec> specs) throws Exception {
        Map<String, Object> manifest = TestFixtures.buildManifest(
                "skyblock-s1", version, managedDirs, specs, transport);
        transport.manifestJson = Json.canonical(manifest);
    }

    /** 流式下载小文件（zstd）→ 内容正确 + sha256 通过 + 临时文件清理。 */
    @Test
    void streamDownloadZstdFileSha256Passes(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        byte[] content = bytes("stream zstd artifact payload");
        install(transport, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/a.jar", content)));

        assertEquals(Updater.OK, updater(gameDir, transport).run());

        assertArrayEquals(content, Files.readAllBytes(gameDir.resolve("mods/a.jar")));
        assertEquals(1, transport.artifactFetchCount, "流式拉取应调用一次");
        assertFalse(Files.exists(gameDir.resolve("mods/a.jar.jmtmp.dl")), "完成后下载临时应清理");
    }

    /** none codec 流式路径：.jmtmp.dl 即最终内容，校验后直接原子 rename。 */
    @Test
    void streamDownloadNoneCodecFile(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        byte[] content = bytes("plain none-codec payload");
        install(transport, 1, Collections.singletonList("mods"),
                Collections.singletonList(
                        new TestFixtures.FileSpec("mods/b.dat", content).codec("none")));

        assertEquals(Updater.OK, updater(gameDir, transport).run());

        assertArrayEquals(content, Files.readAllBytes(gameDir.resolve("mods/b.dat")));
        assertFalse(Files.exists(gameDir.resolve("mods/b.dat.jmtmp.dl")));
    }

    /**
     * 断点续传：首次下载到一半中断（模拟）→ .jmtmp.dl 残留 → 重试从断点继续 → 完整 + sha256 通过。
     */
    @Test
    void resumeFromPartialDownload(@TempDir Path gameDir) throws Exception {
        InterruptingTransport transport = new InterruptingTransport();
        // 随机内容（zstd 不可压缩，保证压缩制品 ~300KB 触发多次 64KB 读）。
        byte[] content = new byte[300 * 1024];
        new java.util.Random(42).nextBytes(content);
        install(transport, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/big.dat", content)));
        long fullArtifact = transport.artifacts.values().iterator().next().length;

        // 首次：读到 50KB 阈值后模拟中断。
        transport.failAfterBytes = 50 * 1024;
        assertEquals(Updater.FAIL_STATIC, updater(gameDir, transport).run(), "下载中断应 fail-static");

        Path dlTmp = gameDir.resolve("mods/big.dat.jmtmp.dl");
        assertTrue(Files.exists(dlTmp), "中断后下载临时文件应残留供续传");
        long partial = Files.size(dlTmp);
        assertTrue(partial > 0 && partial < fullArtifact,
                "应只下载部分制品: " + partial + "/" + fullArtifact);

        // 第二次：不中断，从断点续传。
        transport.failAfterBytes = -1;
        transport.artifactFetchCount = 0;
        transport.lastStreamOffset = 0;
        assertEquals(Updater.OK, updater(gameDir, transport).run());

        assertTrue(transport.lastStreamOffset > 0,
                "第二次应从断点（offset>0）续传，实际=" + transport.lastStreamOffset);
        assertArrayEquals(content, Files.readAllBytes(gameDir.resolve("mods/big.dat")),
                "续传完成后内容应完整且正确");
        assertFalse(Files.exists(dlTmp), "完成后下载临时应清理");
        assertEquals(1, transport.artifactFetchCount, "续传应再拉取一次");
    }

    /** 断点续传残留的 .jmtmp.dl 不被减量删除（保留供下次续传）。 */
    @Test
    void partialTmpNotDeletedByStaleRemoval(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        byte[] content = bytes("keep");
        install(transport, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/keep.jar", content)));
        // 预置一个无关文件的残留下载临时（模拟上次中断遗留）。
        Files.createDirectories(gameDir.resolve("mods"));
        Path orphanTmp = gameDir.resolve("mods/orphan.dat.jmtmp.dl");
        Files.write(orphanTmp, bytes("partial residue"));

        assertEquals(Updater.OK, updater(gameDir, transport).run());

        assertArrayEquals(content, Files.readAllBytes(gameDir.resolve("mods/keep.jar")));
        assertTrue(Files.exists(orphanTmp), ".jmtmp.dl 残留应被减量跳过保留供续传");
    }

    /** 大文件走流式 fetchArtifactStream，不再走 byte[] fetchArtifact（内存不随文件大小增长）。 */
    @Test
    void largeFileUsesStreamNotByteArray(@TempDir Path gameDir) throws Exception {
        RecordingTransport transport = new RecordingTransport();
        // 2MB 随机内容（不可压缩），制品 ~2MB，验证不进 byte[]。
        byte[] content = new byte[2 * 1024 * 1024];
        new java.util.Random(7).nextBytes(content);
        install(transport, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/big.bin", content)));

        assertEquals(Updater.OK, updater(gameDir, transport).run());

        assertTrue(transport.streamInvoked, "大文件应走流式 fetchArtifactStream");
        assertFalse(transport.byteArrayInvoked, "不应再走 byte[] fetchArtifact");
        assertArrayEquals(content, Files.readAllBytes(gameDir.resolve("mods/big.bin")));
    }

    /** sha256 不符时删除临时文件并报错（不污染目标）。 */
    @Test
    void corruptArtifactRejectedAndTmpCleaned(@TempDir Path gameDir) throws Exception {
        TestFixtures.MemoryTransport transport = new TestFixtures.MemoryTransport();
        byte[] content = bytes("good");
        install(transport, 1, Collections.singletonList("mods"),
                Collections.singletonList(new TestFixtures.FileSpec("mods/c.jar", content)));
        // 篡改制品：让 artifactSha256 寻址到的内容被换坏。
        String artSha = transport.artifacts.keySet().iterator().next();
        transport.artifacts.put(artSha, bytes("corrupted payload not matching sha"));

        assertEquals(Updater.FAIL_STATIC, updater(gameDir, transport).run(),
                "制品 sha256 不符应 fail-static");
        assertFalse(Files.exists(gameDir.resolve("mods/c.jar")), "校验失败不应写目标文件");
        assertFalse(Files.exists(gameDir.resolve("mods/c.jar.jmtmp.dl")),
                "校验失败应删除下载临时");
    }

    /** 可中断流式 Transport：读到指定字节数后抛 IOException 模拟下载中断。 */
    static final class InterruptingTransport extends TestFixtures.MemoryTransport {
        long failAfterBytes = -1;

        @Override
        public InputStream fetchArtifactStream(String sha, long offset) throws IOException {
            InputStream delegate = super.fetchArtifactStream(sha, offset);
            if (failAfterBytes < 0) {
                return delegate;
            }
            return new InputStream() {
                private long read;
                private boolean interrupted;

                @Override
                public int read() throws IOException {
                    if (interrupted) {
                        throw new IOException("模拟下载中断");
                    }
                    int b = delegate.read();
                    if (b != -1) {
                        read++;
                        if (read >= failAfterBytes) {
                            interrupted = true;
                        }
                    }
                    return b;
                }

                @Override
                public int read(byte[] b, int off, int len) throws IOException {
                    if (interrupted) {
                        throw new IOException("模拟下载中断");
                    }
                    int n = delegate.read(b, off, len);
                    if (n > 0) {
                        read += n;
                        if (read >= failAfterBytes) {
                            interrupted = true;
                        }
                    }
                    return n;
                }

                @Override
                public void close() throws IOException {
                    delegate.close();
                }
            };
        }
    }

    /** 记录流式 vs byte[] 拉取是否被调用的 Transport。 */
    static final class RecordingTransport extends TestFixtures.MemoryTransport {
        boolean streamInvoked;
        boolean byteArrayInvoked;

        @Override
        public InputStream fetchArtifactStream(String sha, long offset) throws IOException {
            streamInvoked = true;
            return super.fetchArtifactStream(sha, offset);
        }

        @Override
        public byte[] fetchArtifact(String sha) throws IOException {
            byteArrayInvoked = true;
            return super.fetchArtifact(sha);
        }
    }
}
