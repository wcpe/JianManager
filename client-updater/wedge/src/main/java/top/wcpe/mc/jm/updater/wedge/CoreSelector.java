package top.wcpe.mc.jm.updater.wedge;

import java.io.File;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.Arrays;
import java.util.Properties;

/**
 * updater-core 自更新选择状态机（FR-091，wedge 侧）。
 *
 * <p>premain 加载 core 前据 {@code <gameDir>/.jm-updater/core/state.properties} +
 * {@code pending.confirmed}/{@code rollback.flag} 标志决定加载哪个 core jar：
 * <ul>
 *   <li>pending 已确认（看门狗建 confirmed）→ <b>promote</b>（selected=pending，prev=旧 selected）；</li>
 *   <li>pending 已 tried 但未确认（上次崩溃/早退）→ <b>回退</b>（弃 pending，留 selected=N-1）；</li>
 *   <li>pending 未 tried → <b>首次 trial</b>（标 tried 后加载 pending）；</li>
 *   <li>否则加载 selected（缺失返回 null，由 Wedge 决定是否下载/fail-open）。</li>
 * </ul>
 * 状态格式与 core 的 {@code CoreSelectStore} 一致（java.util.Properties）。全程 <b>fail-open</b>：任何异常返回 null。
 *
 * <p>FR-258 起：不再有 bundledJar（整合包只带 wedge.jar），本地无 core 时返回 null。
 */
final class CoreSelector {

    private static final String K_SELECTED_SHA = "selectedSha";
    private static final String K_SELECTED_VERSION = "selectedVersion";
    private static final String K_PREV_SHA = "prevSha";
    private static final String K_PREV_VERSION = "prevVersion";
    private static final String K_PENDING_SHA = "pendingSha";
    private static final String K_PENDING_VERSION = "pendingVersion";
    private static final String K_PENDING_TRIED = "pendingTried";
    private static final String K_FAILED_VERSION = "failedVersion";

    /** 本地保留的 core jar 数量上限（FR-258）。 */
    static final int KEEP_JARS = 3;

    /** 加载决策：jar 路径 + 版本 + 是否首次 trial（决定是否起 boot-confirm 看门狗）。coreJar 为 null 表示无可用 core。 */
    static final class Selection {
        final File coreJar;
        final long coreVersion;
        final boolean trial;

        Selection(File coreJar, long coreVersion, boolean trial) {
            this.coreJar = coreJar;
            this.coreVersion = coreVersion;
            this.trial = trial;
        }
    }

    /** 本地状态摘要（不推进状态机，供 Wedge 决策是否下载新版）。 */
    static final class StateSummary {
        final long selectedVersion;
        final boolean hasPending;
        final long failedVersion;
        final boolean hasSelectedJar;

        StateSummary(long selectedVersion, boolean hasPending, long failedVersion, boolean hasSelectedJar) {
            this.selectedVersion = selectedVersion;
            this.hasPending = hasPending;
            this.failedVersion = failedVersion;
            this.hasSelectedJar = hasSelectedJar;
        }
    }

    private CoreSelector() {
    }

    /** 读取本地状态摘要（不推进状态机）。 */
    static StateSummary readSummary(File coreDir) {
        Properties p = load(new File(coreDir, "state.properties"));
        String selectedSha = p.getProperty(K_SELECTED_SHA, "");
        boolean hasSelected = notEmpty(selectedSha)
                && new File(coreDir, selectedSha + ".jar").isFile();
        return new StateSummary(
                parseLong(p.getProperty(K_SELECTED_VERSION, "0")),
                notEmpty(p.getProperty(K_PENDING_SHA, "")),
                parseLong(p.getProperty(K_FAILED_VERSION, "0")),
                hasSelected);
    }

    /** 设置 pending core（下载新版后调用，下次 select 将首次 trial）。 */
    static void setPending(File coreDir, String sha, long version) {
        setPending(coreDir, sha, version, null);
    }

    static void setPending(File coreDir, String sha, long version, WedgeLogger log) {
        try {
            File stateFile = new File(coreDir, "state.properties");
            Properties p = load(stateFile);
            p.setProperty(K_PENDING_SHA, sha);
            p.setProperty(K_PENDING_VERSION, Long.toString(version));
            p.setProperty(K_PENDING_TRIED, "false");
            store(stateFile, p);
            if (log != null) {
                log.info("设置 pending core version=" + version + " sha=" + sha);
            }
        } catch (Exception e) {
            if (log != null) {
                log.warn("设置 pending 失败: " + e);
            }
            System.err.println("[JM Updater] 设置 pending 失败: " + e);
        }
    }

    /** 保留最近 {@code keep} 个 core jar，超出自动清理最老的（selected/prev/pending 优先保留）。 */
    static void retainLatestJars(File coreDir, int keep) {
        retainLatestJars(coreDir, keep, null);
    }

