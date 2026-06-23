package top.jm.updater.core;

import java.nio.file.Path;
import java.nio.file.Paths;
import java.time.Duration;
import java.util.Map;

/**
 * JM 客户端 OTA 更新主体（被楔子动态加载）。
 *
 * <p>{@code run} 拉签名 manifest（latest）→ Ed25519 验签 + 防降级 → 文件级 reconcile
 * （增量/减量）→ CAS 缓存；端点不可达 fail-static 带本地版本放行。
 *
 * <p>协议见 {@code docs/specs/client-distribution/contract.md}（ADR-021/022）。
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

            // 本次运行的 core 版本（wedge 经 ctx 注入；解析失败/缺省按 bundled=0，FR-091 自更新比对基准）。
            long runningCoreVersion = 0;
            try {
                if (!coreVersion.isEmpty()) {
                    runningCoreVersion = Long.parseLong(coreVersion.trim());
                }
            } catch (NumberFormatException ignore) {
                // 非法版本按 0 处理：自更新会把 manifest 声明版本视为更高、尝试一次（幂等无害）。
            }

            Transport transport = new HttpTransport(
                    endpoint, channel, key, machineId, coreVersion, Duration.ofSeconds(15));
            Updater updater = new Updater(gameDir, transport, Signatures.production(),
                    runningCoreVersion, new UrlClassLoaderSelfTest());
            return updater.run();
        } catch (Throwable t) {
            // 不抛逃逸到楔子；fail-static（契约 §6.3）。
            System.err.println("[jm-updater] core fail-static: " + t);
            return Updater.FAIL_STATIC;
        }
    }
}
