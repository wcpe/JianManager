package top.jm.updater.wedge;

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
 * 故用占位文件模拟已暂存的 core jar，覆盖 promote/rollback/trial/selected/bundled/手动回退/看门狗。
 */
class CoreSelectorTest {

    private File bundled(Path tmp) throws Exception {
        File f = tmp.resolve("bundled-core.jar").toFile();
        Files.write(f.toPath(), new byte[] { 9 });
        return f;
    }

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
    void freshLoadsBundled(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        File bundled = bundled(tmp);
        CoreSelector.Selection sel = CoreSelector.select(coreDir, bundled, 0);
        assertEquals(bundled, sel.coreJar);
        assertFalse(sel.trial);
    }

    @Test
    void firstTrialLoadsPendingAndMarksTried(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        stageJar(coreDir, "shaPEND");
        writeState(coreDir, "pendingSha", "shaPEND", "pendingVersion", "6", "pendingTried", "false");

        CoreSelector.Selection sel = CoreSelector.select(coreDir, bundled(tmp), 0);

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

        CoreSelector.Selection sel = CoreSelector.select(coreDir, bundled(tmp), 0);

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

        CoreSelector.Selection sel = CoreSelector.select(coreDir, bundled(tmp), 0);

        assertEquals(new File(coreDir, "shaOLD.jar"), sel.coreJar, "未确认应回退到上一可用 N-1");
        assertEquals(5, sel.coreVersion);
        assertFalse(sel.trial);
        assertNull(readState(coreDir).getProperty("pendingSha"), "回退后弃 pending");
    }

    @Test
    void triedUnconfirmedNoSelectedRollsBackToBundled(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        stageJar(coreDir, "shaNEW");
        writeState(coreDir, "pendingSha", "shaNEW", "pendingVersion", "6", "pendingTried", "true");
        File bundled = bundled(tmp);

        CoreSelector.Selection sel = CoreSelector.select(coreDir, bundled, 0);

        assertEquals(bundled, sel.coreJar, "无 N-1 时回退内置 bundled");
        assertFalse(sel.trial);
    }

    @Test
    void selectedLoadsWhenNoPending(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        stageJar(coreDir, "shaSEL");
        writeState(coreDir, "selectedSha", "shaSEL", "selectedVersion", "7");

        CoreSelector.Selection sel = CoreSelector.select(coreDir, bundled(tmp), 0);

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

        CoreSelector.Selection sel = CoreSelector.select(coreDir, bundled(tmp), 0);

        assertEquals(new File(coreDir, "shaPRV.jar"), sel.coreJar, "手动回退应回 prev");
        assertEquals(7, sel.coreVersion);
        Properties st = readState(coreDir);
        assertEquals("shaPRV", st.getProperty("selectedSha"));
        assertNull(st.getProperty("pendingSha"), "手动回退弃 pending");
        assertFalse(new File(coreDir, "rollback.flag").exists(), "rollback.flag 应被消费删除");
    }

    @Test
    void selectIsFailOpenOnGarbageState(@TempDir Path tmp) throws Exception {
        File coreDir = tmp.resolve(".jm-updater/core").toFile();
        coreDir.mkdirs();
        // selected 指向不存在的 jar → 应回退 bundled（不抛）。
        writeState(coreDir, "selectedSha", "missing", "selectedVersion", "5");
        File bundled = bundled(tmp);
        CoreSelector.Selection sel = CoreSelector.select(coreDir, bundled, 0);
        assertEquals(bundled, sel.coreJar, "selected jar 缺失应回退 bundled");
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
}
