package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

/**
 * 文件级 reconcile 引擎（契约 §2/§6.4，FR-090 核心）。
 *
 * <p>增量：对 manifest 中 {@code platform} 适配本机的文件，md5/size 快筛命中即跳过；
 * 否则从 CAS 或 transport 取制品 → 解码 → sha256 强校验 → 原子放置。
 * 减量：仅在 {@code managedDirs} 内、对 {@code sync} 非 once/ignore 的文件，删「本地有但 manifest 未列」的。
 * 玩家区永不碰（{@link PathRules}）。
 */
final class Reconciler {

    /** reconcile 统计结果（供日志/遥测）。 */
    static final class Result {
        int downloaded;
        int skipped;
        int removed;
        int casHits;
        final List<String> errors = new ArrayList<>();

        @Override
        public String toString() {
            return "downloaded=" + downloaded + " skipped=" + skipped
                    + " removed=" + removed + " casHits=" + casHits
                    + " errors=" + errors.size();
        }
    }

    private final Path gameDir;
    private final Transport transport;
    private final CasCache cas;
    private final Platform platform;
    private final Logger log;

    Reconciler(Path gameDir, Transport transport, CasCache cas, Platform platform, Logger log) {
        this.gameDir = gameDir.toAbsolutePath().normalize();
        this.transport = transport;
        this.cas = cas;
        this.platform = platform;
        this.log = log;
    }

    /**
     * 执行 reconcile。任何单文件失败记入 {@link Result#errors} 但不中断整体；
     * 调用方据 errors 是否为空决定成功/ fail-static。
     */
    Result reconcile(Manifest manifest) throws IOException {
        Result result = new Result();

        // 本机适配的目标文件相对路径集合（用于减量时判断「manifest 未列」）。
        Set<String> desiredPaths = new HashSet<>();

        for (Manifest.FileEntry entry : manifest.files) {
            if (!platform.matches(entry.platform)) {
                continue;
            }
            if (!PathRules.isSafeRelative(entry.path)) {
                result.errors.add("非法路径，跳过: " + entry.path);
                log.warn("reconcile 跳过非法路径: " + entry.path);
                continue;
            }
            desiredPaths.add(entry.path.replace('\\', '/'));

            if ("ignore".equals(entry.sync)) {
                // ignore：列出但不增不删（仅展示/审计）。
                continue;
            }

            try {
                applyFile(entry, result);
            } catch (Exception e) {
                result.errors.add(entry.path + ": " + e.getMessage());
                log.warn("reconcile 文件失败 " + entry.path + ": " + e);
            }
        }

        // 减量：仅 managedDirs 内、sync!=once&&!=ignore（once/ignore 文件玩家可留）。
        Set<String> protectedFromRemoval = new HashSet<>();
        for (Manifest.FileEntry entry : manifest.files) {
            if ("once".equals(entry.sync) || "ignore".equals(entry.sync)) {
                protectedFromRemoval.add(entry.path.replace('\\', '/'));
            }
        }
        removeStale(manifest.managedDirs, desiredPaths, protectedFromRemoval, result);

        return result;
    }

    /** 单文件增量：快筛 → CAS/下载 → 解码 → sha256 校验 → 原子放置（按 sync 策略）。 */
    private void applyFile(Manifest.FileEntry entry, Result result) throws IOException {
        Path target = PathRules.resolveSafe(gameDir, entry.path);

        boolean exists = Files.isRegularFile(target);

        // once：仅当本地缺失才写（玩家可改的整合包配置，契约 §2 sync）。
        if ("once".equals(entry.sync) && exists) {
            result.skipped++;
            return;
        }

        // strict（或 once 且缺失）：md5/size 快筛——命中即认为已是目标内容，跳过下载（性能，ADR-022 决策 3）。
        if (exists && quickMatch(target, entry)) {
            result.skipped++;
            return;
        }

        // 取得目标内容：优先 CAS（内容寻址，命中免下），否则下载制品并解码。
        byte[] content = obtainContent(entry, result);

        // sha256 强校验——信任校验必须 sha256（md5 仅快筛，ADR-022 决策 3）。
        String actual = Hashes.sha256(content);
        if (!actual.equalsIgnoreCase(entry.sha256)) {
            throw new IOException("sha256 校验失败 期望=" + entry.sha256 + " 实际=" + actual);
        }

        // 入 CAS（按解压后内容 sha256），供后续/N-1 复用。
        if (!cas.has(entry.sha256)) {
            cas.put(entry.sha256, content);
        }

        atomicWrite(target, content);
        result.downloaded++;
        log.debug("reconcile 写入 " + entry.path + " (" + content.length + "B)");
    }

