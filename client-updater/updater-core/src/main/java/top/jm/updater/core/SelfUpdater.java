package top.jm.updater.core;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;

/**
 * updater-core 自更新（FR-091）：据 {@code manifest.agent.core} 下载更高版本 core jar →
 * sha256 校验 → selftest → 暂存 pending（落 {@code core/<sha>.jar} + state.properties），
 * 由 wedge 下次 premain promote 后加载。
 *
 * <p>全程安全：任一步失败都不改状态、不影响本次 reconcile 放行（fail-static 语义不变）。
 * 一次只暂存一版（已有未决 pending 则串行等其 promote/rollback）。
 */
final class SelfUpdater {

    private final Path coreDir;
    private final Transport transport;
    private final CoreSelfTest selfTest;
    private final Logger log;

    SelfUpdater(Path stateDir, Transport transport, CoreSelfTest selfTest, Logger log) {
        this.coreDir = stateDir.resolve("core");
        this.transport = transport;
        this.selfTest = selfTest;
        this.log = log;
    }

    /**
     * 若 manifest 声明更高 core 版本则下载并暂存为 pending。
     *
     * @param runningCoreVersion 本次运行的 core 版本（wedge 经 ctx 注入；bundled 默认 0）
     * @return 是否暂存了新版（供日志/测试断言）
     */
    boolean maybeUpdate(Manifest manifest, long runningCoreVersion, Platform platform) {
        try {
            long target = manifest.agentCoreVersion;
            if (target < 0 || target <= runningCoreVersion) {
                return false; // 未声明自更新段，或不比当前运行版本新。
            }
            CoreSelectStore store = CoreSelectStore.load(coreDir);
            if (store.hasPending()) {
                return false; // 已有未决 pending（trial 进行中），串行解决后再暂存下一版。
            }
            if (store.selectedVersion() >= target) {
                return false; // 已选定该版本或更高。
            }
            if (target <= store.failedVersion()) {
                // 该版本曾 trial 失败回退（wedge 记 failedVersion）→ 不重暂存同一坏 core，
                // 仅当出现更高版本（修复版）才再尝试，避免 boot-loop（FR-091 真机修）。
                return false;
            }
            Manifest.AgentArtifact art = manifest.agentCoreArtifact(platform.tag());
            if (art == null || art.sha256 == null || art.sha256.isEmpty()) {
                return false; // 该平台无 core 制品。
            }

            byte[] artifactBytes;
            try {
                artifactBytes = transport.fetchArtifact(art.sha256);
            } catch (IOException e) {
                log.warn("自更新：下载 core 制品失败（不影响本次放行）: " + e);
                return false;
            }
            // 下载完整性：制品自身 sha256（内容寻址，manifest 已 Ed25519 验签 → 该值可信）。
            if (!art.sha256.equalsIgnoreCase(Hashes.sha256(artifactBytes))) {
                log.error("自更新：core 制品 sha256 不符，丢弃");
                return false;
            }
            byte[] jarBytes;
            try {
                jarBytes = Codec.decode(artifactBytes, art.codec);
            } catch (IOException e) {
                log.error("自更新：core 制品解码失败: " + e);
                return false;
            }
            String jarSha = Hashes.sha256(jarBytes);

            Files.createDirectories(coreDir);
            // 先落临时文件做 selftest（半成品绝不被当作可用 core）。
            Path tmp = coreDir.resolve(jarSha + ".jar.tmp");
            Files.write(tmp, jarBytes);
            if (!selfTest.test(tmp)) {
                log.error("自更新：新 core v" + target + " selftest 未通过，丢弃");
                Files.deleteIfExists(tmp);
                return false;
            }
            // selftest 通过 → 原子落定 + 置 pending（仅此刻状态才变）。
            Path jar = coreDir.resolve(jarSha + ".jar");
            try {
                Files.move(tmp, jar, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
            } catch (java.nio.file.AtomicMoveNotSupportedException e) {
                Files.move(tmp, jar, StandardCopyOption.REPLACE_EXISTING);
            }
            store.setPending(jarSha, target);
            store.store();
            log.info("自更新：已暂存 core v" + target + "（下次启动 trial 验证）");
            return true;
        } catch (Throwable t) {
            log.warn("自更新异常（忽略，照常放行）: " + t);
            return false;
        }
    }
}
