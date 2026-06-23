package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** core 自更新选择状态持久化（FR-091）：往返、原子落盘、损坏容错。 */
class CoreSelectStoreTest {

    @Test
    void emptyWhenAbsent(@TempDir Path coreDir) {
        CoreSelectStore s = CoreSelectStore.load(coreDir);
        assertFalse(s.hasPending());
        assertEquals(-1, s.pendingVersion());
        assertEquals(0, s.selectedVersion());
    }

    @Test
    void setPendingRoundTrips(@TempDir Path coreDir) throws Exception {
        CoreSelectStore s = CoreSelectStore.load(coreDir);
        s.setPending("abc123", 6);
        s.store();

        CoreSelectStore reloaded = CoreSelectStore.load(coreDir);
        assertTrue(reloaded.hasPending());
        assertEquals("abc123", reloaded.pendingSha());
        assertEquals(6, reloaded.pendingVersion());
        assertTrue(Files.isRegularFile(coreDir.resolve("state.properties")));
    }

    @Test
    void corruptStateTreatedAsEmpty(@TempDir Path coreDir) throws Exception {
        Files.write(coreDir.resolve("state.properties"), new byte[] { 0, 1, 2, (byte) 0xff });
        // Properties.load 对任意字节宽容，这里只验不抛、按可用状态继续。
        CoreSelectStore s = CoreSelectStore.load(coreDir);
        assertEquals(-1, s.pendingVersion());
    }
}
