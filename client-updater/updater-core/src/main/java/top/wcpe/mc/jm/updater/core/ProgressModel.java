package top.wcpe.mc.jm.updater.core;

import java.util.ArrayDeque;
import java.util.Deque;

/**
 * 更新下载进度模型（FR-099）。
 *
 * <p>纯逻辑、线程安全（下载线程 {@code advance}，UI 线程读 {@code fraction/speed/eta}）、时间戳注入便于单测。
 *
 * <ul>
 *   <li>分母 {@code totalBytes}：增量累加（reconcile 预估 + core 自更新追加）。</li>
 *   <li>分子 {@code doneBytes}：下载分块回调推进。</li>
 *   <li>速度：最近 {@value #SPEED_WINDOW_MS} ms 滑动窗口样本端点差分；ETA = 剩余/速度。</li>
 * </ul>
 */
final class ProgressModel {

    /** 速度滑动窗口宽度（ms）。 */
    static final long SPEED_WINDOW_MS = 3000L;

    private long totalBytes;
    private long doneBytes;
    private String currentFile = "";

    /** 速度样本 {@code (timeMs, doneBytes)}；按时间淘汰窗口外，单 ms 内合并以限内存。 */
    private final Deque<long[]> samples = new ArrayDeque<>();

    /** 累加总字节（分母）。仅正数生效。 */
    synchronized void addTotal(long bytes) {
        if (bytes > 0) {
            totalBytes += bytes;
        }
    }

    /** 设置当前正在下载的文件名（展示用）。 */
    synchronized void setCurrentFile(String name) {
        this.currentFile = name == null ? "" : name;
    }

    /** 推进已下字节并记一个速度样本。{@code nowMs} 注入便于确定性测试。 */
    synchronized void advance(long deltaBytes, long nowMs) {
        if (deltaBytes > 0) {
            doneBytes += deltaBytes;
        }
        long[] last = samples.peekLast();
        if (last != null && last[0] == nowMs) {
            last[1] = doneBytes; // 同 ms 合并，限样本量。
        } else {
            samples.addLast(new long[] { nowMs, doneBytes });
        }
        evict(nowMs);
    }

    /** 总体完成比例 [0,1]；总量未知（0）时为 0。 */
    synchronized double fraction() {
        if (totalBytes <= 0) {
            return 0.0;
        }
        double f = (double) doneBytes / (double) totalBytes;
        if (f < 0) {
            return 0.0;
        }
        return f > 1.0 ? 1.0 : f;
    }

    /** 当前速度（字节/秒）；样本不足或停滞为 0。 */
    synchronized long speedBytesPerSec(long nowMs) {
        evict(nowMs);
        if (samples.size() < 2) {
            return 0;
        }
        long[] first = samples.peekFirst();
        long[] lastS = samples.peekLast();
        long dt = lastS[0] - first[0];
        if (dt <= 0) {
            return 0;
        }
        long db = lastS[1] - first[1];
        if (db <= 0) {
            return 0;
        }
        return db * 1000L / dt;
    }

    /** 预计剩余秒数；速度为 0/未知返回 -1；已完成返回 0。 */
    synchronized long etaSeconds(long nowMs) {
        long remaining = totalBytes - doneBytes;
        if (remaining <= 0) {
            return totalBytes > 0 ? 0 : -1;
        }
        long spd = speedBytesPerSec(nowMs);
        if (spd <= 0) {
            return -1;
        }
        return (remaining + spd - 1) / spd; // 向上取整。
    }

    /** 收尾时把已下置满总量（消除「同大小不同 md5」等预估误差导致的尾部偏差）。 */
    synchronized void snapToComplete() {
        if (totalBytes > doneBytes) {
            doneBytes = totalBytes;
        }
    }

    synchronized long totalBytes() {
        return totalBytes;
    }

    synchronized long doneBytes() {
        return doneBytes;
    }

    synchronized String currentFile() {
        return currentFile;
    }

    /** 淘汰窗口外样本，至少保留 1 个（最新）。 */
    private void evict(long nowMs) {
        long cutoff = nowMs - SPEED_WINDOW_MS;
        while (samples.size() > 1 && samples.peekFirst()[0] < cutoff) {
            samples.pollFirst();
        }
    }
}
