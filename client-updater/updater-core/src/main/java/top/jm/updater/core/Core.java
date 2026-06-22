package top.jm.updater.core;

import java.util.Map;

/**
 * JM 客户端 OTA 更新主体（被楔子动态加载）。
 *
 * <p>{@code run} 拉签名 manifest（latest）→ Ed25519 验签 + 防降级 → 文件级 reconcile
 * （增量/减量）→ CAS 缓存 → 自更新 + N-1 回退；端点不可达 fail-static 带本地版本放行。
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
            // TODO(FR-090): 拉 manifest（带 X-Client-Key + X-Machine-Id）→ Ed25519 验签 + 防降级（lastSeenVersion）
            // TODO(FR-090): 文件级 reconcile —— md5/size 快筛 + sha256 强校验；增量下载 zstd 制品解压 + 减量
            // TODO(FR-090): 托管区/玩家区隔离（managedDirs / saves·options.txt 永不碰）；sync 策略 strict|once|ignore
            // TODO(FR-090): CAS 缓存 + LRU 清理；单实例并发锁（单 gameDir）；平台变体取本机文件集
            // TODO(FR-091): updater-core 自更新（验签+selftest+回退）+ N-1 保留 + boot-success 失败自动回退
            // TODO(FR-092): 机器码生成（多硬件特征不可逆 hash）并携带
            // TODO(FR-094): 遥测上报（结果/版本/环境/boot-success，隐私可关）
            System.out.println("[jm-updater] core run（骨架，待 FR-090 实现）ctx=" + ctx);
            return 0;
        } catch (Throwable t) {
            // 不抛逃逸到楔子；fail-static。
            System.err.println("[jm-updater] core fail-static: " + t);
            return 1;
        }
    }
}
