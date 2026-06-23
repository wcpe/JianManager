package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * updater 本地状态持久化（防降级基准 {@code lastSeenVersion} 等，契约 §3 / ADR-022 决策 7）。
 *
 * <p>落 {@code <gameDir>/.jm-updater/state.json}。读失败按空状态处理（首次运行）。
 */
final class StateStore {

    private static final String KEY_LAST_SEEN = "lastSeenVersion";

    private final Path stateFile;
    private final Map<String, Object> data;

    private StateStore(Path stateFile, Map<String, Object> data) {
        this.stateFile = stateFile;
        this.data = data;
    }

    /** 从状态目录加载（不存在/损坏则空状态）。 */
    static StateStore load(Path stateDir) {
        Path file = stateDir.resolve("state.json");
        Map<String, Object> data = new LinkedHashMap<>();
        if (Files.isRegularFile(file)) {
            try {
                String text = new String(Files.readAllBytes(file), StandardCharsets.UTF_8);
                Object parsed = Json.parse(text);
                if (parsed instanceof Map) {
                    @SuppressWarnings("unchecked")
                    Map<String, Object> m = (Map<String, Object>) parsed;
                    data.putAll(m);
                }
            } catch (Exception e) {
                // 损坏的状态文件按首次运行处理；防降级会退化为「接受当前」，下次起恢复。
            }
        }
        return new StateStore(file, data);
    }

    /** 已见最高版本（首次运行返回 -1，表示无基准）。 */
    long lastSeenVersion() {
        Object v = data.get(KEY_LAST_SEEN);
        if (v instanceof Number) {
            return ((Number) v).longValue();
        }
        return -1L;
    }

    /** 记录已见版本（仅在新值更高时抬升），并持久化。 */
    void recordVersion(long version) throws IOException {
        long current = lastSeenVersion();
        if (version > current) {
            data.put(KEY_LAST_SEEN, version);
            persist();
        }
    }

    private void persist() throws IOException {
        Files.createDirectories(stateFile.getParent());
        Path tmp = stateFile.resolveSibling("state.json.tmp");
        Files.write(tmp, Json.canonical(data).getBytes(StandardCharsets.UTF_8));
        try {
            Files.move(tmp, stateFile, java.nio.file.StandardCopyOption.REPLACE_EXISTING,
                    java.nio.file.StandardCopyOption.ATOMIC_MOVE);
        } catch (java.nio.file.AtomicMoveNotSupportedException e) {
            Files.move(tmp, stateFile, java.nio.file.StandardCopyOption.REPLACE_EXISTING);
        }
    }
}
