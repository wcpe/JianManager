package top.wcpe.mc.jm.updater.core;

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * 已解析的版本清单（契约 §2）。携带 reconcile 所需字段。
 *
 * <p>FR-256 起 manifest 不再携带签名段（信任模型改为 HTTPS + 拉取密钥鉴权，见
 * updater-arch-simplification spec §2 A），故本类不再保留 sig 字段或签名输入字节。
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
        /** patch-from 适用的本地旧文件 hash；null=无 patch。 */
        final String patchOldSha256;
        /** patch-from 产出的新文件 hash。 */
        final String patchNewSha256;
        /** patch 制品自身 hash（下载寻址 key）。 */
        final String patchArtifactSha256;
        final long patchArtifactSize;
        /** zstd-patch。 */
        final String patchArtifactCodec;

        FileEntry(String path, String sha256, String md5, long size, String sync,
                  String platform, String artifactSha256, long artifactSize, String artifactCodec,
                  String patchOldSha256, String patchNewSha256,
                  String patchArtifactSha256, long patchArtifactSize, String patchArtifactCodec) {
            this.path = path;
            this.sha256 = sha256;
            this.md5 = md5;
            this.size = size;
            this.sync = sync;
            this.platform = platform;
            this.artifactSha256 = artifactSha256;
            this.artifactSize = artifactSize;
            this.artifactCodec = artifactCodec;
            this.patchOldSha256 = patchOldSha256;
            this.patchNewSha256 = patchNewSha256;
            this.patchArtifactSha256 = patchArtifactSha256;
            this.patchArtifactSize = patchArtifactSize;
            this.patchArtifactCodec = patchArtifactCodec;
        }

        boolean hasPatch() {
            return patchOldSha256 != null && patchNewSha256 != null && patchArtifactSha256 != null;
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
    /** 运营自定义追加排除（FR-255）：命中前缀的路径永不删。空列表=未声明。 */
    final List<String> cleanExclude;
    final List<FileEntry> files;
    /** updater-core 自更新声明版本（契约 §2 agent.core.version，FR-091）；-1=未声明。段保留供楔子消费。 */
    final long agentCoreVersion;
    /** updater-core 自更新各平台制品（os→artifact，FR-091）；可空。 */
    final Map<String, AgentArtifact> agentCorePlatforms;

    private Manifest(int schemaVersion, String channel, long version, List<String> managedDirs,
                     List<String> cleanExclude,
                     List<FileEntry> files,
                     long agentCoreVersion, Map<String, AgentArtifact> agentCorePlatforms) {
        this.schemaVersion = schemaVersion;
        this.channel = channel;
        this.version = version;
        this.managedDirs = managedDirs;
        this.cleanExclude = cleanExclude;
        this.files = files;
        this.agentCoreVersion = agentCoreVersion;
        this.agentCorePlatforms = agentCorePlatforms;
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

        // FR-255：运营自定义追加排除（omitempty，可缺省）。
        List<String> cleanExclude = new ArrayList<>();
        Object ce = obj.get("cleanExclude");
        if (ce instanceof List) {
            for (Object o : (List<Object>) ce) {
                cleanExclude.add(String.valueOf(o));
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
                String patchOldSha = null;
                String patchNewSha = null;
                String patchArtSha = null;
                long patchArtSize = 0;
                String patchArtCodec = null;
                Object patch = f.get("patch");
                if (patch instanceof Map) {
                    Map<String, Object> p = (Map<String, Object>) patch;
                    patchOldSha = (String) p.get("oldSha256");
                    patchNewSha = (String) p.get("newSha256");
                    Object patchArt = p.get("artifact");
                    if (patchArt instanceof Map) {
                        Map<String, Object> a = (Map<String, Object>) patchArt;
                        patchArtSha = (String) a.get("sha256");
                        patchArtSize = asLong(a.get("size"), 0);
                        patchArtCodec = a.get("codec") == null ? "zstd-patch" : String.valueOf(a.get("codec"));
                    }
                }
                files.add(new FileEntry(
                        (String) f.get("path"),
                        (String) f.get("sha256"),
                        (String) f.get("md5"),
                        asLong(f.get("size"), 0),
                        f.get("sync") == null ? "strict" : String.valueOf(f.get("sync")),
                        (String) f.get("platform"),
                        artSha, artSize, artCodec,
                        patchOldSha, patchNewSha, patchArtSha, patchArtSize, patchArtCodec));
            }
        }

        // agent.core 自更新段（契约 §2，FR-091）：version + platforms[os].artifact。可缺省。
        // FR-256 起 core 自更新上移到楔子（FR-258），但 manifest 仍透传该段供楔子消费，故解析保留。
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
                Collections.unmodifiableList(cleanExclude),
                Collections.unmodifiableList(files),
                agentCoreVersion,
                agentCorePlatforms == null ? null : Collections.unmodifiableMap(agentCorePlatforms));
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
