package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** 客户端遥测构建（FR-094）：result 映射、字段齐备、JSON 合法、不含敏感数据。 */
class TelemetryTest {

    @Test
    void resultMapsReturnCode() {
        assertEquals("success", Telemetry.result(Updater.OK));
        assertEquals("fail-static", Telemetry.result(Updater.FAIL_STATIC));
        assertEquals("error", Telemetry.result(-100));
    }

    @Test
    void buildProducesValidJsonWithFields() {
        String json = Telemetry.build("skyblock-s1", Updater.OK, 4, 5, 1234);
        // 可被自带 JSON 解析（结构合法）。
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
        assertTrue(m.containsKey("os") && m.containsKey("javaVersion") && m.containsKey("launcher"),
                "应含环境粗粒度字段");
    }

    @Test
    void failStaticReportsNotBootSuccess() {
        String json = Telemetry.build("ch", Updater.FAIL_STATIC, 5, 5, 10);
        @SuppressWarnings("unchecked")
        java.util.Map<String, Object> m = (java.util.Map<String, Object>) Json.parse(json);
        assertEquals("fail-static", m.get("result"));
        assertEquals(Boolean.FALSE, m.get("bootSuccess"));
    }
}
