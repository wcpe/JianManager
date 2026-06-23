package top.wcpe.mc.jm.updater.core;

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * 已解析的版本清单（契约 §2）。仅携带 reconcile 与验签所需字段。
 *
 * <p>{@link #raw} 保留原始对象树，验签时去掉 {@code sig} 后做 canonical JSON（契约 §3）。
 */
final class Manifest {

    /** 单文件条目（契约 §2 files[]）。 */
    static final class FileEntry {
        final String path;
        final String sha256;
        final String md5;
        final long size;
        /** strict=强制一致 | once=仅缺失时写 | ignore=不动。 */
        final String sync;
        /** null=全平台 | windows | macos | linux。 */
        final String platform;
        /** 制品自身 hash（下载寻址 key）。 */
        final String artifactSha256;
        final long artifactSize;
        /** zstd | none。 */
        final String artifactCodec;

        FileEntry(String path, String sha256, String md5, long size, String sync,
                  String platform, String artifactSha256, long artifactSize, String artifactCodec) {
            this.path = path;
            this.sha256 = sha256;
            this.md5 = md5;
            this.size = size;
            this.sync = sync;
            this.platform = platform;
            this.artifactSha256 = artifactSha256;
            this.artifactSize = artifactSize;
            this.artifactCodec = artifactCodec;
        }
    }

    /** updater-core 自更新段单平台制品引用（契约 §2 agent.core.platforms[os].artifact，FR-091）。 */
    static final class AgentArtifact {
        /** 制品自身 hash（下载寻址 key）。 */
        final String sha256;
        final long size;
        /** zstd | none。 */
        final String codec;

        AgentArtifact(String sha256, long size, String codec) {
            this.sha256 = sha256;
            this.size = size;
            this.codec = codec;
        }
    }

    final int schemaVersion;
    final String channel;
    final long version;
    final List<String> managedDirs;
    final List<FileEntry> files;
    final String sigAlg;
    final String sigKeyId;
    final String sigValue;
    /** updater-core 自更新声明版本（契约 §2 agent.core.version，FR-091）；-1=未声明。 */
    final long agentCoreVersion;
    /** updater-core 自更新各平台制品（os→artifact，FR-091）；可空。 */
    final Map<String, AgentArtifact> agentCorePlatforms;
    /** 原始对象树（含 sig），验签用。 */
    final Map<String, Object> raw;

    private Manifest(int schemaVersion, String channel, long version, List<String> managedDirs,
                     List<FileEntry> files, String sigAlg, String sigKeyId, String sigValue,
                     long agentCoreVersion, Map<String, AgentArtifact> agentCorePlatforms,
                     Map<String, Object> raw) {
        this.schemaVersion = schemaVersion;
        this.channel = channel;
        this.version = version;
        this.managedDirs = managedDirs;
        this.files = files;
        this.sigAlg = sigAlg;
        this.sigKeyId = sigKeyId;
        this.sigValue = sigValue;
        this.agentCoreVersion = agentCoreVersion;
        this.agentCorePlatforms = agentCorePlatforms;
        this.raw = raw;
    }

    /**
     * 取指定平台（windows|macos|linux）的 updater-core 自更新制品；无自更新段或该平台缺失返回 null（FR-091）。
     */
    AgentArtifact agentCoreArtifact(String platform) {
        if (agentCorePlatforms == null || platform == null) {
            return null;
        }
        return agentCorePlatforms.get(platform);
    }

    /** 解析 manifest JSON 文本。结构非法即抛 {@link Json.JsonException}。 */
    @SuppressWarnings("unchecked")
    static Manifest parse(String text) {
        Object root = Json.parse(text);
        if (!(root instanceof Map)) {
            throw new Json.JsonException("manifest 根必须是对象");
        }
        Map<String, Object> obj = (Map<String, Object>) root;

        int schemaVersion = (int) asLong(obj.get("schemaVersion"), 1);
        String channel = (String) obj.get("channel");
        long version = asLong(obj.get("version"), -1);

        List<String> managedDirs = new ArrayList<>();
        Object md = obj.get("managedDirs");
        if (md instanceof List) {
            for (Object o : (List<Object>) md) {
                managedDirs.add(String.valueOf(o));
            }
        }

        List<FileEntry> files = new ArrayList<>();
        Object fl = obj.get("files");
        if (fl instanceof List) {
            for (Object o : (List<Object>) fl) {
                Map<String, Object> f = (Map<String, Object>) o;
                String artSha = null;
                long artSize = 0;
                String artCodec = "none";
                Object art = f.get("artifact");
                if (art instanceof Map) {
                    Map<String, Object> a = (Map<String, Object>) art;
                    artSha = (String) a.get("sha256");
                    artSize = asLong(a.get("size"), 0);
                    artCodec = a.get("codec") == null ? "none" : String.valueOf(a.get("codec"));
                }
                files.add(new FileEntry(
                        (String) f.get("path"),
                        (String) f.get("sha256"),
                        (String) f.get("md5"),
                        asLong(f.get("size"), 0),
                        f.get("sync") == null ? "strict" : String.valueOf(f.get("sync")),
                        (String) f.get("platform"),
                        artSha, artSize, artCodec));
            }
        }

        String sigAlg = null;
        String sigKeyId = null;
        String sigValue = null;
        Object sig = obj.get("sig");
        if (sig instanceof Map) {
            Map<String, Object> s = (Map<String, Object>) sig;
            sigAlg = (String) s.get("alg");
            sigKeyId = (String) s.get("keyId");
            sigValue = (String) s.get("value");
        }

        // agent.core 自更新段（契约 §2，FR-091）：version + platforms[os].artifact。可缺省。
        long agentCoreVersion = -1;
        Map<String, AgentArtifact> agentCorePlatforms = null;
        Object agent = obj.get("agent");
        if (agent instanceof Map) {
            Object core = ((Map<String, Object>) agent).get("core");
            if (core instanceof Map) {
                Map<String, Object> c = (Map<String, Object>) core;
                agentCoreVersion = asLong(c.get("version"), -1);
                Object platforms = c.get("platforms");
                if (platforms instanceof Map) {
                    agentCorePlatforms = new LinkedHashMap<>();
                    for (Map.Entry<String, Object> e : ((Map<String, Object>) platforms).entrySet()) {
                        if (!(e.getValue() instanceof Map)) {
                            continue;
                        }
                        Object art = ((Map<String, Object>) e.getValue()).get("artifact");
                        if (art instanceof Map) {
                            Map<String, Object> a = (Map<String, Object>) art;
                            agentCorePlatforms.put(e.getKey(), new AgentArtifact(
                                    (String) a.get("sha256"),
                                    asLong(a.get("size"), 0),
                                    a.get("codec") == null ? "none" : String.valueOf(a.get("codec"))));
                        }
                    }
                }
            }
        }

        return new Manifest(schemaVersion, channel, version,
                Collections.unmodifiableList(managedDirs),
                Collections.unmodifiableList(files),
                sigAlg, sigKeyId, sigValue,
                agentCoreVersion,
                agentCorePlatforms == null ? null : Collections.unmodifiableMap(agentCorePlatforms),
                (Map<String, Object>) root);
    }

    /**
     * 去掉 {@code sig} 字段后的 canonical JSON 字节（UTF-8）——签名覆盖范围（契约 §3）。
     */
    byte[] signingBytes() {
        Map<String, Object> copy = new LinkedHashMap<>(raw);
        copy.remove("sig");
        return Json.canonical(copy).getBytes(java.nio.charset.StandardCharsets.UTF_8);
    }

    private static long asLong(Object o, long def) {
        if (o == null) {
            return def;
        }
        if (o instanceof Number) {
            return ((Number) o).longValue();
        }
        return Long.parseLong(String.valueOf(o));
    }
}
