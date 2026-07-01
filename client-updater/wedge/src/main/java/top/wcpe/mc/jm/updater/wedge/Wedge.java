package top.wcpe.mc.jm.updater.wedge;

import java.io.File;
import java.lang.instrument.Instrumentation;
import java.net.URISyntaxException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * JM 客户端 OTA 楔子（javaagent）。
 *
 * <p>经启动器 JVM 参数 {@code -javaagent:wedge.jar=<gameDir>} 注入；premain 自定位、
 * 查询 CP 的 coreEndpoint → 按需下载 updater-core → 加载并调用其 {@code run} 入口、同步等待，
 * 更新失败 fail-static、楔子自身任何异常 fail-open——绝不挡住游戏启动（ADR-021 决策 6）。
 *
 * <p>FR-258 楔子改 gradle-wrapper 模式：整合包只带 wedge.jar，首次自动拉 core。
 * 楔子代码冻结后永不变更（§2.5），后续逻辑变动通过 updater-core 自动更新实现。
 *
 * <p>协议见 {@code docs/specs/updater-arch-simplification/spec.md} §2.5/§3.1。
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
            System.out.println(msg.get(Messages.Key.STARTING));
            File wedgeDir = locateWedgeDir();
            String gameDir = GameDirResolver.resolve(agentArgs, System.getProperty("sun.java.command"));
            if (gameDir == null || gameDir.trim().isEmpty()) {
                gameDir = wedgeDir.getAbsolutePath();
            }
            runUpdate(wedgeDir, gameDir, msg);
        } catch (Throwable t) {
            // 楔子唯一允许的失败模式：放行游戏（ADR-021 决策 6 / FR-089）。
            System.err.println(msg.get(Messages.Key.WEDGE_FAILOPEN) + " (" + t + ")");
        }
    }

    /**
     * 更新主流程（抽为独立方法便于单测）。
     *
     * @param wedgeDir 楔子所在目录（含 jm-updater.json）
     * @param gameDir  游戏目录（.jm-updater/core 的根）
     * @param msg      i18n 提示
     */
    static void runUpdate(File wedgeDir, String gameDir, Messages msg) throws Exception {
        // 1. 读 jm-updater.json（保留原始 JSON 文本供 configJson 透传）。
        File configFile = new File(wedgeDir, "jm-updater.json");
        if (!configFile.isFile()) {
            // 无配置 = 未启用 OTA：fail-open 放行（不挡游戏）。
            System.err.println("[JM Updater] 未找到 jm-updater.json，跳过更新直接启动。");
            return;
        }
        String configJsonText = new String(Files.readAllBytes(configFile.toPath()), StandardCharsets.UTF_8);
        WedgeConfig config = WedgeConfig.fromJson(configJsonText);

        // 2. 准备 core 目录 + 读取本地状态摘要（不推进状态机）。
        File coreDir = new File(new File(gameDir, ".jm-updater"), "core");
        CoreSelector.StateSummary summary = CoreSelector.readSummary(coreDir);

        // 3. 查询 CP 的 coreEndpoint（best-effort，不可达返回 null）。
        CoreFetcher.CoreEndpointInfo cpInfo = CoreFetcher.fetchInfo(config.coreEndpoint, config.key);

        // 4. 决策是否下载新 core。
        boolean shouldDownload = decideDownload(summary, cpInfo);
        if (shouldDownload && cpInfo != null) {
            try {
                System.out.println("[JM Updater] 发现新版 core（v" + cpInfo.version + "），正在下载…");
                CoreFetcher.downloadJar(cpInfo, config.key, coreDir);
                CoreSelector.setPending(coreDir, cpInfo.sha256, cpInfo.version);
            } catch (Exception e) {
                // 下载失败，继续用本地（如果有）
                System.err.println("[JM Updater] 下载 core jar 失败，尝试使用本地版本: " + e);
            }
        }

        // 5. CoreSelector.select 推进状态机（pending trial / promote / rollback）。
        CoreSelector.Selection sel = CoreSelector.select(coreDir);
        if (sel.coreJar == null) {
            // 无可用 core（首次断网 + 本地无 jar）→ fail-open 放行游戏
            System.err.println("[JM Updater] 无可用 core jar，跳过更新直接启动。");
            return;
        }

        // 6. 加载 selected/pending jar → CoreLoader.loadAndRun → 同步等待 + 超时（契约 §6.3）。
        Map<String, String> ctx = buildContext(gameDir, config, sel.coreVersion, configJsonText);
        int result = CoreLoader.loadAndRun(sel.coreJar, ctx, config.timeoutSec);

        // 7. 首次 trial 且 core 正常加载运行 → 起 boot-confirm 看门狗（FR-091）。
        boolean coreRanOk = result != CoreLoader.RESULT_LOAD_ERROR && result != CoreLoader.RESULT_TIMEOUT;
        if (sel.trial && coreRanOk) {
            CoreSelector.scheduleBootConfirm(coreDir, config.bootConfirmSec);
        }

        // 8. 处理结果：0=放行；超时/非 0=fail-static 放行带本地版本 + 提示（契约 §6.3）。
        if (result == CoreLoader.RESULT_OK) {
            System.out.println(msg.get(Messages.Key.UPDATE_OK));
        } else if (result == CoreLoader.RESULT_TIMEOUT) {
            System.err.println(msg.get(Messages.Key.UPDATE_TIMEOUT));
        } else {
            System.err.println(msg.get(Messages.Key.UPDATE_FAILED_STATIC));
        }

        // 9. 清理：保留最近 3 个 jar，删最老的。
        CoreSelector.retainLatestJars(coreDir, CoreSelector.KEEP_JARS);
    }

    /**
     * 决策是否需要下载新 core（§3.1 步骤 4）。
     *
     * <pre>
     * - 有未处理 pending → 不下载（CoreSelector.select 先处理）
     * - CP 不可达 → 不下载（用本地/fail-open）
     * - CP 版本 == 失败版本 → 不下载（已知坏版本不重试）
     * - 本地无 selected jar → 下载（首次运行/状态损坏）
     * - CP 版本 > 本地 selected → 下载（升级）
     * - CP 版本 <= 本地 selected → 不下载（用本地）
     * </pre>
     */
    private static boolean decideDownload(CoreSelector.StateSummary summary,
                                          CoreFetcher.CoreEndpointInfo cpInfo) {
        if (summary.hasPending) {
            return false; // CoreSelector.select 先处理现有 pending
        }
        if (cpInfo == null) {
            return false; // CP 不可达
        }
        if (cpInfo.version == summary.failedVersion && summary.failedVersion > 0) {
            return false; // 已知失败版本不重试
        }
        if (!summary.hasSelectedJar) {
            return true; // 首次运行或状态损坏
        }
        return cpInfo.version > summary.selectedVersion; // 升级
    }

    /** 自定位楔子所在目录（契约 §6.2）。 */
    private static File locateWedgeDir() throws URISyntaxException {
        File jar = new File(Wedge.class.getProtectionDomain()
                .getCodeSource().getLocation().toURI());
        File dir = jar.isFile() ? jar.getParentFile() : jar;
        return dir != null ? dir : new File(".");
    }

    /**
     * 组装传给 core 的 ctx（§2.5.2 冻结 key）。
     * <p>楔子代码冻结后此 ctx 格式永久固定——后续 core 可从 {@code configJson} 自行扩展解析。
     *
     * @param coreVersion 选定 core 的版本（FR-091 自更新比对基准）
     * @param configJson  jm-updater.json 原文（供 core 自行扩展解析，§2.5.2）
     */
    static Map<String, String> buildContext(String gameDir, WedgeConfig config,
                                            long coreVersion, String configJson) {
        Map<String, String> ctx = new LinkedHashMap<String, String>();
        ctx.put("gameDir", gameDir);
        ctx.put("channel", nullToEmpty(config.channel));
        ctx.put("key", nullToEmpty(config.key));
        ctx.put("endpoint", nullToEmpty(config.endpoint));
        ctx.put("coreVersion", Long.toString(coreVersion));
        ctx.put("telemetry", Boolean.toString(config.telemetry)); // FR-094 遥测开关（opt-out）。
        ctx.put("timeoutSec", Integer.toString(config.timeoutSec));
        ctx.put("configJson", nullToEmpty(configJson)); // §2.5.2 jm-updater.json 原文透传
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
