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

    private Core() {
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
            // TODO(FR-092): 由机器码身份生成稳定唯一 X-Machine-Id；此前传空（端点仅审计/统计用，可空）。
            String machineId = ctx.getOrDefault("machineId", "");

            if (channel == null || endpoint == null) {
                System.err.println("[jm-updater] core: 缺少 channel/endpoint，fail-static");
                return Updater.FAIL_STATIC;
            }

            Transport transport = new HttpTransport(
                    endpoint, channel, key, machineId, coreVersion, Duration.ofSeconds(15));
            Updater updater = new Updater(gameDir, transport, Signatures.production());
            return updater.run();
        } catch (Throwable t) {
            // 不抛逃逸到楔子；fail-static（契约 §6.3）。
            System.err.println("[jm-updater] core fail-static: " + t);
            return Updater.FAIL_STATIC;
        }
    }
}
