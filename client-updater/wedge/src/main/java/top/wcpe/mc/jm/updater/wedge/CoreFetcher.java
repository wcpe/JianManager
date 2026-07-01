package top.wcpe.mc.jm.updater.wedge;

import java.io.File;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.StandardCopyOption;
import java.security.MessageDigest;
import java.util.Map;

/**
 * core jar 拉取器（FR-258 楔子 gradle-wrapper 模式）。
 *
 * <p>JDK 原生实现（{@link HttpURLConnection} + {@link MessageDigest}），零外部依赖。
 * 负责：①查询 coreEndpoint 获取版本信息；②下载 core jar + sha256 校验。
 *
 * <p>协议见 {@code docs/specs/updater-arch-simplification/spec.md} §2.5/§3.1。
 */
final class CoreFetcher {

    /** 连接超时（毫秒）。 */
    static final int CONNECT_TIMEOUT_MS = 10_000;
    /** 读取超时（毫秒）。 */
    static final int READ_TIMEOUT_MS = 60_000;
    /** 最大尝试次数（含首次 = 重试 1 次）。 */
    private static final int MAX_ATTEMPTS = 2;
    private static final int BUF_SIZE = 8192;

    /** coreEndpoint 返回的版本信息（§2.5.3 冻结格式：只能加字段不能删/改）。 */
    static final class CoreEndpointInfo {
        final long version;
        final String sha256;
        final String downloadUrl;
        final long size;

        private CoreEndpointInfo(long version, String sha256, String downloadUrl, long size) {
            this.version = version;
            this.sha256 = sha256;
            this.downloadUrl = downloadUrl;
            this.size = size;
        }

        /** 从 JSON 文本解析（扁平对象，复用 {@link MiniJson}）。 */
        static CoreEndpointInfo fromJson(String json) {
            Map<String, String> m = MiniJson.parseFlatObject(json);
            return new CoreEndpointInfo(
                    parseLongOr(m.get("version"), 0),
                    m.get("sha256"),
                    m.get("downloadUrl"),
                    parseLongOr(m.get("size"), 0));
        }
    }

    /** sha256 校验失败（不重试，内容错误重试无意义）。 */
    static final class Sha256MismatchException extends IOException {
        Sha256MismatchException(String expected, String actual) {
            super("sha256 校验失败: 期望=" + expected + " 实际=" + actual);
        }
    }

    private CoreFetcher() {
    }

    // ---- 查询 coreEndpoint ----

    /** 查询 coreEndpoint 获取当前 core 版本信息；不可达/出错返回 null（best-effort）。 */
    static CoreEndpointInfo fetchInfo(String coreEndpoint, String key) {
        return fetchInfo(coreEndpoint, key, CONNECT_TIMEOUT_MS, READ_TIMEOUT_MS);
    }

    /** 带超时参数的重载（供单测使用短超时）。 */
    static CoreEndpointInfo fetchInfo(String coreEndpoint, String key, int connectMs, int readMs) {
        if (coreEndpoint == null || coreEndpoint.isEmpty()) {
            return null;
        }
        for (int attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
            HttpURLConnection conn = null;
            try {
                conn = openGet(coreEndpoint, key, connectMs, readMs);
                int code = conn.getResponseCode();
                if (code != 200) {
                    conn.disconnect();
                    return null; // 非 200 不重试（服务端错误）
                }
                String body = readAll(conn.getInputStream());
                conn.disconnect();
                return CoreEndpointInfo.fromJson(body);
            } catch (Exception e) {
                if (conn != null) {
                    try { conn.disconnect(); } catch (Exception ignore) { /* 关闭失败忽略 */ }
                }
                // 网络错误，重试
            }
        }
        return null;
    }

    // ---- 下载 core jar ----

    /**
     * 下载 core jar 到 {@code coreDir/<sha>.jar}，sha256 校验后原子 rename。
     * 如果目标 jar 已存在（同 sha256）直接返回。失败抛 {@link IOException}。
     */
    static File downloadJar(CoreEndpointInfo info, String key, File coreDir) throws IOException {
        return downloadJar(info, key, coreDir, CONNECT_TIMEOUT_MS, READ_TIMEOUT_MS);
    }