    /** md5+size 快筛：本地文件与 manifest 声明一致则视为命中（弱校验，仅免下载）。 */
    private boolean quickMatch(Path target, Manifest.FileEntry entry) throws IOException {
        if (entry.size > 0 && Files.size(target) != entry.size) {
            return false;
        }
        if (entry.md5 != null) {
            return Hashes.md5(target).equalsIgnoreCase(entry.md5);
        }
        // 无 md5 时退回 sha256（仍免下载，只是多算一次本地强 hash）。
        if (entry.sha256 != null) {
            return Hashes.sha256(target).equalsIgnoreCase(entry.sha256);
        }
        return false;
    }

    /** 从 CAS 取（命中）或下载制品并按 codec 解码。 */
    private byte[] obtainContent(Manifest.FileEntry entry, Result result) throws IOException {
        if (entry.sha256 != null && cas.has(entry.sha256)) {
            result.casHits++;
            return cas.get(entry.sha256);
        }
        if (entry.artifactSha256 == null) {
            throw new IOException("缺少 artifact 信息，无法下载: " + entry.path);
        }
        byte[] artifact = transport.fetchArtifact(entry.artifactSha256);
        // 制品自身 hash 校验（下载寻址 key），防 CDN 返回错内容。
        String artHash = Hashes.sha256(artifact);
        if (!artHash.equalsIgnoreCase(entry.artifactSha256)) {
            throw new IOException("制品 hash 不符 期望=" + entry.artifactSha256 + " 实际=" + artHash);
        }
        return Codec.decode(artifact, entry.artifactCodec);
    }

    /** temp 写 + 原子换（契约「中断不损坏客户端」）。 */
    private void atomicWrite(Path target, byte[] content) throws IOException {
        Files.createDirectories(target.getParent());
        Path tmp = target.resolveSibling(target.getFileName() + ".jmtmp." + System.nanoTime());
        Files.write(tmp, content);
        try {
            Files.move(tmp, target, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
        } catch (java.nio.file.AtomicMoveNotSupportedException e) {
            Files.move(tmp, target, StandardCopyOption.REPLACE_EXISTING);
        } finally {
            Files.deleteIfExists(tmp);
        }
    }

    /** 减量：遍历 managedDirs，删本地存在但 manifest 未列、且非 once/ignore 保护的文件。 */
    private void removeStale(List<String> managedDirs, Set<String> desiredPaths,
                             Set<String> protectedFromRemoval, Result result) {
        for (String dir : managedDirs) {
            Path dirPath = PathRules.resolveSafe(gameDir, dir);
            if (!Files.isDirectory(dirPath)) {
                continue;
            }
            List<Path> localFiles = new ArrayList<>();
            try (java.util.stream.Stream<Path> walk = Files.walk(dirPath)) {
                for (Path p : (Iterable<Path>) walk::iterator) {
                    if (Files.isRegularFile(p)) {
                        localFiles.add(p);
                    }
                }
            } catch (IOException e) {
                result.errors.add("遍历托管目录失败 " + dir + ": " + e.getMessage());
                continue;
            }
            for (Path p : localFiles) {
                String rel = gameDir.relativize(p).toString().replace('\\', '/');
                if (desiredPaths.contains(rel) || protectedFromRemoval.contains(rel)) {
                    continue;
                }
                if (PathRules.isPlayerZone(rel)) {
                    continue;
                }
                // 跳过我方临时文件。
                if (rel.contains(".jmtmp.")) {
                    continue;
                }
                try {
                    Files.deleteIfExists(p);
                    result.removed++;
                    log.debug("reconcile 减量删除 " + rel);
                } catch (IOException e) {
                    result.errors.add("删除失败 " + rel + ": " + e.getMessage());
                }
            }
        }
    }
}
