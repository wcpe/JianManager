package top.jm.updater.core;

import org.junit.jupiter.api.Test;

import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

class PathRulesTest {

    @Test
    void acceptsNormalManagedPath() {
        assertTrue(PathRules.isSafeRelative("mods/foo.jar"));
        assertTrue(PathRules.isSafeRelative("config/sub/dir/x.toml"));
    }

    @Test
    void rejectsDotDotEscape() {
        assertFalse(PathRules.isSafeRelative("../evil.jar"));
        assertFalse(PathRules.isSafeRelative("mods/../../etc/passwd"));
    }

    @Test
    void rejectsAbsoluteAndDriveLetter() {
        assertFalse(PathRules.isSafeRelative("/etc/passwd"));
        assertFalse(PathRules.isSafeRelative("C:/Windows/system32"));
    }

    @Test
    void rejectsPlayerZonePaths() {
        // 即使语法合法，玩家区路径也不算「安全可写」（契约 §6.4 永不碰）。
        assertTrue(PathRules.isPlayerZone("saves/world/level.dat"));
        assertTrue(PathRules.isPlayerZone("options.txt"));
        assertTrue(PathRules.isPlayerZone("screenshots/2026.png"));
        assertTrue(PathRules.isPlayerZone("logs/latest.log"));
        assertTrue(PathRules.isPlayerZone("crash-reports/crash.txt"));
        assertFalse(PathRules.isSafeRelative("saves/world/level.dat"));
    }

    @Test
    void modsAreNotPlayerZone() {
        assertFalse(PathRules.isPlayerZone("mods/foo.jar"));
        assertFalse(PathRules.isPlayerZone("config/options.txt")); // 仅根 options.txt 是玩家区
    }

    @Test
    void underManagedDetection() {
        List<String> managed = Arrays.asList("mods", "config");
        assertTrue(PathRules.isUnderManaged("mods/foo.jar", managed));
        assertTrue(PathRules.isUnderManaged("config/a/b.toml", managed));
        assertFalse(PathRules.isUnderManaged("resourcepacks/x.zip", managed));
        assertFalse(PathRules.isUnderManaged("saves/world", managed));
        // 防前缀误判：modsxyz 不应算在 mods 下。
        assertFalse(PathRules.isUnderManaged("modsxyz/foo.jar", managed));
    }

    @Test
    void resolveSafeRejectsEscape() {
        Path game = Paths.get(System.getProperty("java.io.tmpdir")).resolve("jm-game-test");
        assertThrows(SecurityException.class,
                () -> PathRules.resolveSafe(game, "../outside.txt"));
    }

    @Test
    void resolveSafeAcceptsInside() {
        Path game = Paths.get(System.getProperty("java.io.tmpdir")).resolve("jm-game-test");
        Path resolved = PathRules.resolveSafe(game, "mods/foo.jar");
        assertTrue(resolved.startsWith(game.toAbsolutePath().normalize()));
    }
}
