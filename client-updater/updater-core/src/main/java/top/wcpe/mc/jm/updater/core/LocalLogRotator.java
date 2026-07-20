package top.wcpe.mc.jm.updater.core;

import java.io.BufferedInputStream;
import java.io.BufferedOutputStream;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.file.DirectoryStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.attribute.FileTime;
import java.time.Instant;
import java.time.LocalDate;
import java.time.ZoneId;
import java.time.format.DateTimeFormatter;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.zip.GZIPOutputStream;

/** 管理 updater-core 本地诊断日志的启动时轮换。 */
final class LocalLogRotator {

    static final long MAX_LOG_BYTES = 10L * 1024L * 1024L;
    static final int MAX_ARCHIVES = 5;

    private static final DateTimeFormatter ARCHIVE_TIME = DateTimeFormatter.ofPattern("yyyyMMdd-HHmmss");

    private LocalLogRotator() {
    }

    static void prepare(Path logFile) {
        try {
            if (shouldRotate(logFile)) {
                rotate(logFile);
            }
            prune(logFile);
        } catch (IOException | SecurityException ignored) {
            // 诊断日志轮换失败必须放行更新与游戏启动。
        }
    }

    private static boolean shouldRotate(Path logFile) throws IOException {
        if (!Files.isRegularFile(logFile)) {
            return false;
        }
        if (Files.size(logFile) >= MAX_LOG_BYTES) {
            return true;
        }
        LocalDate modified = Files.getLastModifiedTime(logFile).toInstant()
                .atZone(ZoneId.systemDefault()).toLocalDate();
        return modified.isBefore(LocalDate.now());
    }

    private static void rotate(Path logFile) throws IOException {
        FileTime modified = Files.getLastModifiedTime(logFile);
        String timestamp = modified.toInstant().atZone(ZoneId.systemDefault()).format(ARCHIVE_TIME);
        Path archive = uniqueArchive(logFile, timestamp);
        Files.move(logFile, archive);
        gzip(archive);
    }

    private static Path uniqueArchive(Path logFile, String timestamp) {
        Path archive = logFile.resolveSibling(logFile.getFileName() + "." + timestamp);
        int suffix = 1;
        while (Files.exists(archive) || Files.exists(archive.resolveSibling(archive.getFileName() + ".gz"))) {
            archive = logFile.resolveSibling(logFile.getFileName() + "." + timestamp + "-" + suffix++);
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

    private static void prune(Path logFile) throws IOException {
        Path directory = logFile.getParent();
        if (directory == null || !Files.isDirectory(directory)) {
            return;
        }
        List<Path> archives = new ArrayList<Path>();
        try (DirectoryStream<Path> stream = Files.newDirectoryStream(directory,
                logFile.getFileName() + ".*.gz")) {
            for (Path archive : stream) {
                archives.add(archive);
            }
        }
        archives.sort(Comparator.comparingLong(LocalLogRotator::lastModified).reversed());
        for (int i = MAX_ARCHIVES; i < archives.size(); i++) {
            Files.deleteIfExists(archives.get(i));
        }
    }

    private static long lastModified(Path path) {
        try {
            return Files.getLastModifiedTime(path).toMillis();
        } catch (IOException ignored) {
            return Long.MIN_VALUE;
        }
    }
}
