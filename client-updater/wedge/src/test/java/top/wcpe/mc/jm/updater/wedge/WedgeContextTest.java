package top.wcpe.mc.jm.updater.wedge;

import org.junit.jupiter.api.Test;

import java.io.File;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;

/**
 * Wedge.buildContext 透传测试（FR-253，见 ADR-053）。
 *
 * <p>验证楔子把 WedgeConfig 的 signPublicKey / signKeyId 透传到 core 的 ctx，
 * 使 updater-core 运行期可按配置公钥验签（而非只认编译期内置公钥）。
 */
class WedgeContextTest {

    @Test
    void buildContextPassesSignPublicKeyAndKeyId() {
        WedgeConfig config = new WedgeConfig(
                "skyblock-s1", "k_abc", "https://cdn.example.com",
                "updater-core.jar", 120, 0, 30, true,
                "MCowBQYDK2VwAyEAsO7B/k+2++wQtN/L0jpCXCjsGnYV5Sx2eyCk0pDzV0Y=", "k1");
        File wedgeDir = new File("/tmp/wedge");

        Map<String, String> ctx = Wedge.buildContext("/game", config, wedgeDir, 0);

        assertEquals("MCowBQYDK2VwAyEAsO7B/k+2++wQtN/L0jpCXCjsGnYV5Sx2eyCk0pDzV0Y=",
                ctx.get("signPublicKey"), "ctx 应透传 signPublicKey（FR-253）");
        assertEquals("k1", ctx.get("signKeyId"), "ctx 应透传 signKeyId（FR-253）");
    }

    @Test
    void buildContextEmptySignPublicKeyWhenNotConfigured() {
        // 未配置 signPublicKey → ctx 传空串（Core 据此回退内置）。
        WedgeConfig config = new WedgeConfig(
                "c", "k", "https://e", "updater-core.jar", 120, 0, 30, true, null, null);
        File wedgeDir = new File("/tmp/wedge");

        Map<String, String> ctx = Wedge.buildContext("/game", config, wedgeDir, 0);

        assertEquals("", ctx.get("signPublicKey"), "未配置时 ctx signPublicKey 应为空串");
        assertEquals("", ctx.get("signKeyId"), "未配置时 ctx signKeyId 应为空串");
    }
}
