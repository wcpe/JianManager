package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.RandomAccessFile;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.attribute.FileTime;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.stream.Stream;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class LoggerTest {

    @Test
    void rotatesLogAtSizeLimit(@TempDir Path tmp) throws Exception {
        Path logFile = createLogPath(tmp);
        try (RandomAccessFile file = new RandomAccessFile(logFile.toFile(), "rw")) {
            file.setLength(LocalLogRotator.MAX_LOG_BYTES);
        }

        try (Logger logger = Logger.create(tmp)) {
            logger.info("轮换后仍可写入");
        }

        assertTrue(Files.size(logFile) < LocalLogRotator.MAX_LOG_BYTES);
        assertEquals(1, archiveCount(logFile));
    }

    @Test
    void rotatesLogFromPreviousDay(@TempDir Path tmp) throws Exception {
        Path logFile = createLogPath(tmp);
        Files.write(logFile, new byte[]{1});
        Files.setLastModifiedTime(logFile, FileTime.from(Instant.now().minus(2, ChronoUnit.DAYS)));

        try (Logger ignored = Logger.create(tmp)) {
            // 创建日志器即触发轮换。
        }

        assertEquals(1, archiveCount(logFile));
        assertTrue(Files.isRegularFile(logFile));
    }

    @Test
    void keepsOnlyFiveNewestArchives(@TempDir Path tmp) throws Exception {
        Path logFile = createLogPath(tmp);
        for (int i = 0; i < 7; i++) {
            Path archive = logFile.resolveSibling("updater.log.20260720-12000" + i + ".gz");
            Files.write(archive, new byte[]{(byte) i});
            Files.setLastModifiedTime(archive, FileTime.fromMillis(1_000L + i));
        }

        try (Logger ignored = Logger.create(tmp)) {
            // 创建日志器即触发归档清理。
        }

        assertEquals(LocalLogRotator.MAX_ARCHIVES, archiveCount(logFile));
    }

    @Test
    void failsOpenWhenStateDirectoryIsNotDirectory(@TempDir Path tmp) throws Exception {
        Path stateDir = tmp.resolve("state-file");
        Files.write(stateDir, new byte[]{1});

        assertDoesNotThrow(() -> Logger.create(stateDir).close());
    }

    private static Path createLogPath(Path stateDir) throws Exception {
        Path logDir = stateDir.resolve("logs");
        Files.createDirectories(logDir);
        return logDir.resolve("updater.log");
    }

    private static long archiveCount(Path logFile) throws Exception {
        try (Stream<Path> files = Files.list(logFile.getParent())) {
            return files.filter(path -> path.getFileName().toString().startsWith("updater.log."))
                    .filter(path -> path.getFileName().toString().endsWith(".gz"))
                    .count();
        }
    }
}
