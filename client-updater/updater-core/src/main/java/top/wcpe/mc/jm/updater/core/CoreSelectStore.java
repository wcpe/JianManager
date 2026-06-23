package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.Properties;

/**
 * updater-core 自更新选择状态（FR-091），落 {@code <gameDir>/.jm-updater/core/state.properties}。
 *
 * <p><b>格式即 wedge↔core 的契约</b>：用 {@link Properties}（JDK 原生，两模块各自读写、零 JSON 依赖）。
 * core 侧仅 <em>暂存</em> pending（下载+selftest 通过的新 core）；selected/prev 的 promote/rollback 由
 * wedge 的 {@code CoreSelector} 在 premain 加载前决策（见 {@code docs/specs/client-distribution/fr-091.md}）。
 *
 * <p>键：{@code selectedSha/selectedVersion}（当前应加载的 core）、{@code prevSha/prevVersion}（N-1）、
 * {@code pendingSha/pendingVersion}（已下载待首启确认）、{@code pendingTried}（已被加载过一次）。
 */
final class CoreSelectStore {

    static final String K_SELECTED_SHA = "selectedSha";
    static final String K_SELECTED_VERSION = "selectedVersion";
    static final String K_PENDING_SHA = "pendingSha";
    static final String K_PENDING_VERSION = "pendingVersion";
    static final String K_PENDING_TRIED = "pendingTried";
    static final String K_FAILED_VERSION = "failedVersion";

    private final Path file;
    private final Properties props;

    private CoreSelectStore(Path file, Properties props) {
        this.file = file;
        this.props = props;
    }

    /** 从 core 目录加载（不存在/损坏→空状态，按首次运行处理）。 */
    static CoreSelectStore load(Path coreDir) {
        Path f = coreDir.resolve("state.properties");
        Properties p = new Properties();
        if (Files.isRegularFile(f)) {
            try (InputStream in = Files.newInputStream(f)) {
                p.load(in);
            } catch (Exception e) {
                // 损坏按空状态：下次起恢复，绝不因状态损坏阻断（fail-open 精神）。
                p = new Properties();
            }
        }
        return new CoreSelectStore(f, p);
    }

    long selectedVersion() {
        return longProp(K_SELECTED_VERSION, 0);
    }

    /** 曾 trial 失败回退的最高 core 版本（wedge 回退时记录）；用于跳过重暂存同一坏版本，防 boot-loop（FR-091）。 */
    long failedVersion() {
        return longProp(K_FAILED_VERSION, 0);
    }

    String pendingSha() {
        return props.getProperty(K_PENDING_SHA, "");
    }

    long pendingVersion() {
        return longProp(K_PENDING_VERSION, -1);
    }

    /** 是否已有暂存 pending（有则本次不再暂存新版，串行解决）。 */
    boolean hasPending() {
        return !pendingSha().isEmpty();
    }

    /** 暂存一个已下载+selftest 通过的新 core（pendingTried=false）。 */
    void setPending(String sha, long version) {
        props.setProperty(K_PENDING_SHA, sha);
        props.setProperty(K_PENDING_VERSION, Long.toString(version));
        props.setProperty(K_PENDING_TRIED, "false");
    }

    private long longProp(String key, long def) {
        String v = props.getProperty(key);
        if (v == null || v.isEmpty()) {
            return def;
        }
        try {
            return Long.parseLong(v.trim());
        } catch (NumberFormatException e) {
            return def;
        }
    }

    /** 原子持久化（临时文件 + move）。 */
    void store() throws IOException {
        Files.createDirectories(file.getParent());
        Path tmp = file.resolveSibling("state.properties.tmp");
        try (OutputStream out = Files.newOutputStream(tmp)) {
            props.store(out, "jm-updater core self-update state (FR-091)");
        }
        try {
            Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
        } catch (java.nio.file.AtomicMoveNotSupportedException e) {
            Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING);
        }
    }
}
