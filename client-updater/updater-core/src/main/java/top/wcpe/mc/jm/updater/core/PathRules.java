package top.wcpe.mc.jm.updater.core;

import java.nio.file.Path;
import java.util.List;
import java.util.Locale;

/**
 * 路径安全与托管区/玩家区边界（契约 §2 path 规则 / §6.4）。
 *
 * <p>玩家区永不碰：{@code saves/}、{@code screenshots/}、{@code logs/}、{@code crash-reports/}、
 * {@code options.txt}、及任何不在 {@code managedDirs} 下的路径。
 */
final class PathRules {

    /** 玩家区固定前缀/文件（即便误列入 managedDirs 也永不增删，纵深防御）。 */
    private static final String[] PLAYER_ZONE = {
            "saves/", "screenshots/", "logs/", "crash-reports/",
            "options.txt", "optionsof.txt", "servers.dat", "usercache.json"
    };

    private PathRules() {
    }

    /**
     * manifest 相对路径是否合法：非空、POSIX `/`、不含 `..` 逃逸、不绝对、不落玩家区。
     */
    static boolean isSafeRelative(String path) {
        if (path == null || path.isEmpty()) {
            return false;
        }
        String norm = path.replace('\\', '/');
        if (norm.startsWith("/") || norm.contains(":")) {
            return false;
        }
        for (String seg : norm.split("/")) {
            if (seg.equals("..") || seg.equals(".")) {
                return false;
            }
        }
        return !isPlayerZone(norm);
    }

    /** 该相对路径是否落入玩家区（永不碰）。 */
    static boolean isPlayerZone(String relPath) {
        String p = relPath.replace('\\', '/').toLowerCase(Locale.ROOT);
        for (String pz : PLAYER_ZONE) {
            if (pz.endsWith("/")) {
                if (p.equals(pz.substring(0, pz.length() - 1)) || p.startsWith(pz)) {
                    return true;
                }
            } else if (p.equals(pz)) {
                return true;
            }
        }
        return false;
    }

    /** 该相对路径是否在某个托管目录下（managedDirs 内才允许增删）。 */
    static boolean isUnderManaged(String relPath, List<String> managedDirs) {
        String p = relPath.replace('\\', '/');
        for (String dir : managedDirs) {
            String d = dir.replace('\\', '/');
            if (!d.endsWith("/")) {
                d = d + "/";
            }
            if (p.equals(dir) || p.startsWith(d)) {
                return true;
            }
        }
        return false;
    }

    /**
     * 解析相对路径到 gameDir 下的绝对路径，并校验未逃逸出 gameDir（双保险）。
     *
     * @return 绝对规范化路径；逃逸时抛 {@link SecurityException}。
     */
    static Path resolveSafe(Path gameDir, String relPath) {
        Path base = gameDir.toAbsolutePath().normalize();
        Path resolved = base.resolve(relPath).normalize();
        if (!resolved.startsWith(base)) {
            throw new SecurityException("路径逃逸 gameDir: " + relPath);
        }
        return resolved;
    }
}
