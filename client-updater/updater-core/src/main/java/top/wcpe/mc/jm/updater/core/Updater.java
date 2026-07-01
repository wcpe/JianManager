package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.nio.file.Path;

/**
 * reconcile 编排（FR-090 / FR-256 简化后）：拉 manifest → 单实例锁 → reconcile → 记录版本。
 *
 * <p>FR-256 起：去掉验签（信任靠 HTTPS + 拉取密钥鉴权）、防降级（回滚靠服务端改版本号）、
 * CAS LRU 清理（CasCache 已删）、core 自更新（上移到楔子，FR-258）。仍保留：单实例锁、
 * 文件级 reconcile（增量/减量）、端点不可达 fail-static 带本地版本放行。
 *
 * <p>协作者（{@link Transport} / 路径）由构造注入，使核心逻辑可在临时目录端到端测试，
 * 不依赖真端点（生产装配见 {@link Core#run}）。
 *
 * <p>返回码遵循契约 §6.3：{@code 0}=放行；非 {@code 0}=fail-static（带本地版本放行）。
 */
final class Updater {

    /** updater 结束码（契约 §6.3）。 */
    static final int OK = 0;
    static final int FAIL_STATIC = 1;
    static final int BUSY = 2; // 另一实例在跑，本次退让（仍放行游戏）。

    private final Path gameDir;
    private final Path stateDir;
    private final Transport transport;
    /** 是否展示更新进度窗口（FR-099）；测试/headless/ctx 关闭时为 false。 */
    private final boolean progressUiEnabled;

    /** 装配（含 FR-099 进度窗）。 */
    Updater(Path gameDir, Transport transport, boolean progressUiEnabled) {
        this.gameDir = gameDir.toAbsolutePath().normalize();
        this.stateDir = this.gameDir.resolve(".jm-updater");
        this.transport = transport;
        this.progressUiEnabled = progressUiEnabled;
    }

    /**
     * 执行一次更新。全程兜底——任何异常都收敛为 fail-static（不抛逃逸到楔子，契约 §6.3）。
     */
    int run() {
        Logger log = Logger.create(stateDir);
        try {
            return runInternal(log);
        } catch (Throwable t) {
            log.error("更新异常，fail-static 带本地版本放行: " + t);
            return FAIL_STATIC;
        } finally {
            log.close();
        }
    }

    private int runInternal(Logger log) {
        // 单实例并发锁：单 gameDir 仅一个 updater 改目录（契约 FR-090）。
        SingleInstanceLock lock;
        try {
            lock = SingleInstanceLock.tryAcquire(stateDir);
        } catch (IOException e) {
            log.warn("获取实例锁失败，放行: " + e);
            return BUSY;
        }
        if (lock == null) {
            log.warn("另一 updater 实例正在运行，本次退让放行（不并发改目录）");
            return BUSY;
        }

        try (SingleInstanceLock held = lock;
             ProgressReporter reporter = ProgressReporter.create(
                     CoreMessages.forDefaultLocale(), log, progressUiEnabled)) {
            // 1. 拉 manifest（端点不可达 → fail-static 带本地版本，契约 §6.3）。
            String manifestJson;
            try {
                manifestJson = transport.fetchManifest();
            } catch (IOException e) {
                log.warn("manifest 端点不可达，fail-static 带本地版本进游戏: " + e);
                return FAIL_STATIC;
            }

            Manifest manifest;
            try {
                manifest = Manifest.parse(manifestJson);
            } catch (RuntimeException e) {
                log.error("manifest 解析失败，fail-static: " + e);
                return FAIL_STATIC;
            }

            // 2. 文件级 reconcile（增量 + 减量，托管区/玩家区隔离，契约 §2/§6.4）。
            Reconciler reconciler = new Reconciler(gameDir, transport, Platform.current(), log, reporter);
            Reconciler.Result result;
            try {
                result = reconciler.reconcile(manifest);
            } catch (IOException e) {
                log.error("reconcile 失败，fail-static: " + e);
                return FAIL_STATIC;
            }
            log.info("reconcile 完成 version=" + manifest.version + " " + result);

            // 3. 文件级有错 → fail-static（不把残缺当成功放行，玩家带本地能跑的版本进游戏）。
            if (!result.errors.isEmpty()) {
                log.error("reconcile 存在 " + result.errors.size() + " 个文件错误，fail-static");
                return FAIL_STATIC;
            }

            // 4. 记录本次版本供遥测上报（FR-094 fromVersion/toVersion 基准；FR-256 已去防降级，不再据此拒绝低版本）。
            try {
                StateStore.load(stateDir).recordVersion(manifest.version);
            } catch (IOException e) {
                log.warn("记录 lastSeenVersion 失败: " + e);
            }

            log.info("更新成功，放行游戏 version=" + manifest.version);
            return OK;
        }
    }
}
