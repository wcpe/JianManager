package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** 客户端遥测构建（FR-094 / FR-360）：result 映射、字段齐备、归一/分档、JSON 合法。 */
class TelemetryTest {

    @Test
    void resultMapsReturnCode() {
        assertEquals("success", Telemetry.result(Updater.OK));
        assertEquals("fail-static", Telemetry.result(Updater.FAIL_STATIC));
        assertEquals("error", Telemetry.result(-100));
    }

    @Test
    void buildProducesValidJsonWithRequiredAndDiagnosticFields() {
        String json = Telemetry.build("skyblock-s1", Updater.OK, 4, 5, 1234, "1.2.3+abc");
        Object parsed = Json.parse(json);
        assertTrue(parsed instanceof java.util.Map, "遥测应为合法 JSON 对象");
        @SuppressWarnings("unchecked")
        java.util.Map<String, Object> m = (java.util.Map<String, Object>) parsed;
        assertEquals("skyblock-s1", m.get("channel"));
        assertEquals("success", m.get("result"));
        assertEquals(4L, ((Number) m.get("fromVersion")).longValue());
        assertEquals(5L, ((Number) m.get("toVersion")).longValue());
        assertEquals(1234L, ((Number) m.get("durationMs")).longValue());
        assertEquals(Boolean.TRUE, m.get("bootSuccess"));
        assertEquals("1.2.3+abc", m.get("coreVersion"));
        assertTrue(m.containsKey("os") && m.containsKey("arch"), "应含 required 环境字段");
        assertTrue(m.containsKey("javaVersion") && m.containsKey("javaVendor"), "应含 diagnostic Java 字段");
        assertTrue(m.containsKey("locale") && m.containsKey("timezone") && m.containsKey("memoryTier"),
                "应含 diagnostic 诊断字段");
        assertTrue(m.containsKey("launcher"), "应含 launcher");
    }

    @Test
    void failStaticReportsNotBootSuccess() {
        String json = Telemetry.build("ch", Updater.FAIL_STATIC, 5, 5, 10, "core-x");
        @SuppressWarnings("unchecked")
        java.util.Map<String, Object> m = (java.util.Map<String, Object>) Json.parse(json);
        assertEquals("fail-static", m.get("result"));
        assertEquals(Boolean.FALSE, m.get("bootSuccess"));
        assertEquals("core-x", m.get("coreVersion"));
    }

    @Test
    void normalizeOsAndArch() {
        assertEquals("windows", Telemetry.normalizeOs("Windows 11"));
        assertEquals("macos", Telemetry.normalizeOs("Mac OS X"));
        assertEquals("linux", Telemetry.normalizeOs("Linux"));
        assertEquals("unknown", Telemetry.normalizeOs(""));
        assertEquals("amd64", Telemetry.normalizeArch("amd64"));
        assertEquals("amd64", Telemetry.normalizeArch("x86_64"));
        assertEquals("arm64", Telemetry.normalizeArch("aarch64"));
        assertEquals("unknown", Telemetry.normalizeArch(""));
    }

    @Test
    void memoryTierBuckets() {
        assertEquals("unknown", Telemetry.memoryTier(0));
        assertEquals("le2g", Telemetry.memoryTier(1024L * 1024 * 1024));
        assertEquals("le4g", Telemetry.memoryTier(3L * 1024 * 1024 * 1024));
        assertEquals("le8g", Telemetry.memoryTier(8L * 1024 * 1024 * 1024));
        assertEquals("gt8g", Telemetry.memoryTier(12L * 1024 * 1024 * 1024));
    }
}
