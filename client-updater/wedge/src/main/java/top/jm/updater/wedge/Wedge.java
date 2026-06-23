package top.jm.updater.wedge;

import java.io.File;
import java.lang.instrument.Instrumentation;
import java.net.URISyntaxException;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * JM 客户端 OTA 楔子（javaagent）。
 *
 * <p>经启动器 JVM 参数 {@code -javaagent:wedge.jar=<gameDir>} 注入；premain 自定位、
 * 动态加载 updater-core 并调用其 {@code run} 入口、同步等待，更新失败 fail-static、
 * 楔子自身任何异常 fail-open——绝不挡住游戏启动（ADR-021 决策 6）。
 *
 * <p>协议见 {@code docs/specs/client-distribution/contract.md} §6。
 */
public final class Wedge {

    private Wedge() {
    }

    /**
     * javaagent 入口。{@code agentArgs} = gameDir（契约 §6.1）。全程 fail-open。
     */
    public static void premain(String agentArgs, Instrumentation inst) {
        Messages msg = safeMessages();
        try {
            premainInternal(agentArgs, msg);
        } catch (Throwable t) {
            // 楔子唯一允许的失败模式：放行游戏（ADR-021 决策 6 / FR-089）。
            // 自定位失败 / core 缺失 / 加载错误 / 任意异常都到此 → 放行。
            System.err.println(msg.get(Messages.Key.WEDGE_FAILOPEN) + " (" + t + ")");
        }
    }

    private static void premainInternal(String agentArgs, Messages msg) throws Exception {
        System.out.println(msg.get(Messages.Key.STARTING));

        // 1. 自定位：getCodeSource().getLocation() → wedge.jar 绝对路径 → 同目录（契约 §6.2）。
        File wedgeDir = locateWedgeDir();

        // 2. 读同目录 jm-updater.json（channel/key/endpoint/coreJar/timeoutSec）。
        File configFile = new File(wedgeDir, "jm-updater.json");
        if (!configFile.isFile()) {
            // 无配置 = 未启用 OTA：fail-open 放行（不挡游戏）。
            System.err.println("[JM Updater] 未找到 jm-updater.json，跳过更新直接启动。");
            return;
        }
        WedgeConfig config = WedgeConfig.load(configFile);

        // 3. 解析 gameDir：agentArgs 优先，兜底 sun.java.command 的 --gameDir（契约 §6.1）。
        String gameDir = GameDirResolver.resolve(agentArgs, System.getProperty("sun.java.command"));
        if (gameDir == null || gameDir.trim().isEmpty()) {
            // 末位兜底：用楔子所在目录推断（基础包内 wedge 常与 gameDir 同处或其下）。
            gameDir = wedgeDir.getAbsolutePath();
        }

        // 4. URLClassLoader 内存加载 updater-core.jar → 反射 Core.run(ctx) → 同步等待 + 超时（契约 §6.3）。
        File coreJar = new File(wedgeDir, config.coreJar);
        Map<String, String> ctx = buildContext(gameDir, config, wedgeDir);
        int result = CoreLoader.loadAndRun(coreJar, ctx, config.timeoutSec);

        // 5. 处理结果：0=放行；超时/非 0=fail-static 放行带本地版本 + 提示（契约 §6.3）。
        if (result == CoreLoader.RESULT_OK) {
            System.out.println(msg.get(Messages.Key.UPDATE_OK));
        } else if (result == CoreLoader.RESULT_TIMEOUT) {
            System.err.println(msg.get(Messages.Key.UPDATE_TIMEOUT));
        } else {
            System.err.println(msg.get(Messages.Key.UPDATE_FAILED_STATIC));
        }
        // 无论何种结果都 return（放行游戏）——楔子从不因更新结果挡启动。
    }

    /** 自定位楔子所在目录（契约 §6.2）。 */
    private static File locateWedgeDir() throws URISyntaxException {
        File jar = new File(Wedge.class.getProtectionDomain()
                .getCodeSource().getLocation().toURI());
        File dir = jar.isFile() ? jar.getParentFile() : jar;
        return dir != null ? dir : new File(".");
    }

    /** 组装传给 core 的 ctx（契约 §6.3）。 */
    private static Map<String, String> buildContext(String gameDir, WedgeConfig config, File wedgeDir) {
        Map<String, String> ctx = new LinkedHashMap<String, String>();
        ctx.put("gameDir", gameDir);
        ctx.put("channel", nullToEmpty(config.channel));
        ctx.put("key", nullToEmpty(config.key));
        ctx.put("endpoint", nullToEmpty(config.endpoint));
        ctx.put("wedgeDir", wedgeDir.getAbsolutePath());
        ctx.put("coreVersion", "");
        return ctx;
    }

    private static Messages safeMessages() {
        try {
            return Messages.forDefaultLocale();
        } catch (Throwable t) {
            return Messages.forLanguage("en");
        }
    }

    private static String nullToEmpty(String s) {
        return s == null ? "" : s;
    }
}
