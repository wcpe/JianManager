package top.wcpe.mc.jm.updater.wedge;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;

class WedgeConfigTest {

    @Test
    void parsesFullConfig() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"skyblock-s1\",\"key\":\"k_abc\","
                        + "\"endpoint\":\"https://cdn.example.com\","
                        + "\"coreEndpoint\":\"https://cdn.example.com/api/v1/client-channels/skyblock-s1/updater-core\","
                        + "\"timeoutSec\":90}");
        assertEquals("skyblock-s1", c.channel);
        assertEquals("k_abc", c.key);
        assertEquals("https://cdn.example.com", c.endpoint);
        assertEquals("https://cdn.example.com/api/v1/client-channels/skyblock-s1/updater-core", c.coreEndpoint);
        assertEquals(90, c.timeoutSec);
    }

    @Test
    void appliesDefaults() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"c\",\"key\":\"k\",\"endpoint\":\"https://e\"}");
        assertEquals(WedgeConfig.DEFAULT_TIMEOUT_SEC, c.timeoutSec);
        assertEquals(WedgeConfig.DEFAULT_BOOT_CONFIRM_SEC, c.bootConfirmSec);
    }

    @Test
    void invalidTimeoutFallsBackToDefault() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"c\",\"endpoint\":\"https://e\",\"timeoutSec\":\"abc\"}");
        assertEquals(WedgeConfig.DEFAULT_TIMEOUT_SEC, c.timeoutSec);
    }

    @Test
    void handlesWhitespaceAndOrdering() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\n  \"endpoint\" : \"https://e\" ,\n  \"channel\":\"c\"\n}");
        assertEquals("c", c.channel);
        assertEquals("https://e", c.endpoint);
    }

    // ---- FR-258：coreEndpoint 解析 ----

    @Test
    void parsesCoreEndpoint() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"c\",\"endpoint\":\"https://e\","
                        + "\"coreEndpoint\":\"https://srv/api/updater-core\"}");
        assertEquals("https://srv/api/updater-core", c.coreEndpoint);
    }

    @Test
    void coreEndpointDefaultsToNullWhenAbsent() {
        WedgeConfig c = WedgeConfig.fromJson("{\"channel\":\"c\",\"endpoint\":\"https://e\"}");
        assertNull(c.coreEndpoint, "未配置 coreEndpoint 应为 null");
    }

    // ---- 旧配置兼容性（FR-256/258 去掉的字段不影响解析）----

    @Test
    void oldConfigFieldsAreIgnoredWithoutError() {
        // 旧配置含 coreJar/coreVersion/signPublicKey/signKeyId → 楔子忽略这些字段，不报错
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"c\",\"endpoint\":\"https://e\","
                        + "\"coreEndpoint\":\"https://srv/api/core\","
                        + "\"coreJar\":\"updater-core.jar\","
                        + "\"coreVersion\":5,"
                        + "\"signPublicKey\":\"MCowBQYDK2VwAyEA...\","
                        + "\"signKeyId\":\"k1\"}");
        assertEquals("c", c.channel);
        assertEquals("https://srv/api/core", c.coreEndpoint);
        // 这些字段已从 WedgeConfig 移除，解析时静默忽略
    }
}
