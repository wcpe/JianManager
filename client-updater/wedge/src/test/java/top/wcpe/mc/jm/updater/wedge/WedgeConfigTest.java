package top.wcpe.mc.jm.updater.wedge;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

class WedgeConfigTest {

    @Test
    void parsesFullConfig() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"skyblock-s1\",\"key\":\"k_abc\","
                        + "\"endpoint\":\"https://cdn.example.com/api/v1\","
                        + "\"timeoutSec\":90}");
        assertEquals("skyblock-s1", c.channel);
        assertEquals("k_abc", c.key);
        assertEquals("https://cdn.example.com/api/v1", c.endpoint);
        assertEquals("https://cdn.example.com/api/v1/client-channels/skyblock-s1/updater-core", c.updaterCoreEndpoint());
        assertEquals(90, c.timeoutSec);
    }

    @Test
    void appliesDefaults() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"c\",\"key\":\"k\",\"endpoint\":\"https://e/api/v1\"}");
        assertEquals(WedgeConfig.DEFAULT_TIMEOUT_SEC, c.timeoutSec);
        assertEquals(WedgeConfig.DEFAULT_BOOT_CONFIRM_SEC, c.bootConfirmSec);
    }

    @Test
    void invalidTimeoutFallsBackToDefault() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"c\",\"endpoint\":\"https://e/api/v1\",\"timeoutSec\":\"abc\"}");
        assertEquals(WedgeConfig.DEFAULT_TIMEOUT_SEC, c.timeoutSec);
    }

    @Test
    void handlesWhitespaceAndOrdering() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\n  \"endpoint\" : \"https://e/api/v1\" ,\n  \"channel\":\"c\"\n}");
        assertEquals("c", c.channel);
        assertEquals("https://e/api/v1", c.endpoint);
    }

    @Test
    void derivesCoreEndpointFromApiRootEndpoint() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"sky block\",\"endpoint\":\"https://e/api/v1/\"}");
        assertEquals("https://e/api/v1", c.endpoint);
        assertEquals("https://e/api/v1/client-channels/sky%20block/updater-core", c.updaterCoreEndpoint());
    }

    @Test
    void rejectsCoreEndpointField() {
        assertThrows(IllegalArgumentException.class, () -> WedgeConfig.fromJson(
                "{\"channel\":\"c\",\"endpoint\":\"https://e/api/v1\","
                        + "\"coreEndpoint\":\"https://srv/api/updater-core\"}"));
    }

    @Test
    void rejectsEndpointWithSuffix() {
        assertThrows(IllegalArgumentException.class, () -> WedgeConfig.fromJson(
                "{\"channel\":\"c\",\"endpoint\":\"https://e/api/v1/client-channels/c/manifest\"}"));
    }

    // ---- 旧配置兼容性（FR-256 去掉的字段不影响解析）----

    @Test
    void oldConfigFieldsAreIgnoredWithoutError() {
        // 旧配置含 coreJar/coreVersion/signPublicKey/signKeyId → 楔子忽略这些字段，不报错
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"c\",\"endpoint\":\"https://e/api/v1\","
                        + "\"coreJar\":\"updater-core.jar\","
                        + "\"coreVersion\":5,"
                        + "\"signPublicKey\":\"MCowBQYDK2VwAyEA...\","
                        + "\"signKeyId\":\"k1\"}");
        assertEquals("c", c.channel);
        assertEquals("https://e/api/v1/client-channels/c/updater-core", c.updaterCoreEndpoint());
        // 这些字段已从 WedgeConfig 移除，解析时静默忽略
    }
}
