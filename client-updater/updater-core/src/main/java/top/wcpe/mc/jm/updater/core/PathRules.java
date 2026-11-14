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

    /**
     * 更新器自身的接入目录（永不清理）。
     *
     * <p>为什么必须由客户端自己保护、不能靠运营填 cleanExclude：
     * clean-all（managedDirs 含 {@code "*"}）会遍历整个 gameDir 删除“不在清单里”的文件，
     * 而接入指引让玩家把 {@code wedge.jar} 与 {@code jm-updater.json} 放在 {@code jm-updater/}，
     * 更新器自己的 core 缓存/状态在 {@code .jm-updater/}。这两个目录**从来不会出现在 manifest 里**，
     * 于是一次 clean-all 就会把配置与 core 删掉 —— 下次启动读不到配置就跳过更新（Linux 上连
     * wedge.jar 都能被 unlink，下次 JVM 加载 agent 直接失败）。
     *
     * <p>线上已有部署用的是 {@code jm-updater/}；{@code .jm-updater/} 是内部状态目录。
     * 两者都算“更新器私有”，一律不参与增删。
     */
    private static final String[] UPDATER_ZONE = {
            "jm-updater/", ".jm-updater/"
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

    /**
     * 该相对路径是否落入更新器自身目录（永不增删）。
     *
     * <p>与玩家区同级的纵深防御：无论 managedDirs 怎么写、cleanExclude 有没有配、
     * 哪怕是 {@code "*"} 全量清理，这两个目录都不参与删除。
     */
    static boolean isUpdaterZone(String relPath) {
        String p = relPath.replace('\\', '/').toLowerCase(Locale.ROOT);
        for (String uz : UPDATER_ZONE) {
            String dir = uz.substring(0, uz.length() - 1);
            if (p.equals(dir) || p.startsWith(uz)) {
                return true;
            }
        }
        return false;
    }

    /** manifest managedDirs 的「整个 gameDir」哨兵（clean-all）。 */
    static final String ALL_GAMEDIR_SENTINEL = "*";

    /** 该相对路径是否在某个托管目录下（managedDirs 内才允许增删）。 */
    static boolean isUnderManaged(String relPath, List<String> managedDirs) {
        if (managedDirs == null || managedDirs.isEmpty()) {
            return false;
        }
        // 哨兵 "*" 表示整个 gameDir 托管（clean-all），任何路径都算在托管下。
        if (managedDirs.contains(ALL_GAMEDIR_SENTINEL)) {
            return true;
        }
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
     * 该相对路径是否命中运营自定义排除前缀（cleanExclude）。
     * 命中则永不删（与 PLAYER_ZONE 并列的纵深防御）。
     *
     * @param cleanExclude 运营自定义排除列表；null/空视为未声明
     */
    static boolean isExcluded(String relPath, List<String> cleanExclude) {
        if (cleanExclude == null || cleanExclude.isEmpty()) {
            return false;
        }
        String p = relPath.replace('\\', '/');
        for (String excl : cleanExclude) {
            String e = excl.replace('\\', '/');
            if (!e.endsWith("/")) {
                e = e + "/";
            }
            if (p.equals(excl) || p.startsWith(e)) {
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