    /** 带超时参数的重载（供单测使用短超时）。 */
    static File downloadJar(CoreEndpointInfo info, String key, File coreDir,
                            int connectMs, int readMs) throws IOException {
        File target = new File(coreDir, info.sha256 + ".jar");
        if (target.isFile()) {
            return target; // 同 sha256 已下载过
        }

        IOException lastError = null;
        for (int attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
            File tmpFile = null;
            HttpURLConnection conn = null;
            try {
                conn = openGet(info.downloadUrl, key, connectMs, readMs);
                int code = conn.getResponseCode();
                if (code != 200) {
                    conn.disconnect();
                    throw new IOException("下载 core jar 失败，HTTP " + code);
                }

                coreDir.mkdirs();
                tmpFile = File.createTempFile("jm-core-", ".tmp", coreDir);
                tmpFile.deleteOnExit();

                MessageDigest md = newSha256(); // SHA-256 由 JDK 规范保证可用
                try (InputStream in = conn.getInputStream();
                     OutputStream out = Files.newOutputStream(tmpFile.toPath())) {
                    byte[] buf = new byte[BUF_SIZE];
                    int n;
                    while ((n = in.read(buf)) != -1) {
                        out.write(buf, 0, n);
                        md.update(buf, 0, n);
                    }
                }
                conn.disconnect();

                String actualSha = hexEncode(md.digest());
                if (!actualSha.equalsIgnoreCase(info.sha256)) {
                    tmpFile.delete();
                    throw new Sha256MismatchException(info.sha256, actualSha);
                }

                atomicMove(tmpFile, target);
                return target;
            } catch (Sha256MismatchException e) {
                throw e; // sha256 不符不重试
            } catch (IOException e) {
                lastError = e;
                if (tmpFile != null) {
                    tmpFile.delete();
                }
                if (conn != null) {
                    try { conn.disconnect(); } catch (Exception ignore) { /* 关闭失败忽略 */ }
                }
                // 网络错误，重试
            }
        }
        throw lastError != null ? lastError : new IOException("下载 core jar 失败");
    }

    // ---- 内部工具 ----

    /** 创建 SHA-256 MessageDigest（JDK 规范保证所有实现都支持）。 */
    private static MessageDigest newSha256() {
        try {
            return MessageDigest.getInstance("SHA-256");
        } catch (java.security.NoSuchAlgorithmException e) {
            // 不可能发生：SHA-256 是 JDK 标准算法
            throw new RuntimeException("SHA-256 不可用", e);
        }
    }

    private static HttpURLConnection openGet(String urlStr, String key, int connectMs, int readMs)
            throws IOException {
        HttpURLConnection conn = (HttpURLConnection) new URL(urlStr).openConnection();
        conn.setRequestMethod("GET");
        conn.setConnectTimeout(connectMs);
        conn.setReadTimeout(readMs);
        conn.setInstanceFollowRedirects(true);
        if (key != null && !key.isEmpty()) {
            conn.setRequestProperty("X-Client-Key", key); // 拉取密钥鉴权（与 manifest 端点同级）
        }
        return conn;
    }

    private static String readAll(InputStream in) throws IOException {
        ByteArrayOutputStreamLite buf = new ByteArrayOutputStreamLite();
        byte[] b = new byte[BUF_SIZE];
        int n;
        while ((n = in.read(b)) != -1) {
            buf.write(b, 0, n);
        }
        return new String(buf.toByteArray(), StandardCharsets.UTF_8);
    }

    private static void atomicMove(File source, File target) throws IOException {
        try {
            Files.move(source.toPath(), target.toPath(),
                    StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
        } catch (java.nio.file.AtomicMoveNotSupportedException e) {
            Files.move(source.toPath(), target.toPath(), StandardCopyOption.REPLACE_EXISTING);
        }
    }

    private static final char[] HEX = "0123456789abcdef".toCharArray();

    private static String hexEncode(byte[] bytes) {
        char[] out = new char[bytes.length * 2];
        for (int i = 0; i < bytes.length; i++) {
            int v = bytes[i] & 0xFF;
            out[i * 2] = HEX[v >>> 4];
            out[i * 2 + 1] = HEX[v & 0x0F];
        }
        return new String(out);
    }

    private static long parseLongOr(String s, long def) {
        if (s == null || s.trim().isEmpty()) {
            return def;
        }
        try {
            return Long.parseLong(s.trim());
        } catch (NumberFormatException e) {
            return def;
        }
    }

    /** 极简 ByteArrayOutputStream（避免 java.io.ByteArrayOutputStream 的同步开销）。 */
    private static final class ByteArrayOutputStreamLite {
        private byte[] buf = new byte[BUF_SIZE];
        private int count;

        void write(byte[] b, int off, int len) {
            int newCount = count + len;
            if (newCount > buf.length) {
                byte[] newBuf = new byte[Math.max(buf.length << 1, newCount)];
                System.arraycopy(buf, 0, newBuf, 0, count);
                buf = newBuf;
            }
            System.arraycopy(b, off, buf, count, len);
            count = newCount;
        }

        byte[] toByteArray() {
            byte[] out = new byte[count];
            System.arraycopy(buf, 0, out, 0, count);
            return out;
        }
    }
}
