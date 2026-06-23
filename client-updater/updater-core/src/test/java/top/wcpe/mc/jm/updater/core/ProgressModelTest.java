package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** 进度模型（FR-099）：完成比例 / 滑窗速度 / ETA / 边界，注入时间戳确定性验证。 */
class ProgressModelTest {

    @Test
    void fractionZeroWhenTotalUnknown() {
        ProgressModel m = new ProgressModel();
        assertEquals(0.0, m.fraction(), 1e-9);
        m.advance(500, 1000); // 无总量时推进也不应 NaN/越界
        assertEquals(0.0, m.fraction(), 1e-9);
    }

    @Test
    void fractionAndClamp() {
        ProgressModel m = new ProgressModel();
        m.addTotal(1000);
        m.advance(250, 1000);
        assertEquals(0.25, m.fraction(), 1e-9);
        m.advance(2000, 1100); // 超过总量
        assertEquals(1.0, m.fraction(), 1e-9, "完成比例应钳制到 1.0");
    }

    @Test
    void addTotalAccumulates() {
        ProgressModel m = new ProgressModel();
        m.addTotal(1000);
        m.addTotal(500);
        m.advance(750, 1000);
        assertEquals(0.5, m.fraction(), 1e-9);
    }

    @Test
    void speedFromWindowEndpoints() {
        ProgressModel m = new ProgressModel();
        m.addTotal(3_000_000);
        m.advance(1_000_000, 1000);
        m.advance(1_000_000, 2000);
        // (2e6-1e6) 字节 / (2000-1000) ms = 1e6 B/s
        assertEquals(1_000_000L, m.speedBytesPerSec(2000));
    }

    @Test
    void etaFromRemainingOverSpeed() {
        ProgressModel m = new ProgressModel();
        m.addTotal(3_000_000);
        m.advance(1_000_000, 1000);
        m.advance(1_000_000, 2000); // done=2e6, speed=1e6/s, remaining=1e6
        assertEquals(1L, m.etaSeconds(2000));
    }

    @Test
    void speedZeroWithSingleSample() {
        ProgressModel m = new ProgressModel();
        m.addTotal(1000);
        m.advance(100, 1000);
        assertEquals(0L, m.speedBytesPerSec(1000));
        assertEquals(-1L, m.etaSeconds(1000), "速度未知时 ETA = -1");
    }

    @Test
    void staleSamplesEvictedToZeroSpeed() {
        ProgressModel m = new ProgressModel();
        m.addTotal(3_000_000);
        m.advance(1_000_000, 1000);
        m.advance(1_000_000, 2000);
        assertTrue(m.speedBytesPerSec(2000) > 0);
        // 远超窗口（>3s 无新样本）→ 旧样本淘汰、速度回 0（停滞）
        assertEquals(0L, m.speedBytesPerSec(10_000));
    }

    @Test
    void snapToCompleteFillsRemainder() {
        ProgressModel m = new ProgressModel();
        m.addTotal(1000);
        m.advance(900, 1000); // 预估误差残留 100B
        assertTrue(m.fraction() < 1.0);
        m.snapToComplete();
        assertEquals(1.0, m.fraction(), 1e-9);
        assertEquals(1000L, m.doneBytes());
    }

    @Test
    void currentFileTracked() {
        ProgressModel m = new ProgressModel();
        m.setCurrentFile("mods/foo.jar");
        assertEquals("mods/foo.jar", m.currentFile());
        m.setCurrentFile(null);
        assertEquals("", m.currentFile(), "null 文件名归一为空串");
    }
}
