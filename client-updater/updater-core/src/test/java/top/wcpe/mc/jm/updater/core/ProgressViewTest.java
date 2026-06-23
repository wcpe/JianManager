package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

/** 进度视图工厂选择（FR-099）：禁用→Noop；headless→文本；展示操作不抛。 */
class ProgressViewTest {

    @Test
    void disabledGivesNoop() {
        ProgressView v = ProgressView.create(
                new ProgressModel(), CoreMessages.forLanguage("en"), Logger.consoleOnly(), false);
        assertTrue(v instanceof NoopProgressView);
        assertFalse(v.isCancelled());
        // 全程不抛（fail-open）。
        v.show();
        v.onProgress();
        v.close();
    }

    @Test
    void headlessGivesText() {
        // test 任务设了 -Djava.awt.headless=true。
        assumeTrue(java.awt.GraphicsEnvironment.isHeadless(), "需 headless 环境");
        ProgressView v = ProgressView.create(
                new ProgressModel(), CoreMessages.forLanguage("zh"), Logger.consoleOnly(), true);
        assertTrue(v instanceof TextProgressView);
        assertFalse(v.isCancelled());
        v.show();
        v.onProgress();
        v.close();
    }
}
