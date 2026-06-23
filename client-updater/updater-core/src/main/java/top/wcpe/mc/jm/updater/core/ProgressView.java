package top.wcpe.mc.jm.updater.core;

/**
 * 更新进度展示（FR-099）。实现按环境选择：有显示 → {@link SwingProgressView} 独立窗口；
 * headless / 禁用 → {@link TextProgressView} 文本 / {@link NoopProgressView}。
 *
 * <p>所有实现对异常 fail-open：展示失败绝不阻断更新/游戏（契约 ADR-021）。
 */
interface ProgressView extends AutoCloseable {

    /** 惰性显示（reporter 在首个下载文件时调用）。 */
    void show();

    /** 一次进度推进通知（实现自行节流/重绘）。 */
    void onProgress();

    /** 玩家是否请求取消（关窗）。文本/Noop 恒为 false。 */
    boolean isCancelled();

    @Override
    void close();

    /**
     * 按环境选择实现。{@code enabled=false} → Noop；headless → 文本；否则 Swing。
     * 任意创建异常都降级文本（绝不抛逃逸）。
     */
    static ProgressView create(ProgressModel model, CoreMessages msg, Logger log, boolean enabled) {
        if (!enabled) {
            return new NoopProgressView();
        }
        try {
            if (java.awt.GraphicsEnvironment.isHeadless()) {
                return new TextProgressView(model, msg, log);
            }
            return new SwingProgressView(model, msg, log);
        } catch (Throwable t) {
            log.warn("进度窗口初始化失败，降级文本: " + t);
            return new TextProgressView(model, msg, log);
        }
    }
}
