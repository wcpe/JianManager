package top.jm.updater.wedge;

import java.io.File;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
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
 *   <li>否则加载 selected（缺失则 bundled）。</li>
 * </ul>
 * 状态格式与 core 的 {@code CoreSelectStore} 一致（java.util.Properties）。全程 <b>fail-open</b>：任何异常回退 bundled。
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

    /** 加载决策：jar 路径 + 版本 + 是否首次 trial（决定是否起 boot-confirm 看门狗）。 */
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

    private CoreSelector() {
    }

    /** 决定加载哪个 core 并据此推进状态；任何异常 fail-open 回退 bundled。 */
    static Selection select(File coreDir, File bundledJar, long bundledVersion) {
        try {
            return selectInternal(coreDir, bundledJar, bundledVersion);
        } catch (Throwable t) {
            System.err.println("[JM Updater] core 选择异常，回退内置 core: " + t);
            return new Selection(bundledJar, bundledVersion, false);
        }
    }

    private static Selection selectInternal(File coreDir, File bundledJar, long bundledVersion)
            throws Exception {
        File stateFile = new File(coreDir, "state.properties");
        File confirmedFlag = new File(coreDir, "pending.confirmed");
        File rollbackFlag = new File(coreDir, "rollback.flag");
        Properties p = load(stateFile);

        // 手动回退：运营/玩家放置 rollback.flag → 弃 pending，selected 回 prev（无 prev 则回 bundled）。
        if (rollbackFlag.isFile()) {
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
                // boot-loop（FR-091 真机发现）。SelfUpdater 据此跳过该版本，仅当出现更高版本（修复版）才再暂存。
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
                    return new Selection(jar, parseLong(p.getProperty(K_PENDING_VERSION)), true);
                }
                // pending jar 不见了 → 弃，继续 normal。
                clearPending(p);
                store(stateFile, p);
            }
        }

        // normal：selected jar 在则用之，否则 bundled。
        String selectedSha = p.getProperty(K_SELECTED_SHA, "");
        if (notEmpty(selectedSha)) {
            File jar = new File(coreDir, selectedSha + ".jar");
            if (jar.isFile()) {
                return new Selection(jar, parseLong(p.getProperty(K_SELECTED_VERSION)), false);
            }
        }
        return new Selection(bundledJar, bundledVersion, false);
    }

    /**
     * trial 加载且 core 正常运行后起 boot-confirm 看门狗（FR-091）：daemon 线程睡 {@code seconds} 秒，
     * 游戏仍存活则建 {@code pending.confirmed} 标志（下次 premain 据此 promote）。
     * 游戏崩溃则 daemon 随 JVM 死、标志不建 = 未确认，下次回退。绝不阻塞/挡游戏。
     */
    static void scheduleBootConfirm(final File coreDir, final int seconds) {
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
                    }
                } catch (Throwable ignore) {
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
