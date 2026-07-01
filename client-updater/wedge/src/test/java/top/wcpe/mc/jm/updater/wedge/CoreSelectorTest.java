package top.wcpe.mc.jm.updater.wedge;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.File;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Properties;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * core 自更新选择状态机（FR-091，wedge 侧）。CoreSelector 仅判 jar 存在性 + 读写 state.properties，
 * 故用占位文件模拟已暂存的 core jar，覆盖 promote/rollback/trial/selected/null/手动回退/看门狗。
 *
 * <p>FR-258 起：不再有 bundledJar，本地无 core 时 select 返回 null。
 */
class CoreSelectorTest {

    private void stageJar(File coreDir, String sha) throws Exception {
        coreDir.mkdirs();
        Files.write(new File(coreDir, sha + ".jar").toPath(), new byte[] { 1 });
    }

    private void writeState(File coreDir, String... kv) throws Exception {
        coreDir.mkdirs();
        Properties p = new Properties();
        for (int i = 0; i + 1 < kv.length; i += 2) {
            p.setProperty(kv[i], kv[i + 1]);
        }
        try (OutputStream o = Files.newOutputStream(new File(coreDir, "state.properties").toPath())) {
            p.store(o, null);
        }
    }

    private Properties readState(File coreDir) throws Exception {
        Properties p = new Properties();
        File f = new File(coreDir, "state.properties");
        if (f.isFile()) {
            try (InputStream in = Files.newInputStream(f.toPath())) {
                p.load(in);
            }
        }
        return p;
    }

    @Test
    void freshReturnsNullWhenNoCore(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        CoreSelector.Selection sel = CoreSelector.select(coreDir);
        assertNull(sel.coreJar, "无本地 core 时应返回 null");
        assertFalse(sel.trial);
    }

    @Test
    void firstTrialLoadsPendingAndMarksTried(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        stageJar(coreDir, "shaPEND");
        writeState(coreDir, "pendingSha", "shaPEND", "pendingVersion", "6", "pendingTried", "false");

        CoreSelector.Selection sel = CoreSelector.select(coreDir);

        assertEquals(new File(coreDir, "shaPEND.jar"), sel.coreJar, "首次应加载 pending");
        assertEquals(6, sel.coreVersion);
        assertTrue(sel.trial, "首次加载 pending 应标记为 trial");
        assertEquals("true", readState(coreDir).getProperty("pendingTried"), "应已置 pendingTried 以便崩溃后回退");
    }

    @Test
    void confirmedPromotesPending(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        stageJar(coreDir, "shaNEW");
        stageJar(coreDir, "shaOLD");
        writeState(coreDir, "selectedSha", "shaOLD", "selectedVersion", "5",
                "pendingSha", "shaNEW", "pendingVersion", "6", "pendingTried", "true");
        Files.write(new File(coreDir, "pending.confirmed").toPath(), new byte[0]);

        CoreSelector.Selection sel = CoreSelector.select(coreDir);

        assertEquals(new File(coreDir, "shaNEW.jar"), sel.coreJar, "确认后应 promote 并加载新 core");
        assertEquals(6, sel.coreVersion);
        assertFalse(sel.trial, "promote 后不再是 trial");
        Properties st = readState(coreDir);
        assertEquals("shaNEW", st.getProperty("selectedSha"));
        assertEquals("shaOLD", st.getProperty("prevSha"), "旧 selected 应降为 N-1");
        assertNull(st.getProperty("pendingSha"), "promote 后清 pending");
        assertFalse(new File(coreDir, "pending.confirmed").exists(), "confirmed 标志应消费删除");
    }

    @Test
    void triedUnconfirmedRollsBackToSelected(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        stageJar(coreDir, "shaNEW");
        stageJar(coreDir, "shaOLD");
        // pending 已 tried 但无 confirmed → 上次 trial 崩溃。
        writeState(coreDir, "selectedSha", "shaOLD", "selectedVersion", "5",
                "pendingSha", "shaNEW", "pendingVersion", "6", "pendingTried", "true");

        CoreSelector.Selection sel = CoreSelector.select(coreDir);

        assertEquals(new File(coreDir, "shaOLD.jar"), sel.coreJar, "未确认应回退到上一可用 N-1");
        assertEquals(5, sel.coreVersion);
        assertFalse(sel.trial);
        Properties rb = readState(coreDir);
        assertNull(rb.getProperty("pendingSha"), "回退后弃 pending");
        assertEquals("6", rb.getProperty("failedVersion"),
                "回退应记失败版本，供楔子跳过重暂存同一坏 core（防 boot-loop，FR-091）");
    }

    @Test
    void triedUnconfirmedNoSelectedReturnsNull(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        stageJar(coreDir, "shaNEW");
        writeState(coreDir, "pendingSha", "shaNEW", "pendingVersion", "6", "pendingTried", "true");

        CoreSelector.Selection sel = CoreSelector.select(coreDir);

        assertNull(sel.coreJar, "无 N-1 时回退返回 null（无 bundled）");
        assertFalse(sel.trial);
    }

    @Test
    void selectedLoadsWhenNoPending(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        stageJar(coreDir, "shaSEL");
        writeState(coreDir, "selectedSha", "shaSEL", "selectedVersion", "7");

        CoreSelector.Selection sel = CoreSelector.select(coreDir);

        assertEquals(new File(coreDir, "shaSEL.jar"), sel.coreJar);
        assertEquals(7, sel.coreVersion);
        assertFalse(sel.trial);
    }

