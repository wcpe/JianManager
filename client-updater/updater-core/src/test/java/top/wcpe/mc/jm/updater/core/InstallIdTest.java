package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** installId 本地持久化：首次生成 UUID，后续稳定复用。 */
class InstallIdTest {

    @Test
    void firstRunGeneratesAndPersistsUuid(@TempDir Path gameDir) throws Exception {
        Path stateDir = gameDir.resolve(".jm-updater");

        String installId = InstallId.loadOrCreate(stateDir);

        UUID.fromString(installId);
        Path file = stateDir.resolve("install-id");
        assertTrue(Files.isRegularFile(file), "installId 应持久化到 .jm-updater/install-id");
        assertEquals(installId, new String(Files.readAllBytes(file), StandardCharsets.UTF_8).trim());
    }

    @Test
    void secondRunReusesPersistedUuid(@TempDir Path gameDir) throws Exception {
        Path stateDir = gameDir.resolve(".jm-updater");
        String first = InstallId.loadOrCreate(stateDir);

        String second = InstallId.loadOrCreate(stateDir);

        assertEquals(first, second, "后续启动必须复用同一个 installId");
    }
}
