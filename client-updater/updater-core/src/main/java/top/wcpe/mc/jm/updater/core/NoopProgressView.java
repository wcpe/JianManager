package top.wcpe.mc.jm.updater.core;

/** 不展示进度（FR-099，ctx {@code progressUi=false} 时）。 */
final class NoopProgressView implements ProgressView {

    @Override
    public void show() {
    }

    @Override
    public void onProgress() {
    }

    @Override
    public boolean isCancelled() {
        return false;
    }

    @Override
    public void close() {
    }
}
