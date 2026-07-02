package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.UUID;

/** 安装实例 ID：首次生成 UUID，并持久化在 .jm-updater/install-id。 */
final class InstallId {

    private InstallId() {
    }

    static String loadOrCreate(Path stateDir) throws IOException {
        Path file = stateDir.resolve("install-id");
        if (Files.isRegularFile(file)) {
            String existing = new String(Files.readAllBytes(file), StandardCharsets.UTF_8).trim();
            if (isUuid(existing)) {
                return existing;
            }
        }
        String generated = UUID.randomUUID().toString();
        persist(file, generated);
        return generated;
    }

    private static void persist(Path file, String installId) throws IOException {
        Files.createDirectories(file.getParent());
        Path tmp = file.resolveSibling("install-id.tmp");
        Files.write(tmp, installId.getBytes(StandardCharsets.UTF_8));
        try {
            Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
        } catch (java.nio.file.AtomicMoveNotSupportedException e) {
            Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING);
        }
    }

    private static boolean isUuid(String value) {
        try {
            UUID.fromString(value);
            return true;
        } catch (RuntimeException e) {
            return false;
        }
    }
}
