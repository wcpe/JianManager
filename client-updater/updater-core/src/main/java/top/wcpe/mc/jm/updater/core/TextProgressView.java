package top.wcpe.mc.jm.updater.core;

import java.util.Locale;

/**
 * 文本进度（FR-099 headless / 降级路径）：节流写日志（控制台 + updater.log），不弹窗。
 *
 * <p>服务端驱动 / 无显示环境用此，绝不因无 GUI 报错或阻断。
 */
final class TextProgressView implements ProgressView {

    /** 两条进度日志最小间隔，避免高频刷屏。 */
    private static final long THROTTLE_MS = 800L;

    private final ProgressModel model;
    private final CoreMessages msg;
    private final Logger log;

    private volatile boolean shown;
    private volatile long lastLogMs;

    TextProgressView(ProgressModel model, CoreMessages msg, Logger log) {
        this.model = model;
        this.msg = msg;
        this.log = log;
    }

    @Override
    public void show() {
        if (!shown) {
            shown = true;
            log.info(msg.downloading());
        }
    }

    @Override
    public void onProgress() {
        long now = System.currentTimeMillis();
        if (now - lastLogMs < THROTTLE_MS) {
            return;
        }
        lastLogMs = now;
        int pct = (int) Math.round(model.fraction() * 100);
        long spd = model.speedBytesPerSec(now);
        long eta = model.etaSeconds(now);
        log.info(String.format(Locale.US, "[进度] %d%% %s %s %s",
                pct, CoreMessages.humanSpeed(spd), msg.eta(eta), shortName(model.currentFile())));
    }

    @Override
    public boolean isCancelled() {
        return false;
    }

    @Override
    public void close() {
        if (shown) {
            log.info(msg.complete());
        }
    }

    /** 仅取末段文件名，避免长路径刷屏。 */
    private static String shortName(String path) {
        if (path == null || path.isEmpty()) {
            return "";
        }
        int i = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'));
        return i >= 0 && i < path.length() - 1 ? path.substring(i + 1) : path;
    }
}
