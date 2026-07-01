package top.wcpe.mc.jm.updater.wedge;

import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;

/**
 * Wedge.buildContext 透传测试（§2.5.2 接口契约冻结）。
 *
 * <p>验证楔子把 jm-updater.json 原文透传到 core 的 ctx（key=configJson），
 * 以及 ctx 固定 key 集合正确。楔子代码冻结后此 ctx 格式永久固定。
 */
class WedgeContextTest {

    @Test
    void buildContextPassesConfigJson() {
        WedgeConfig config = new WedgeConfig(
                "skyblock-s1", "k_abc", "https://cdn.example.com",
                "https://srv/api/core", 120, 30, true);
        String json = "{\"channel\":\"skyblock-s1\",\"coreEndpoint\":\"https://srv/api/core\"}";

        Map<String, String> ctx = Wedge.buildContext("/game", config, 5, json);

        assertEquals(json, ctx.get("configJson"), "ctx 应透传 jm-updater.json 原文（§2.5.2）");
    }

    @Test
    void buildContextContainsAllFrozenKeys() {
        WedgeConfig config = new WedgeConfig(
                "c", "k", "https://e", "https://srv/core", 90, 30, true);

        Map<String, String> ctx = Wedge.buildContext("/game", config, 7, "{\"channel\":\"c\"}");

        // §2.5.2 冻结 key 集合
        assertEquals("/game", ctx.get("gameDir"));
        assertEquals("c", ctx.get("channel"));
        assertEquals("k", ctx.get("key"));
        assertEquals("https://e", ctx.get("endpoint"));
        assertEquals("7", ctx.get("coreVersion"));
        assertEquals("true", ctx.get("telemetry"));
        assertEquals("90", ctx.get("timeoutSec"));
        assertEquals("{\"channel\":\"c\"}", ctx.get("configJson"));
    }

    @Test
    void buildContextDoesNotContainRemovedKeys() {
        WedgeConfig config = new WedgeConfig(
                "c", "k", "https://e", "https://srv/core", 90, 30, true);

        Map<String, String> ctx = Wedge.buildContext("/game", config, 0, "{}");

        // FR-256 移除的字段不应出现在 ctx
        assertFalse(ctx.containsKey("signPublicKey"), "ctx 不应含 signPublicKey（FR-256 已移除）");
        assertFalse(ctx.containsKey("signKeyId"), "ctx 不应含 signKeyId（FR-256 已移除）");
    }

    @Test
    void buildContextEmptyConfigJsonWhenNull() {
        WedgeConfig config = new WedgeConfig(
                "c", "k", "https://e", "https://srv/core", 90, 30, true);

        Map<String, String> ctx = Wedge.buildContext("/game", config, 0, null);

        assertEquals("", ctx.get("configJson"), "configJson 为 null 时应传空串");
    }
}