    @Test
    void rollbackFlagRevertsToPrev(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        stageJar(coreDir, "shaCUR");
        stageJar(coreDir, "shaPRV");
        writeState(coreDir, "selectedSha", "shaCUR", "selectedVersion", "8",
                "prevSha", "shaPRV", "prevVersion", "7",
                "pendingSha", "shaX", "pendingVersion", "9", "pendingTried", "false");
        Files.write(new File(coreDir, "rollback.flag").toPath(), new byte[0]);

        CoreSelector.Selection sel = CoreSelector.select(coreDir);

        assertEquals(new File(coreDir, "shaPRV.jar"), sel.coreJar, "手动回退应回 prev");
        assertEquals(7, sel.coreVersion);
        Properties st = readState(coreDir);
        assertEquals("shaPRV", st.getProperty("selectedSha"));
        assertNull(st.getProperty("pendingSha"), "手动回退弃 pending");
        assertFalse(new File(coreDir, "rollback.flag").exists(), "rollback.flag 应被消费删除");
    }

    @Test
    void selectReturnsNullOnGarbageState(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        coreDir.mkdirs();
        // selected 指向不存在的 jar → 应返回 null（不抛）。
        writeState(coreDir, "selectedSha", "missing", "selectedVersion", "5");
        CoreSelector.Selection sel = CoreSelector.select(coreDir);
        assertNull(sel.coreJar, "selected jar 缺失应返回 null");
    }

    @Test
    void scheduleBootConfirmCreatesFlag(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        coreDir.mkdirs();
        CoreSelector.scheduleBootConfirm(coreDir, 1);
        File flag = new File(coreDir, "pending.confirmed");
        // 看门狗最少睡 1s；轮询至多 ~4s。
        long deadline = System.currentTimeMillis() + 4000;
        while (!flag.exists() && System.currentTimeMillis() < deadline) {
            Thread.sleep(100);
        }
        assertTrue(flag.exists(), "存活到 boot-confirm 时限应建 pending.confirmed 标志");
    }

    // ---- FR-258 新增：setPending / readSummary / retainLatestJars ----

    @Test
    void setPendingWritesStateForTrial(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        stageJar(coreDir, "shaDL");

        CoreSelector.setPending(coreDir, "shaDL", 10);

        Properties st = readState(coreDir);
        assertEquals("shaDL", st.getProperty("pendingSha"));
        assertEquals("10", st.getProperty("pendingVersion"));
        assertEquals("false", st.getProperty("pendingTried"), "新 pending 应未 tried");
    }

    @Test
    void readSummaryReflectsState(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        stageJar(coreDir, "shaSEL");
        writeState(coreDir, "selectedSha", "shaSEL", "selectedVersion", "5",
                "failedVersion", "3");

        CoreSelector.StateSummary s = CoreSelector.readSummary(coreDir);

        assertEquals(5, s.selectedVersion);
        assertFalse(s.hasPending);
        assertEquals(3, s.failedVersion);
        assertTrue(s.hasSelectedJar, "selected jar 存在");
    }

    @Test
    void readSummaryEmptyOnFreshDir(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        CoreSelector.StateSummary s = CoreSelector.readSummary(coreDir);
        assertEquals(0, s.selectedVersion);
        assertFalse(s.hasPending);
        assertFalse(s.hasSelectedJar);
    }

    @Test
    void retainLatestJarsDeletesOldestBeyond3(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        // selected=shaA, prev=shaB, 其他三个 shaC/shaD/shaE（按修改时间递增）
        stageJar(coreDir, "shaA");
        Thread.sleep(5);
        stageJar(coreDir, "shaB");
        Thread.sleep(5);
        stageJar(coreDir, "shaC");
        Thread.sleep(5);
        stageJar(coreDir, "shaD");
        Thread.sleep(5);
        stageJar(coreDir, "shaE");
        writeState(coreDir, "selectedSha", "shaA", "selectedVersion", "1",
                "prevSha", "shaB", "prevVersion", "0");

        CoreSelector.retainLatestJars(coreDir, 3);

        File[] remaining = coreDir.listFiles((d, n) -> n.endsWith(".jar"));
        assertEquals(3, remaining.length, "应保留 3 个 jar");
        // shaA(selected) 和 shaB(prev) 应被保留
        boolean hasA = false, hasB = false, hasE = false;
        for (File f : remaining) {
            if (f.getName().equals("shaA.jar")) hasA = true;
            if (f.getName().equals("shaB.jar")) hasB = true;
            if (f.getName().equals("shaE.jar")) hasE = true;
        }
        assertTrue(hasA, "selected jar 应保留");
        assertTrue(hasB, "prev jar 应保留");
        assertTrue(hasE, "最新的其他 jar 应保留");
        // shaC/shaD（较老的其他 jar）应被删除
        assertFalse(new File(coreDir, "shaC.jar").exists(), "较老的其他 jar 应被清理");
        assertFalse(new File(coreDir, "shaD.jar").exists(), "较老的其他 jar 应被清理");
    }

    @Test
    void retainLatestJarsKeepsAllWhenUnder3(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        stageJar(coreDir, "shaA");
        stageJar(coreDir, "shaB");

        CoreSelector.retainLatestJars(coreDir, 3);

        assertEquals(2, coreDir.listFiles((d, n) -> n.endsWith(".jar")).length);
    }
}
