package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.nio.file.StandardOpenOption;
import java.security.DigestInputStream;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.function.LongConsumer;

/**
 * 文件级 reconcile 引擎（契约 §2/§6.4，FR-090 核心）。
 *
 * <p>增量：对 manifest 中 {@code platform} 适配本机的文件，md5/size 快筛命中即跳过；
 * 否则从 transport 取制品 → 解码 → sha256 强校验 → 原子放置。
 * 减量：仅在 {@code managedDirs} 内、对 {@code sync} 非 once/ignore 的文件，删「本地有但 manifest 未列」的。
 * 玩家区永不碰（{@link PathRules}）。
 *
 * <p>FR-256 起：去掉 CAS 内容寻址缓存（CasCache 已删）。重复文件复用靠 size+sha256 快筛（命中即跳过下载）。
 *
 * <p>FR-257 起：下载改为流式——Transport 返回 {@link InputStream}，边读边写盘（64KB 缓冲）+
 * {@link DigestInputStream} 流式 sha256 + zstd 流式解压；支持 HTTP Range 断点续传
 * （{@code .jmtmp.dl} 残留则从已下载大小处续传）。1GB 文件下载内存占用恒定 &lt; 10MB。
 */
final class Reconciler {

    /** reconcile 统计结果（供日志/遥测）。 */
    static final class Result {
        int downloaded;
        int skipped;
        int removed;
        final List<String> errors = new ArrayList<>();

        @Override
        public String toString() {
            return "downloaded=" + downloaded + " skipped=" + skipped
                    + " removed=" + removed
                    + " errors=" + errors.size();
        }
    }

    private final Path gameDir;
    private final Transport transport;
    private final Platform platform;
    private final Logger log;
    /** 下载进度上报（FR-099）；不展示时为 Noop reporter（非 null）。 */
    private final ProgressReporter reporter;

    Reconciler(Path gameDir, Transport transport, Platform platform, Logger log,
               ProgressReporter reporter) {
        this.gameDir = gameDir.toAbsolutePath().normalize();
        this.transport = transport;
        this.platform = platform;
        this.log = log;
        this.reporter = reporter;
    }

