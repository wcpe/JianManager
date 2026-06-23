package top.jm.updater.wedge;

import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

class MiniJsonTest {

    @Test
    void parsesStringsAndNumbers() {
        Map<String, String> m = MiniJson.parseFlatObject(
                "{\"a\":\"hello\",\"b\":120,\"c\":\"with space\"}");
        assertEquals("hello", m.get("a"));
        assertEquals("120", m.get("b"));
        assertEquals("with space", m.get("c"));
    }

    @Test
    void handlesEscapesInValues() {
        Map<String, String> m = MiniJson.parseFlatObject(
                "{\"path\":\"C:\\\\Games\\\\Pack\"}");
        assertEquals("C:\\Games\\Pack", m.get("path"));
    }

    @Test
    void parsesEmptyObject() {
        assertEquals(0, MiniJson.parseFlatObject("{}").size());
    }

    @Test
    void rejectsNonObject() {
        assertThrows(IllegalArgumentException.class, () -> MiniJson.parseFlatObject("[1,2]"));
    }
}
