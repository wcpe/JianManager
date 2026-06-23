package top.jm.updater.core;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** 客户端机器码（FR-092）：格式、稳定（持久化复用 + 容错）、不可逆、never-throw。 */
class MachineIdTest {

    private boolean isHex64(String s) {
        return s != null && s.matches("[0-9a-f]{64}");
    }

    @Test
    void computeIsHex64AndStable() {
        String a = MachineId.compute();
        String b = MachineId.compute();
        assertTrue(isHex64(a), "机器码应为 64 位十六进制（SHA-256），实际 " + a);
        assertEquals(a, b, "同机两次计算应一致");
    }

    @Test
    void getPersistsAndReuses(@TempDir Path tmp) throws Exception {
        Path file = tmp.resolve("machine-id");
        String id1 = MachineId.get(file);
        assertTrue(isHex64(id1));
        assertTrue(Files.isRegularFile(file), "应持久化机器码");
        // 第二次（模拟下次启动）应读持久值，与首次一致。
        String id2 = MachineId.get(file);
        assertEquals(id1, id2, "持久化后应稳定复用");
    }

    @Test
    void reusesPersistedEvenIfHardwareChanged(@TempDir Path tmp) throws Exception {
        // 持久化一个已知机器码（模拟历史身份）→ 即使本机特征不同也应原样返回（部分变化容错）。
        Path file = tmp.resolve("machine-id");
        String known = "a".repeat(64);
        Files.createDirectories(file.getParent());
        Files.write(file, known.getBytes(StandardCharsets.UTF_8));
        assertEquals(known, MachineId.get(file), "已持久化身份应稳定复用，硬件变化不改之");
    }

    @Test
    void ignoresCorruptPersistedAndRecomputes(@TempDir Path tmp) throws Exception {
        Path file = tmp.resolve("machine-id");
        Files.write(file, "not-a-valid-hash".getBytes(StandardCharsets.UTF_8));
        String id = MachineId.get(file);
        assertTrue(isHex64(id), "损坏持久值应被忽略并重算出合法机器码");
    }

    @Test
    void doesNotLeakRawSignals() {
        String id = MachineId.compute();
        String user = System.getProperty("user.name", "");
        if (!user.isEmpty()) {
            assertTrue(id.indexOf(user) < 0, "机器码为不可逆 hash，不得含原始 user.name");
        }
        assertNotEquals(System.getProperty("os.name", ""), id);
    }

    @Test
    void neverThrowsWhenPersistUnwritable(@TempDir Path tmp) throws Exception {
        // 父路径是一个文件 → createDirectories 失败 → 持久化失败，但 get 仍应返回合法机器码。
        Path blocker = tmp.resolve("blocker");
        Files.write(blocker, new byte[] { 1 });
        Path unwritable = blocker.resolve("machine-id");
        String id = MachineId.get(unwritable);
        assertTrue(isHex64(id), "持久化不可写时仍应返回合法机器码（best-effort 不抛）");
    }
}
