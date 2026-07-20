package top.wcpe.mc.jm.updater.wedge;

import java.io.BufferedInputStream;
import java.io.BufferedOutputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.time.LocalDate;
import java.time.ZoneId;
import java.time.format.DateTimeFormatter;
import java.util.Arrays;
import java.util.Comparator;
import java.util.zip.GZIPOutputStream;

/** 管理楔子本地诊断日志的启动时轮换。 */
final class LocalLogRotator {

    static final long MAX_LOG_BYTES = 10L * 1024L * 1024L;
    static final int MAX_ARCHIVES = 5;

    private static final DateTimeFormatter ARCHIVE_TIME = DateTimeFormatter.ofPattern("yyyyMMdd-HHmmss");

    private LocalLogRotator() {
    }

    static void prepare(File logFile) {
        try {
            if (shouldRotate(logFile)) {
                rotate(logFile);
            }
            prune(logFile);
        } catch (IOException | SecurityException ignored) {
            // 诊断日志轮换失败必须放行游戏启动。
        }
    }

    private static boolean shouldRotate(File logFile) throws IOException {
        if (!logFile.isFile()) {
            return false;
        }
        if (logFile.length() >= MAX_LOG_BYTES) {
            return true;
        }
        LocalDate modified = Instant.ofEpochMilli(Files.getLastModifiedTime(logFile.toPath()).toMillis())
                .atZone(ZoneId.systemDefault()).toLocalDate();
        return modified.isBefore(LocalDate.now());
    }

    private static void rotate(File logFile) throws IOException {
        Path source = logFile.toPath();
        String timestamp = Instant.ofEpochMilli(Files.getLastModifiedTime(source).toMillis())
                .atZone(ZoneId.systemDefault()).format(ARCHIVE_TIME);
        Path archive = uniqueArchive(source, timestamp);
        Files.move(source, archive);
        gzip(archive);
    }

    private static Path uniqueArchive(Path source, String timestamp) {
        Path archive = source.resolveSibling(source.getFileName() + "." + timestamp);
        int suffix = 1;
        while (Files.exists(archive) || Files.exists(archive.resolveSibling(archive.getFileName() + ".gz"))) {
            archive = source.resolveSibling(source.getFileName() + "." + timestamp + "-" + suffix++);
        }
        return archive;
    }

    private static void gzip(Path archive) throws IOException {
        Path compressed = archive.resolveSibling(archive.getFileName() + ".gz");
        try (BufferedInputStream input = new BufferedInputStream(new FileInputStream(archive.toFile()));
             GZIPOutputStream output = new GZIPOutputStream(
                     new BufferedOutputStream(new FileOutputStream(compressed.toFile())))) {
            byte[] buffer = new byte[8192];
            int read;
            while ((read = input.read(buffer)) != -1) {
                output.write(buffer, 0, read);
            }
        } catch (IOException e) {
            Files.deleteIfExists(compressed);
            throw e;
        }
        Files.delete(archive);
    }

    private static void prune(File logFile) throws IOException {
        File directory = logFile.getParentFile();
        File[] archives = directory == null ? null : directory.listFiles((dir, name) ->
                name.startsWith(logFile.getName() + ".") && name.endsWith(".gz"));
        if (archives == null || archives.length <= MAX_ARCHIVES) {
            return;
        }
        Arrays.sort(archives, Comparator.comparingLong(File::lastModified).reversed());
        for (int i = MAX_ARCHIVES; i < archives.length; i++) {
            Files.deleteIfExists(archives[i].toPath());
        }
    }
}