    static void retainLatestJars(File coreDir, int keep, WedgeLogger log) {
        File[] jars = coreDir.listFiles((dir, name) -> name.endsWith(".jar"));
        if (jars == null || jars.length <= keep) {
            return;
        }
        Properties p = load(new File(coreDir, "state.properties"));
        String selectedSha = p.getProperty(K_SELECTED_SHA, "");
        String prevSha = p.getProperty(K_PREV_SHA, "");
        String pendingSha = p.getProperty(K_PENDING_SHA, "");
        // 排序：在用 jar 优先（selected > prev > pending > 其他），同类按最后修改时间倒序
        Arrays.sort(jars, (a, b) -> {
            int pa = jarPriority(a, selectedSha, prevSha, pendingSha);
            int pb = jarPriority(b, selectedSha, prevSha, pendingSha);
            if (pa != pb) {
                return pb - pa;
            }
            return Long.compare(b.lastModified(), a.lastModified());
        });
        for (int i = keep; i < jars.length; i++) {
            if (!jars[i].delete()) {
                if (log != null) {
                    log.warn("清理旧 core jar 失败: " + jars[i].getName());
                }
                System.err.println("[JM Updater] 清理旧 core jar 失败: " + jars[i].getName());
            } else if (log != null) {
                log.info("已清理旧 core jar: " + jars[i].getName());
            }
        }
    }

    private static int jarPriority(File jar, String selectedSha, String prevSha, String pendingSha) {
        String sha = jarNameNoExt(jar.getName());
        if (sha.equals(selectedSha)) {
            return 4;
        }
        if (sha.equals(prevSha)) {
            return 3;
        }
        if (sha.equals(pendingSha)) {
            return 2;
        }
        return 1;
    }

    private static String jarNameNoExt(String name) {
        int dot = name.lastIndexOf('.');
        return dot > 0 ? name.substring(0, dot) : name;
    }

    /** 决定加载哪个 core 并据此推进状态；任何异常 fail-open 返回 null。 */
    static Selection select(File coreDir) {
        return select(coreDir, null);
    }

    static Selection select(File coreDir, WedgeLogger log) {
        try {
            return selectInternal(coreDir, log);
        } catch (Throwable t) {
            if (log != null) {
                log.error("core 选择异常，无可用 core: " + t);
            }
            System.err.println("[JM Updater] core 选择异常，无可用 core: " + t);
            return new Selection(null, 0, false);
        }
    }

    private static Selection selectInternal(File coreDir, WedgeLogger log) throws Exception {
        File stateFile = new File(coreDir, "state.properties");
        File confirmedFlag = new File(coreDir, "pending.confirmed");
        File rollbackFlag = new File(coreDir, "rollback.flag");
        Properties p = load(stateFile);

        // 手动回退：运营/玩家放置 rollback.flag → 弃 pending，selected 回 prev（无 prev 则清 selected）。
        if (rollbackFlag.isFile()) {
            if (log != null) {
                log.warn("检测到 rollback.flag，执行手动回退");
            }
            confirmedFlag.delete();
            clearPending(p);
            if (notEmpty(p.getProperty(K_PREV_SHA))) {
                p.setProperty(K_SELECTED_SHA, p.getProperty(K_PREV_SHA));
                p.setProperty(K_SELECTED_VERSION, p.getProperty(K_PREV_VERSION, "0"));
                p.remove(K_PREV_SHA);
                p.remove(K_PREV_VERSION);
            } else {
                p.remove(K_SELECTED_SHA);
                p.remove(K_SELECTED_VERSION);
            }
            store(stateFile, p);
            rollbackFlag.delete();
        }

        String pendingSha = p.getProperty(K_PENDING_SHA, "");
        if (notEmpty(pendingSha)) {
            boolean tried = "true".equalsIgnoreCase(p.getProperty(K_PENDING_TRIED, "false"));
            if (confirmedFlag.isFile()) {
                if (log != null) {
                    log.info("pending core 已确认，执行 promote version=" + p.getProperty(K_PENDING_VERSION, "0"));
                }
                // promote：prev=selected，selected=pending。
                if (notEmpty(p.getProperty(K_SELECTED_SHA))) {
                    p.setProperty(K_PREV_SHA, p.getProperty(K_SELECTED_SHA));
                    p.setProperty(K_PREV_VERSION, p.getProperty(K_SELECTED_VERSION, "0"));
                }
                p.setProperty(K_SELECTED_SHA, pendingSha);
                p.setProperty(K_SELECTED_VERSION, p.getProperty(K_PENDING_VERSION, "0"));
                clearPending(p);
                store(stateFile, p);
                confirmedFlag.delete();
                // 落到下方 normal 加载新 selected。
            } else if (tried) {
                // 上次 trial 未确认（崩溃/早退）→ 回退：弃 pending，保留 selected（N-1）。
                // 并记失败版本：否则下一次 reconcile 会立刻重暂存同一坏 core，形成「每隔一次启动 trial 崩溃」的
                // boot-loop（FR-091 真机发现）。楔子据此跳过该版本，仅当出现更高版本（修复版）才再暂存。
                if (log != null) {
                    log.warn("上次新 core 未确认启动，回退到上一可用版本 version=" + p.getProperty(K_PENDING_VERSION, "0"));
                }
                System.err.println("[JM Updater] 上次新 core 未确认启动，回退到上一可用版本。");
                long failed = parseLong(p.getProperty(K_PENDING_VERSION));
                if (failed > parseLong(p.getProperty(K_FAILED_VERSION))) {
                    p.setProperty(K_FAILED_VERSION, Long.toString(failed));
                }
                clearPending(p);
                store(stateFile, p);
            } else {
                // 首次 trial：先持久化 tried（崩溃后下次据此回退），再加载 pending。
                p.setProperty(K_PENDING_TRIED, "true");
                store(stateFile, p);
                File jar = new File(coreDir, pendingSha + ".jar");
                if (jar.isFile()) {
                    if (log != null) {
                        log.info("首次 trial pending core version=" + p.getProperty(K_PENDING_VERSION, "0"));
                    }
                    return new Selection(jar, parseLong(p.getProperty(K_PENDING_VERSION)), true);
                }
                // pending jar 不见了 → 弃，继续 normal。
                clearPending(p);
                store(stateFile, p);
            }
        }

        // normal：selected jar 在则用之，否则返回 null（Wedge 据此决定下载/fail-open）。
        String selectedSha = p.getProperty(K_SELECTED_SHA, "");
        if (notEmpty(selectedSha)) {
            File jar = new File(coreDir, selectedSha + ".jar");
            if (jar.isFile()) {
                if (log != null) {
                    log.info("选择 selected core version=" + p.getProperty(K_SELECTED_VERSION, "0"));
                }
                return new Selection(jar, parseLong(p.getProperty(K_SELECTED_VERSION)), false);
            }
        }
        if (log != null) {
            log.warn("本地状态中没有可用 selected core jar");
        }
        return new Selection(null, 0, false);
    }

