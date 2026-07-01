package top.wcpe.mc.jm.updater.core;

import java.nio.file.Path;
import java.nio.file.Paths;
import java.time.Duration;
import java.util.Map;

/**
 * JM 客户端 OTA 更新主体（被楔子动态加载）。
 *
 * <p>{@code run} 拉 manifest（latest）→ 文件级 reconcile（增量/减量）；
 * 端点不可达 fail-static 带本地版本放行。
 *
 * <p>FR-256 起去掉验签 / CAS / core 自更新（信任靠 HTTPS + 拉取密钥鉴权，
 * 见 {@code docs/specs/updater-arch-simplification/spec.md}）。
 * <p>协议见 {@code docs/specs/client-distribution/contract.md}（ADR-021）。
 */
public final class Core {

    /** selftest 通过码（FR-091）；{@link UrlClassLoaderSelfTest} 据此判定新 jar 是否可切换。 */
    public static final int SELFTEST_OK = 0;

    private Core() {
    }

    /**
     * 新 core jar 切换前的自检入口（FR-091）：被 selftest 以独立 classloader 反射调用，
     * 校验本 jar 关键依赖（JSON / 哈希 / zstd 解码）在仅自身 classpath 下可用；通过返回 {@link #SELFTEST_OK}。
     */
    public static int selfTest() {
        try {
            if (!(Json.parse("{\"a\":1}") instanceof Map)) {
                return 1;
            }
            if (Hashes.sha256(new byte[] { 1, 2, 3 }).length() != 64) {
                return 2;
            }
            // zstd 解码链路（fat jar 内置 zstd-jni 可独立加载）：压缩再解压回原。
            byte[] sample = "jm-updater-core-selftest".getBytes(java.nio.charset.StandardCharsets.UTF_8);
            byte[] back = Codec.decode(com.github.luben.zstd.Zstd.compress(sample), "zstd");
            if (!java.util.Arrays.equals(sample, back)) {
                return 3;
            }
            return SELFTEST_OK;
        } catch (Throwable t) {
            return 99;
        }
    }

    /**
     * 楔子入口（契约 §6.3）。{@code ctx} = {gameDir, channel, key, endpoint, wedgeDir, coreVersion}。
     *
     * @return 0 = 更新成功放行；非 0 = fail-static（带本地版本放行）。不抛异常逃逸到楔子。
     */
    public static int run(Map<String, String> ctx) {
        try {
            String gameDirStr = ctx.get("gameDir");
            if (gameDirStr == null || gameDirStr.isEmpty()) {
                System.err.println("[jm-updater] core: 缺少 gameDir，fail-static");
                return Updater.FAIL_STATIC;
            }
            Path gameDir = Paths.get(gameDirStr);

            String channel = ctx.get("channel");
            String key = ctx.get("key");
            String endpoint = ctx.get("endpoint");
            // 本次运行的 core 版本号（wedge 经 ctx 注入），透传给 transport 做请求标识。
            // FR-256 起 core 自更新上移到楔子（FR-258），core 不再据 manifest 自更新，此值仅作信息性透传。
            String coreVersion = ctx.getOrDefault("coreVersion", "");
            // 机器码身份（FR-092）：稳定、不可逆、跨平台；ctx 显式提供则用之（测试/特殊），否则本机生成。
            String machineId = ctx.getOrDefault("machineId", "");
            if (machineId.isEmpty()) {
                machineId = MachineId.get();
            }

            if (channel == null || endpoint == null) {
                System.err.println("[jm-updater] core: 缺少 channel/endpoint，fail-static");
                return Updater.FAIL_STATIC;
            }

            Transport transport = new HttpTransport(
                    endpoint, channel, key, machineId, coreVersion, Duration.ofSeconds(15));
            Path stateDir = gameDir.resolve(".jm-updater");
            long fromVersion = StateStore.load(stateDir).lastSeenVersion();
            long start = System.currentTimeMillis();
            // 进度窗口（FR-099）：默认展示；ctx progressUi=false 可关（headless 由展示层自动降级文本）。
            boolean progressUiEnabled = !"false".equalsIgnoreCase(ctx.getOrDefault("progressUi", "true"));
            Updater updater = new Updater(gameDir, transport, progressUiEnabled);
            int rc = updater.run();

            // 遥测上报（FR-094，best-effort、opt-out）：BUSY（未实际更新）不报；telemetry=false 关闭。
            boolean telemetryEnabled = !"false".equalsIgnoreCase(ctx.getOrDefault("telemetry", "true"));
            if (telemetryEnabled && rc != Updater.BUSY) {
                long toVersion = StateStore.load(stateDir).lastSeenVersion();
                transport.postTelemetry(
                        Telemetry.build(channel, rc, fromVersion, toVersion, System.currentTimeMillis() - start));
            }
            return rc;
        } catch (Throwable t) {
            // 不抛逃逸到楔子；fail-static（契约 §6.3）。
            System.err.println("[jm-updater] core fail-static: " + t);
            return Updater.FAIL_STATIC;
        }
    }
}
