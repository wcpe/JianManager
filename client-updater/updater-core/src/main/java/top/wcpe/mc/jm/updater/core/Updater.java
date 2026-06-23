package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.nio.file.Path;

/**
 * reconcile 编排（FR-090）：拉 manifest → 验签 → 防降级 → 单实例锁 → reconcile → CAS 清理 → 记录版本。
 *
 * <p>协作者（{@link Transport} / {@link Signatures} / 路径）由构造注入，使核心逻辑可在临时目录端到端测试，
 * 不依赖真端点（生产装配见 {@link Core#run}）。
 *
 * <p>返回码遵循契约 §6.3：{@code 0}=放行；非 {@code 0}=fail-static（带本地版本放行）。
 */
final class Updater {

    /** updater 结束码（契约 §6.3）。 */
    static final int OK = 0;
    static final int FAIL_STATIC = 1;
    static final int BUSY = 2; // 另一实例在跑，本次退让（仍放行游戏）。

    /** 缓存容量上限：1.5 GiB（连同 N-1 控制磁盘占用，契约 FR-090）。 */
    private static final long CAS_LIMIT_BYTES = 1536L * 1024 * 1024;

    private final Path gameDir;
    private final Path stateDir;
    private final Transport transport;
    private final Signatures signatures;
    /** 本次运行的 core 版本（FR-091 自更新比对基准；reconcile-only 调用按 0）。 */
    private final long runningCoreVersion;
    /** 新下载 core 的自检（FR-091）；为 null 表示不做自更新（纯 reconcile）。 */
    private final CoreSelfTest selfTest;

    /** 纯 reconcile 装配（不做 core 自更新）。供 FR-090 测试与不关心自更新的调用方使用。 */
    Updater(Path gameDir, Transport transport, Signatures signatures) {
        this(gameDir, transport, signatures, 0, null);
    }

    /** 完整装配（含 FR-091 core 自更新）：runningCoreVersion 为本次 core 版本，selfTest 校验新 jar。 */
    Updater(Path gameDir, Transport transport, Signatures signatures,
            long runningCoreVersion, CoreSelfTest selfTest) {
        this.gameDir = gameDir.toAbsolutePath().normalize();
        this.stateDir = this.gameDir.resolve(".jm-updater");
        this.transport = transport;
        this.signatures = signatures;
        this.runningCoreVersion = runningCoreVersion;
        this.selfTest = selfTest;
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

        try (SingleInstanceLock held = lock) {
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

            // 2. Ed25519 验签——信任根（契约 §3，ADR-022）。签名缺失/不符一律拒绝。
            if (!signatures.verify(manifest)) {
                log.error("manifest 验签失败（keyId=" + manifest.sigKeyId
                        + "），拒绝并 fail-static 带本地版本进游戏");
                return FAIL_STATIC;
            }

            // 3. 防降级：拒绝 version 低于本地已见最高版本的 manifest（契约 §3，ADR-022 决策 7）。
            StateStore state = StateStore.load(stateDir);
            long lastSeen = state.lastSeenVersion();
            if (lastSeen >= 0 && manifest.version < lastSeen) {
                log.error("防降级触发：manifest version=" + manifest.version
                        + " < 本地已见 " + lastSeen + "，拒绝（疑似重放旧版投毒）");
                return FAIL_STATIC;
            }

            // 4. 文件级 reconcile（增量 + 减量，托管区/玩家区隔离，契约 §2/§6.4）。
            CasCache cas = new CasCache(stateDir.resolve("cas"));
            Reconciler reconciler = new Reconciler(gameDir, transport, cas, Platform.current(), log);
            Reconciler.Result result;
            try {
                result = reconciler.reconcile(manifest);
            } catch (IOException e) {
                log.error("reconcile 失败，fail-static: " + e);
                return FAIL_STATIC;
            }
            log.info("reconcile 完成 version=" + manifest.version + " " + result);

            // 5. CAS LRU 清理（容量上限，契约 FR-090）。
            try {
                cas.enforceLimit(CAS_LIMIT_BYTES);
            } catch (IOException e) {
                log.warn("CAS 清理失败（不影响本次更新）: " + e);
            }

            // 6. 文件级有错 → fail-static（不把残缺当成功放行，玩家带本地能跑的版本进游戏）。
            if (!result.errors.isEmpty()) {
                log.error("reconcile 存在 " + result.errors.size() + " 个文件错误，fail-static");
                return FAIL_STATIC;
            }

            // 7. 全部成功才抬升防降级基准（失败时不抬升，避免锁死在错误版本）。
            try {
                state.recordVersion(manifest.version);
            } catch (IOException e) {
                log.warn("记录 lastSeenVersion 失败: " + e);
            }

            // 8. core 自更新（FR-091）：reconcile 成功后据 manifest.agent.core 暂存更高版本 core（失败不影响放行）。
            if (selfTest != null) {
                try {
                    new SelfUpdater(stateDir, transport, selfTest, log)
                            .maybeUpdate(manifest, runningCoreVersion, Platform.current());
                } catch (Throwable t) {
                    log.warn("core 自更新阶段异常（忽略，照常放行）: " + t);
                }
            }

            log.info("更新成功，放行游戏 version=" + manifest.version);
            return OK;
        }
    }
}
