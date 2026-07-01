package top.wcpe.mc.jm.updater.core;

import com.github.luben.zstd.Zstd;

import java.io.ByteArrayInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * 测试夹具：构造 manifest 对象树、制品仓（sha256→字节）、内存 Transport。
 */
final class TestFixtures {

    /** 内存 Transport：从预置 manifest 文本与制品表返回，模拟端点（无需真 HTTP）。 */
    static class MemoryTransport implements Transport {
        String manifestJson;
        boolean manifestUnreachable;
        final Map<String, byte[]> artifacts = new HashMap<>();
        int artifactFetchCount;

        @Override
        public String fetchManifest() throws IOException {
            if (manifestUnreachable) {
                throw new IOException("模拟端点不可达");
            }
            return manifestJson;
        }

        @Override
        public byte[] fetchArtifact(String artifactSha256) throws IOException {
            byte[] data = artifacts.get(artifactSha256);
            if (data == null) {
                throw new IOException("制品不存在: " + artifactSha256);
            }
            return data;
        }

        /** 最近一次流式拉取的 offset（断点续传测试断言用）。 */
        long lastStreamOffset;

        @Override
        public InputStream fetchArtifactStream(String artifactSha256, long offset) throws IOException {
            artifactFetchCount++;
            lastStreamOffset = offset;
            byte[] data = artifacts.get(artifactSha256);
            if (data == null) {
                throw new IOException("制品不存在: " + artifactSha256);
            }
            int start = (int) Math.max(0L, Math.min(offset, data.length));
            return new ByteArrayInputStream(data, start, data.length - start);
        }

        /** 最近一次遥测上报体（FR-094 测试断言用）。 */
        String lastTelemetry;

        @Override
        public void postTelemetry(String jsonBody) {
            lastTelemetry = jsonBody;
        }
    }

    /** 一个待加入 manifest 的文件 + 其制品。 */
    static final class FileSpec {
        final String path;
        final byte[] content;
        String sync = "strict";
        String platform = null;
        String codec = "zstd";

        FileSpec(String path, byte[] content) {
            this.path = path;
            this.content = content;
        }

        FileSpec sync(String s) {
            this.sync = s;
            return this;
        }

        FileSpec platform(String p) {
            this.platform = p;
            return this;
        }

        FileSpec codec(String c) {
            this.codec = c;
            return this;
        }
    }

    /** 构造 manifest 对象树（不含 sig），并把制品填入 transport。 */
    static Map<String, Object> buildManifest(String channel, long version, List<String> managedDirs,
                                             List<FileSpec> specs, MemoryTransport transport) {
        return buildManifest(channel, version, managedDirs, null, specs, transport);
    }

    /**
     * 构造 manifest 对象树（不含 sig），并把制品填入 transport。
     * FR-255：cleanExclude 非空时加入 manifest（omitempty 语义由调用方控制——传 null 即省略）。
     */
    static Map<String, Object> buildManifest(String channel, long version, List<String> managedDirs,
                                             List<String> cleanExclude,
                                             List<FileSpec> specs, MemoryTransport transport) {
        Map<String, Object> manifest = new LinkedHashMap<>();
        manifest.put("schemaVersion", 1L);
        manifest.put("channel", channel);
        manifest.put("version", version);
        manifest.put("issuedAt", "2026-06-23T10:00:00Z");
        manifest.put("managedDirs", new ArrayList<>(managedDirs));
        // FR-255：cleanExclude 仅在非空时加入（omitempty，与 Go manifestToTree 对齐）。
        if (cleanExclude != null && !cleanExclude.isEmpty()) {
            manifest.put("cleanExclude", new ArrayList<>(cleanExclude));
        }

        List<Object> files = new ArrayList<>();
        for (FileSpec spec : specs) {
            byte[] artifact = encode(spec.content, spec.codec);
            String artifactSha = Hashes.sha256(artifact);
            transport.artifacts.put(artifactSha, artifact);

            Map<String, Object> file = new LinkedHashMap<>();
            file.put("path", spec.path);
            file.put("sha256", Hashes.sha256(spec.content));
            file.put("md5", md5(spec.content));
            file.put("size", (long) spec.content.length);
            file.put("sync", spec.sync);
            file.put("platform", spec.platform);

            Map<String, Object> art = new LinkedHashMap<>();
            art.put("sha256", artifactSha);
            art.put("size", (long) artifact.length);
            art.put("codec", spec.codec);
            file.put("artifact", art);

            files.add(file);
        }
        manifest.put("files", files);
        return manifest;
    }

    static byte[] encode(byte[] content, String codec) {
        if ("zstd".equalsIgnoreCase(codec)) {
            return Zstd.compress(content);
        }
        return content;
    }

    static String md5(byte[] data) {
        try {
            return Hashes.hex(java.security.MessageDigest.getInstance("MD5").digest(data));
        } catch (Exception e) {
            throw new RuntimeException(e);
        }
    }

    private TestFixtures() {
    }
}
