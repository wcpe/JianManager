package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** 运行态心跳体（FR-265）：只含粗粒度运行态，不含密钥与机器码。 */
class RuntimeHeartbeatTest {

    @Test
    void buildProducesRuntimeFieldsWithoutSecrets() {
        String json = RuntimeHeartbeat.build("12", 7);
        @SuppressWarnings("unchecked")
        Map<String, Object> body = (Map<String, Object>) Json.parse(json);

        assertEquals("12", body.get("coreVersion"));
        assertEquals(7L, ((Number) body.get("localVersion")).longValue());
        assertTrue(body.containsKey("platform"));
        assertTrue(body.containsKey("javaVersion"));
        assertTrue(body.containsKey("launcher"));
        assertTrue(!body.containsKey("key"), "心跳体不得包含拉取密钥");
        assertTrue(!body.containsKey("machineId"), "机器码只走请求头，不入 body");
    }
}