    /**
     * trial 加载且 core 正常运行后起 boot-confirm 看门狗（FR-091）：daemon 线程睡 {@code seconds} 秒，
     * 游戏仍存活则建 {@code pending.confirmed} 标志（下次 premain 据此 promote）。
     * 游戏崩溃则 daemon 随 JVM 死、标志不建 = 未确认，下次回退。绝不阻塞/挡游戏。
     */
    static void scheduleBootConfirm(final File coreDir, final int seconds) {
        scheduleBootConfirm(coreDir, seconds, null);
    }

    static void scheduleBootConfirm(final File coreDir, final int seconds, final WedgeLogger log) {
        final File flag = new File(coreDir, "pending.confirmed");
        Thread t = new Thread(new Runnable() {
            @Override
            public void run() {
                try {
                    Thread.sleep(Math.max(1, seconds) * 1000L);
                    coreDir.mkdirs();
                    if (!flag.exists()) {
                        // 仅 touch（建空文件）——无读改写竞态。
                        Files.write(flag.toPath(), new byte[0]);
                        if (log != null) {
                            log.info("boot-confirm 已写入 pending.confirmed");
                        }
                    }
                } catch (Throwable e) {
                    if (log != null) {
                        log.warn("boot-confirm 写入失败: " + e);
                    }
                    // 看门狗失败=不确认→下次回退，绝不影响游戏。
                }
            }
        }, "jm-updater-boot-confirm");
        t.setDaemon(true);
        t.start();
    }

    private static Properties load(File stateFile) {
        Properties p = new Properties();
        if (stateFile.isFile()) {
            try (InputStream in = Files.newInputStream(stateFile.toPath())) {
                p.load(in);
            } catch (Exception e) {
                return new Properties(); // 损坏按空状态。
            }
        }
        return p;
    }

    private static void store(File stateFile, Properties p) throws Exception {
        Path file = stateFile.toPath();
        Files.createDirectories(file.getParent());
        Path tmp = file.resolveSibling("state.properties.tmp");
        try (OutputStream out = Files.newOutputStream(tmp)) {
            p.store(out, "jm-updater core self-update state (FR-091)");
        }
        try {
            Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
        } catch (java.nio.file.AtomicMoveNotSupportedException e) {
            Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING);
        }
    }

    private static void clearPending(Properties p) {
        p.remove(K_PENDING_SHA);
        p.remove(K_PENDING_VERSION);
        p.remove(K_PENDING_TRIED);
    }

    private static boolean notEmpty(String s) {
        return s != null && !s.isEmpty();
    }

    private static long parseLong(String s) {
        if (s == null || s.isEmpty()) {
            return 0;
        }
        try {
            return Long.parseLong(s.trim());
        } catch (NumberFormatException e) {
            return 0;
        }
    }
}
