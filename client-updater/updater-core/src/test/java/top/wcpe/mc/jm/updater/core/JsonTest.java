package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

class JsonTest {

    @Test
    void parsesNestedObject() {
        Object v = Json.parse("{\"a\":1,\"b\":[true,null,\"x\"],\"c\":{\"d\":2.5}}");
        assertTrue(v instanceof Map);
        @SuppressWarnings("unchecked")
        Map<String, Object> m = (Map<String, Object>) v;
        assertEquals(1L, m.get("a"));
        @SuppressWarnings("unchecked")
        List<Object> b = (List<Object>) m.get("b");
        assertEquals(Boolean.TRUE, b.get(0));
        assertNull(b.get(1));
        assertEquals("x", b.get(2));
    }

    @Test
    void canonicalSortsKeysRecursively() {
        Object v = Json.parse("{\"b\":1,\"a\":{\"z\":2,\"y\":3}}");
        // 键升序排序：a 在 b 前，y 在 z 前。
        assertEquals("{\"a\":{\"y\":3,\"z\":2},\"b\":1}", Json.canonical(v));
    }

    @Test
    void canonicalIsStableRegardlessOfInputOrder() {
        String a = Json.canonical(Json.parse("{\"x\":1,\"y\":2}"));
        String b = Json.canonical(Json.parse("{\"y\":2,\"x\":1}"));
        assertEquals(a, b);
    }

    @Test
    void canonicalIntegersHaveNoDecimalPoint() {
        // version 等整数必须最简形式（契约 §3 数字最简）。
        assertEquals("{\"version\":42}", Json.canonical(Json.parse("{\"version\":42}")));
    }

    @Test
    void rejectsTrailingGarbage() {
        assertThrows(Json.JsonException.class, () -> Json.parse("{\"a\":1} trailing"));
    }

    @Test
    void canonicalEscapesStrings() {
        assertEquals("{\"k\":\"a\\\"b\\n\"}",
                Json.canonical(Json.parse("{\"k\":\"a\\\"b\\n\"}")));
    }

    @Test
    void parsesEmptyContainers() {
        assertTrue(((Map<?, ?>) Json.parse("{}")).isEmpty());
        assertTrue(((List<?>) Json.parse("[]")).isEmpty());
    }

    @Test
    void distinguishesLongFromDouble() {
        Map<?, ?> m = (Map<?, ?>) Json.parse("{\"i\":10,\"d\":10.5}");
        assertTrue(m.get("i") instanceof Long);
        assertTrue(m.get("d") instanceof Double);
        assertFalse(m.get("d") instanceof Long);
    }
}
