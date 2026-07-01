package top.wcpe.mc.jm.updater.wedge;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class WedgeConfigTest {

    @Test
    void parsesFullConfig() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"skyblock-s1\",\"key\":\"k_abc\","
                        + "\"endpoint\":\"https://cdn.example.com\","
                        + "\"coreJar\":\"updater-core.jar\",\"timeoutSec\":90}");
        assertEquals("skyblock-s1", c.channel);
        assertEquals("k_abc", c.key);
        assertEquals("https://cdn.example.com", c.endpoint);
        assertEquals("updater-core.jar", c.coreJar);
        assertEquals(90, c.timeoutSec);
    }

    @Test
    void appliesDefaults() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"c\",\"key\":\"k\",\"endpoint\":\"https://e\"}");
        assertEquals(WedgeConfig.DEFAULT_CORE_JAR, c.coreJar);
        assertEquals(WedgeConfig.DEFAULT_TIMEOUT_SEC, c.timeoutSec);
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

    // ---- FR-253：signPublicKey / signKeyId 解析 ----

    @Test
    void parsesSignPublicKeyAndKeyId() {
        WedgeConfig c = WedgeConfig.fromJson(
                "{\"channel\":\"c\",\"endpoint\":\"https://e\","
                        + "\"signPublicKey\":\"MCowBQYDK2VwAyEAsO7B/k+2++wQtN/L0jpCXCjsGnYV5Sx2eyCk0pDzV0Y=\","
                        + "\"signKeyId\":\"k1\"}");
        assertEquals("MCowBQYDK2VwAyEAsO7B/k+2++wQtN/L0jpCXCjsGnYV5Sx2eyCk0pDzV0Y=", c.signPublicKey);
        assertEquals("k1", c.signKeyId);
    }

    @Test
    void signPublicKeyDefaultsToNullWhenAbsent() {
        WedgeConfig c = WedgeConfig.fromJson("{\"channel\":\"c\",\"endpoint\":\"https://e\"}");
        assertEquals(null, c.signPublicKey, "未配置 signPublicKey 应为 null（回退内置）");
        assertEquals(null, c.signKeyId, "未配置 signKeyId 应为 null");
    }
}
