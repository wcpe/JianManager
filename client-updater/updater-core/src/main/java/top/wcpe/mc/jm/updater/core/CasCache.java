package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;

/**
 * 内容寻址缓存（CAS，契约 §6 / FR-090）：按解压后内容 sha256 存原始内容，命中免下。
 * 自更新 N-1 与重复文件零重下载靠此（FR-091 复用）。
 *
 * <p>落 {@code <gameDir>/.jm-updater/cas/<aa>/<sha256>}（两级前缀分桶避免单目录过多）。
 * 提供 LRU + 容量上限清理（契约「缓存清理 / 容量上限」）。
 */
final class CasCache {

    private final Path root;

    CasCache(Path root) {
        this.root = root;
    }

    /** 缓存内是否已有该内容。 */
    boolean has(String sha256) {
        return Files.isRegularFile(pathFor(sha256));
    }

    /** 读取缓存内容（调用方确保 {@link #has} 为真）。 */
    byte[] get(String sha256) throws IOException {
        Path p = pathFor(sha256);
        // touch 访问时间用于 LRU。
        try {
            Files.setLastModifiedTime(p, java.nio.file.attribute.FileTime.fromMillis(System.currentTimeMillis()));
        } catch (IOException ignore) {
            // 触碰失败不影响读取。
        }
        return Files.readAllBytes(p);
    }

    /** 原子写入内容（内容寻址，键即 sha256）。 */
    void put(String sha256, byte[] content) throws IOException {
        Path dest = pathFor(sha256);
        Files.createDirectories(dest.getParent());
        Path tmp = dest.resolveSibling(dest.getFileName() + ".tmp." + System.nanoTime());
        Files.write(tmp, content);
        try {
            Files.move(tmp, dest, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
        } catch (java.nio.file.AtomicMoveNotSupportedException e) {
            Files.move(tmp, dest, StandardCopyOption.REPLACE_EXISTING);
        }
    }

    /**
     * LRU 清理到容量上限：当缓存总字节超过 {@code maxBytes} 时，按最后访问时间升序删旧条目。
     */
    void enforceLimit(long maxBytes) throws IOException {
        if (!Files.isDirectory(root)) {
            return;
        }
        List<Path> files = new ArrayList<>();
        long total = 0;
        try (java.util.stream.Stream<Path> walk = Files.walk(root)) {
            for (Path p : (Iterable<Path>) walk::iterator) {
                if (Files.isRegularFile(p)) {
                    files.add(p);
                    total += Files.size(p);
                }
            }
        }
        if (total <= maxBytes) {
            return;
        }
        files.sort(Comparator.comparingLong(p -> {
            try {
                return Files.getLastModifiedTime(p).toMillis();
            } catch (IOException e) {
                return 0L;
            }
        }));
        for (Path p : files) {
            if (total <= maxBytes) {
                break;
            }
            try {
                long sz = Files.size(p);
                Files.deleteIfExists(p);
                total -= sz;
            } catch (IOException ignore) {
                // 单文件删除失败不阻断整体清理。
            }
        }
    }

    private Path pathFor(String sha256) {
        String prefix = sha256.length() >= 2 ? sha256.substring(0, 2) : "00";
        return root.resolve(prefix).resolve(sha256);
    }
}
