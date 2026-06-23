package top.wcpe.mc.jm.updater.core;

import java.util.function.LongConsumer;

/**
 * 更新进度上报（FR-099）：连接下载逻辑（{@link Reconciler}/{@link SelfUpdater}）与展示（{@link ProgressView}）。
 *
 * <p>持 {@link ProgressModel}（数据）+ {@link ProgressView}（展示）。下载侧调用 {@link #plan}/{@link #beginFile}/
 * {@link #sink}；窗口在首个 {@link #beginFile} 时惰性显示（无下载则不弹）。玩家关窗 → {@link #isCancelled()}。
 */
final class ProgressReporter implements AutoCloseable {

    private final ProgressModel model;
    private final ProgressView view;
    private volatile boolean shown;

    private ProgressReporter(ProgressModel model, ProgressView view) {
        this.model = model;
        this.view = view;
    }

    /** 按环境装配（enabled=false→Noop；headless→文本；否则 Swing）。 */
    static ProgressReporter create(CoreMessages msg, Logger log, boolean enabled) {
        ProgressModel model = new ProgressModel();
        return new ProgressReporter(model, ProgressView.create(model, msg, log, enabled));
    }

    /** 累加总下载字节（分母）。 */
    void plan(long bytes) {
        model.addTotal(bytes);
    }

    /** 开始下载一个文件：置当前文件名 + 惰性显示窗口。 */
    void beginFile(String path) {
        model.setCurrentFile(path);
        if (!shown) {
            shown = true;
            view.show();
        }
    }

    /**
     * 下载分块字节回调（传给 {@link Transport#fetchArtifact(String, LongConsumer)}）。
     * 玩家已关窗 → 抛运行时异常中止当前下载（reconcile 据此记错、停后续、fail-static）。
     */
    LongConsumer sink() {
        return delta -> {
            if (view.isCancelled()) {
                throw new ProgressCancelledException();
            }
            model.advance(delta, System.currentTimeMillis());
            view.onProgress();
        };
    }

    /** 玩家是否已关窗请求取消。 */
    boolean isCancelled() {
        return view.isCancelled();
    }

    @Override
    public void close() {
        try {
            model.snapToComplete();
        } catch (Throwable ignore) {
            // 收尾失败无害。
        }
        view.close();
    }

    /** 取消信号（玩家关窗）——中止当前下载。 */
    static final class ProgressCancelledException extends RuntimeException {
        ProgressCancelledException() {
            super("用户取消更新");
        }
    }
}
