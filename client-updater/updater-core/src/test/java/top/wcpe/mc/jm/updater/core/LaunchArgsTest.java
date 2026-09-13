package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** 启动命令行采集（FR-426 增补）：--username 解析、accessToken 脱敏、玩家名兜底。 */
class LaunchArgsTest {

    private static final String FABRIC = "net.fabricmc.loader.impl.launch.knot.KnotClient"
            + " --username Amazon2 --uuid abc-123 --accessToken ghp_SECRET --gameDir /tmp/g";

    @Test
    void parseUsernameExtractsLauncherInjectedName() {
        assertEquals("Amazon2", LaunchArgs.parseUsername(FABRIC));
    }

    @Test
    void parseUsernameSupportsEqualsForm() {
        assertEquals("Steve", LaunchArgs.parseUsername("net.minecraft.client.main.Main --username=Steve --version 1.20"));
    }

    @Test
    void parseUsernameReturnsEmptyWhenAbsent() {
        assertEquals("", LaunchArgs.parseUsername("net.minecraft.client.main.Main --version 1.20"));
        assertEquals("", LaunchArgs.parseUsername(""));
        assertEquals("", LaunchArgs.parseUsername(null));
    }

    @Test
    void redactMasksAccessTokenValue() {
        String r = LaunchArgs.redact(FABRIC);
        assertFalse(r.contains("ghp_SECRET"), "accessToken 值必须脱敏");
        assertTrue(r.contains("--accessToken ***"));
        assertTrue(r.contains("--username Amazon2"), "username 保留");
        assertTrue(r.contains("--uuid abc-123"), "uuid 保留（诊断价值）");
        assertTrue(r.contains("--gameDir /tmp/g"), "其余参数原样保留");
    }

    @Test
    void redactMasksEqualsFormAndHandlesEmpty() {
        assertTrue(LaunchArgs.redact("Main --accessToken=c9d9").contains("--accessToken=***"));
        assertEquals("", LaunchArgs.redact(""));
        assertEquals("", LaunchArgs.redact(null));
    }

    @Test
    void resolvePlayerNamePrefersExplicitThenUsername() {
        assertEquals("Explicit", LaunchArgs.resolvePlayerName("Explicit", FABRIC));
        assertEquals("Amazon2", LaunchArgs.resolvePlayerName("", FABRIC));
        assertEquals("Amazon2", LaunchArgs.resolvePlayerName(null, FABRIC));
        assertEquals("", LaunchArgs.resolvePlayerName("  ", "Main --version 1.20"));
    }
}