    /**
     * 执行 reconcile。任何单文件失败记入 {@link Result#errors} 但不中断整体；
     * 调用方据 errors 是否为空决定成功/ fail-static。
     */
    Result reconcile(Manifest manifest) throws IOException {
        Result result = new Result();

        // 进度分母：廉价预估本次将下载的压缩字节（不哈希；FR-099）。
        reporter.plan(estimateDownloadBytes(manifest));

        // 本机适配的目标文件相对路径集合（用于减量时判断「manifest 未列」）。
        Set<String> desiredPaths = new HashSet<>();

        for (Manifest.FileEntry entry : manifest.files) {
            // 玩家关闭进度窗 → 停止下载、fail-static 放行（FR-099）。
            if (reporter.isCancelled()) {
                result.errors.add("用户取消更新（玩家关闭进度窗）");
                log.warn("玩家关闭进度窗，取消剩余下载，fail-static 带本地版本放行");
                break;
            }
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

        // 取消时不做减量（避免半截状态删玩家文件）。
        if (reporter.isCancelled()) {
            return result;
        }

        // 减量：仅 managedDirs 内、sync!=once&&!=ignore（once/ignore 文件玩家可留）。
        Set<String> protectedFromRemoval = new HashSet<>();
        for (Manifest.FileEntry entry : manifest.files) {
            if ("once".equals(entry.sync) || "ignore".equals(entry.sync)) {
                protectedFromRemoval.add(entry.path.replace('\\', '/'));
            }
        }
        removeStale(manifest.managedDirs, manifest.cleanExclude, desiredPaths, protectedFromRemoval, result);

        return result;
    }

    /**
     * 廉价预估本次将下载的压缩字节（FR-099 进度分母）：不哈希——按 once/exists、size 匹配粗筛，
     * 余者计 {@code artifactSize}。同大小不同 md5 的极少数误判由收尾 snap-to-100% 兜底。
     */
    private long estimateDownloadBytes(Manifest manifest) {
        long total = 0;
        for (Manifest.FileEntry entry : manifest.files) {
            if (!platform.matches(entry.platform) || !PathRules.isSafeRelative(entry.path)) {
                continue;
            }
            if ("ignore".equals(entry.sync)) {
                continue;
            }
            Path target = PathRules.resolveSafe(gameDir, entry.path);
            boolean exists = java.nio.file.Files.isRegularFile(target);
            if ("once".equals(entry.sync) && exists) {
                continue; // 仅缺失才写，已存在不下。
            }
            if (exists && sizeMatches(target, entry)) {
                continue; // 大小一致，大概率快筛跳过（不哈希）。
            }
            if (entry.artifactSha256 == null) {
                continue; // 无制品信息，无法下载。
            }
            total += entry.artifactSize > 0 ? entry.artifactSize : entry.size;
        }
        return total;
    }

    private boolean sizeMatches(Path target, Manifest.FileEntry entry) {
        try {
            return entry.size > 0 && java.nio.file.Files.size(target) == entry.size;
        } catch (IOException e) {
            return false;
        }
    }

    /** 单文件增量：快筛 → 流式下载 → 校验 → 解压 → 原子放置（按 sync 策略，FR-257 流式）。 */
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
        if (exists && tryPatchAndPlace(entry, target)) {
            result.downloaded++;
            return;
        }

        // 流式下载并原子放置（FR-257：64KB 缓冲，不再全量读进 byte[]）。
        downloadAndPlace(entry, target);
        result.downloaded++;
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

    /** 64KB 流式读写缓冲（FR-257：大文件内存占用恒定，不随文件大小增长）。 */
    private static final int STREAM_BUF = 64 * 1024;
    /** 下载临时后缀（存制品流，支持断点续传；含 {@code .jmtmp.} 以被减量跳过保留供下次续传）。 */
    private static final String DL_TMP_SUFFIX = ".jmtmp.dl";
    /** 解压输出临时后缀（仅 zstd 路径用）。 */
    private static final String OUT_TMP_SUFFIX = ".jmtmp.out";
    /** patch 下载临时后缀。 */
    private static final String PATCH_TMP_SUFFIX = ".jmtmp.patch";
    /** zstd-jni raw dict API 需要 byte[]；超过此阈值直接回退完整制品，避免旧文件进 Java 堆。 */
    private static final long PATCH_DICT_HEAP_LIMIT = 64L * 1024L * 1024L;

    /**
     * 流式下载制品 → 校验 → 解压 → 原子放置（FR-257）。
     *
     * <p>下载阶段：检查 {@code .jmtmp.dl} 已下载大小 → HTTP Range 从断点续传 → 追加写（64KB 缓冲），
     * 边读边回调进度（玩家关窗时 sink 抛异常中止本次下载，已写部分保留供下次续传）。下载完成后校验
     * 制品 sha256（下载寻址 key，防 CDN 错内容）。随后按 codec 放置：{@code none} 直接原子 rename；
     * {@code zstd} 流式解压 + {@link DigestInputStream} 算内容 sha256 → 写 {@code .jmtmp.out} → 校验通过后 rename。
     *
     * <p>断点续传：{@code .jmtmp.dl} 残留则从当前大小处续传；任一 sha256 不符则删临时报错（不污染目标）。
     */
    private void downloadAndPlace(Manifest.FileEntry entry, Path target) throws IOException {
        if (entry.artifactSha256 == null) {
            throw new IOException("缺少 artifact 信息，无法下载: " + entry.path);
        }
        Files.createDirectories(target.getParent());
        Path dlTmp = target.resolveSibling(target.getFileName() + DL_TMP_SUFFIX);

        // 1. 流式下载（断点续传：从 dlTmp 已有大小处 Range 续传）。
        long offset = Files.isRegularFile(dlTmp) ? Files.size(dlTmp) : 0L;
        reporter.beginFile(entry.path);
        LongConsumer onBytes = reporter.sink();
        try (InputStream net = transport.fetchArtifactStream(entry.artifactSha256, offset);
             OutputStream out = Files.newOutputStream(dlTmp,
                     StandardOpenOption.CREATE, StandardOpenOption.APPEND)) {
            byte[] buf = new byte[STREAM_BUF];
            int n;
            while ((n = net.read(buf)) != -1) {
                out.write(buf, 0, n);
                onBytes.accept((long) n); // 进度推进 + 取消检查（关窗时抛异常中止）
            }
        }

        // 2. 校验制品 sha256（下载寻址 key，防 CDN 返回错内容）。
        if (!Hashes.sha256(dlTmp).equalsIgnoreCase(entry.artifactSha256)) {
            Files.deleteIfExists(dlTmp);
            throw new IOException("制品 hash 不符 期望=" + entry.artifactSha256);
        }

        // 3. 按 codec 放置：none 直接 rename；zstd 流式解压后再 rename。
        placeDecoded(dlTmp, target, entry);
    }

    /** 本地旧文件 hash 命中时优先应用 patch；任一 patch 错误都回退完整制品。 */
    private boolean tryPatchAndPlace(Manifest.FileEntry entry, Path target) {
        if (!entry.hasPatch()) {
            return false;
        }
        try {
            String localSha = Hashes.sha256(target);
            if (!localSha.equalsIgnoreCase(entry.patchOldSha256)) {
                return false;
            }
            applyPatchAndPlace(entry, target);
            return true;
        } catch (IOException e) {
            log.warn("patch 应用失败，回退完整制品 " + entry.path + ": " + e.getMessage());
            return false;
        }
    }

    /** 下载 patch 制品 → 用旧文件作字典解码 → 校验新文件 sha256 → 原子替换。 */
    private void applyPatchAndPlace(Manifest.FileEntry entry, Path target) throws IOException {
        Path patchTmp = target.resolveSibling(target.getFileName() + PATCH_TMP_SUFFIX);
        Path outTmp = target.resolveSibling(target.getFileName() + OUT_TMP_SUFFIX);
        try {
            downloadPatchArtifact(entry, patchTmp);
            decodePatchToOutput(entry, target, patchTmp, outTmp);
            atomicMove(outTmp, target);
        } finally {
            Files.deleteIfExists(patchTmp);
            Files.deleteIfExists(outTmp);
        }
    }

    private void downloadPatchArtifact(Manifest.FileEntry entry, Path patchTmp) throws IOException {
        reporter.beginFile(entry.path);
        LongConsumer onBytes = reporter.sink();
        try (InputStream net = transport.fetchArtifactStream(entry.patchArtifactSha256, 0);
             OutputStream out = Files.newOutputStream(patchTmp,
                     StandardOpenOption.CREATE, StandardOpenOption.TRUNCATE_EXISTING)) {
            byte[] buf = new byte[STREAM_BUF];
            int n;
            while ((n = net.read(buf)) != -1) {
                out.write(buf, 0, n);
                onBytes.accept((long) n);
            }
        }
        if (!Hashes.sha256(patchTmp).equalsIgnoreCase(entry.patchArtifactSha256)) {
            throw new IOException("patch 制品 hash 不符 期望=" + entry.patchArtifactSha256);
        }
    }

    private void decodePatchToOutput(Manifest.FileEntry entry, Path oldContent, Path patchTmp, Path outTmp)
            throws IOException {
        long oldSize = Files.size(oldContent);
        if (oldSize > PATCH_DICT_HEAP_LIMIT) {
            throw new IOException("patch 字典文件过大");
        }
        byte[] oldBytes = Files.readAllBytes(oldContent);
        MessageDigest md = newSha256Digest();
        try (InputStream fileIn = Files.newInputStream(patchTmp);
             InputStream decoded = Codec.decodePatchStream(fileIn, entry.patchArtifactCodec, oldBytes);
             DigestInputStream din = new DigestInputStream(decoded, md);
             OutputStream out = Files.newOutputStream(outTmp,
                     StandardOpenOption.CREATE, StandardOpenOption.TRUNCATE_EXISTING)) {
            byte[] buf = new byte[STREAM_BUF];
            int n;
            while ((n = din.read(buf)) != -1) {
                out.write(buf, 0, n);
            }
        }
        String actual = Hashes.hex(md.digest());
        if (!actual.equalsIgnoreCase(entry.patchNewSha256) || !actual.equalsIgnoreCase(entry.sha256)) {
            throw new IOException("patch 后 sha256 校验失败 期望=" + entry.sha256 + " 实际=" + actual);
        }
    }

    /**
     * 校验内容 sha256 后原子放置到 target。
     * <ul>
     *   <li>{@code none}：dlTmp 即最终内容，校验 entry.sha256 后原子 rename。</li>
     *   <li>{@code zstd}：dlTmp(压缩) → 流式解压 → DigestInputStream(算 entry.sha256) → 写 outTmp → 校验通过后 rename。</li>
     * </ul>
     * zstd 路径需两个临时文件（压缩流不可从中间续解压，故先存压缩流支持断点续传，再整流式解压）。
     */
    private void placeDecoded(Path dlTmp, Path target, Manifest.FileEntry entry) throws IOException {
        boolean zstd = "zstd".equalsIgnoreCase(entry.artifactCodec);
        if (!zstd) {
            // none：dlTmp 即最终内容，校验 entry.sha256 后原子 rename。
            if (!Hashes.sha256(dlTmp).equalsIgnoreCase(entry.sha256)) {
                Files.deleteIfExists(dlTmp);
                throw new IOException("sha256 校验失败 期望=" + entry.sha256);
            }
            atomicMove(dlTmp, target);
            return;
        }
        // zstd：dlTmp(压缩) → 流式解压 + DigestInputStream(算 entry.sha256) → 写 outTmp → 校验 → rename。
        Path outTmp = target.resolveSibling(target.getFileName() + OUT_TMP_SUFFIX);
        MessageDigest md = newSha256Digest();
        try (InputStream fileIn = Files.newInputStream(dlTmp);
             InputStream decoded = Codec.decodeStream(fileIn, entry.artifactCodec);
             DigestInputStream din = new DigestInputStream(decoded, md);
             OutputStream out = Files.newOutputStream(outTmp,
                     StandardOpenOption.CREATE, StandardOpenOption.TRUNCATE_EXISTING)) {
            byte[] buf = new byte[STREAM_BUF];
            int n;
            while ((n = din.read(buf)) != -1) {
                out.write(buf, 0, n);
            }
        } finally {
            Files.deleteIfExists(dlTmp); // 压缩临时用完即删
        }
        String actual = Hashes.hex(md.digest());
        if (!actual.equalsIgnoreCase(entry.sha256)) {
            Files.deleteIfExists(outTmp);
            throw new IOException("sha256 校验失败 期望=" + entry.sha256 + " 实际=" + actual);
        }
        atomicMove(outTmp, target);
    }

    /** 临时文件 → 目标 原子换（不支持原子 move 时退回普通 replace，契约「中断不损坏客户端」）。 */
    private static void atomicMove(Path tmp, Path target) throws IOException {
        try {
            Files.move(tmp, target, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
        } catch (java.nio.file.AtomicMoveNotSupportedException e) {
            Files.move(tmp, target, StandardCopyOption.REPLACE_EXISTING);
        }
    }

    private static MessageDigest newSha256Digest() {
        try {
            return MessageDigest.getInstance("SHA-256");
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }

    /**
     * 减量：遍历托管目录，删本地存在但 manifest 未列、且非 once/ignore 保护的文件。
     * FR-255：managedDirs 含 "*" 时遍历整个 gameDir（clean-all）；cleanExclude 命中则永不删。
     */
    private void removeStale(List<String> managedDirs, List<String> cleanExclude,
                             Set<String> desiredPaths,
                             Set<String> protectedFromRemoval, Result result) {
        // FR-255：clean-all 哨兵 → 遍历整个 gameDir。
        if (managedDirs != null && managedDirs.contains(PathRules.ALL_GAMEDIR_SENTINEL)) {
            removeStaleInDir(gameDir, "", cleanExclude, desiredPaths, protectedFromRemoval, result);
            return;
        }
        for (String dir : managedDirs) {
            Path dirPath = PathRules.resolveSafe(gameDir, dir);
            if (!Files.isDirectory(dirPath)) {
                continue;
            }
            removeStaleInDir(dirPath, dir, cleanExclude, desiredPaths, protectedFromRemoval, result);
        }
    }

    /** 遍历指定目录（含子目录），删本地多余文件。dirPrefix 为该目录相对 gameDir 的前缀（根传空串）。 */
    private void removeStaleInDir(Path dirPath, String dirPrefix, List<String> cleanExclude,
                                   Set<String> desiredPaths, Set<String> protectedFromRemoval,
                                   Result result) {
        List<Path> localFiles = new ArrayList<>();
        try (java.util.stream.Stream<Path> walk = Files.walk(dirPath)) {
            for (Path p : (Iterable<Path>) walk::iterator) {
                if (Files.isRegularFile(p)) {
                    localFiles.add(p);
                }
            }
        } catch (IOException e) {
            result.errors.add("遍历托管目录失败 " + dirPrefix + ": " + e.getMessage());
            return;
        }
        for (Path p : localFiles) {
            String rel = gameDir.relativize(p).toString().replace('\\', '/');
            if (desiredPaths.contains(rel) || protectedFromRemoval.contains(rel)) {
                continue;
            }
            if (PathRules.isPlayerZone(rel)) {
                continue;
            }
            // FR-255：运营自定义排除命中 → 永不删。
            if (PathRules.isExcluded(rel, cleanExclude)) {
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
