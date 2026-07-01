package top.wcpe.mc.jm.updater.core;

import java.io.IOException;
import java.io.InputStream;
import java.util.function.LongConsumer;

/**
 * manifest / 制品拉取抽象（契约 §4 端点）。
 *
 * <p>生产实现 {@link HttpTransport} 走 {@code java.net.http} + 拉取密钥/机器码请求头；
 * 测试以本地文件系统实现替身，从而 reconcile 逻辑可在临时目录端到端验证（无需真端点）。
 */
interface Transport {

    /** 拉取频道 latest manifest 的 JSON 文本。端点不可达抛 {@link IOException}（触发 fail-static）。 */
    String fetchManifest() throws IOException;

    /** 按制品 sha256 拉取制品字节（可能为 zstd 压缩流，按 manifest codec 解码）。 */
    byte[] fetchArtifact(String artifactSha256) throws IOException;

    /**
     * 带下载进度回调的制品拉取（FR-099）。{@code onBytes} 按读入的每个分块字节数回调（可空）。
     * 默认实现不流式（一次性读完后报总量），生产 {@link HttpTransport} 重写为分块流式上报。
     */
    default byte[] fetchArtifact(String artifactSha256, LongConsumer onBytes) throws IOException {
        byte[] b = fetchArtifact(artifactSha256);
        if (onBytes != null && b != null) {
            onBytes.accept((long) b.length);
        }
        return b;
    }

    /**
     * 流式拉取制品（FR-257）：返回 InputStream 供调用方边读边写盘（64KB 缓冲），不再全量读进 byte[]。
     * {@code offset>0} 时以 {@code Range: bytes=<offset>-} 从断点续传（HTTP 206）。
     * 调用方负责关闭返回的 InputStream（HttpTransport 实现会在 close 时断开底层连接）。
     * 进度上报由调用方在读取循环中按分块字节数回调 {@link ProgressReporter#sink()}。
     */
    InputStream fetchArtifactStream(String artifactSha256, long offset) throws IOException;

    /** 上报遥测（FR-094，契约 §4.3）。**best-effort**：端点不可达/非 202 静默忽略，绝不抛逃逸。 */
    void postTelemetry(String jsonBody);
}
