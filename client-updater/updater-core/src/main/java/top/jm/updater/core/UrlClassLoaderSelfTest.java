package top.jm.updater.core;

import java.lang.reflect.Method;
import java.net.URL;
import java.net.URLClassLoader;
import java.nio.file.Path;
import java.util.Map;

/**
 * 生产自检（FR-091）：以独立 {@link URLClassLoader}（parent=平台加载器，<b>不</b>委派当前 core 的类）
 * 加载新 jar 的 {@code top.jm.updater.core.Core}，校验 {@code run(Map)} 入口存在、并调用其
 * {@code selfTest()} 返回 OK——一并验证 fat jar 内置依赖（zstd 等）可在仅该 jar 的加载器下解析。
 *
 * <p>parent 取平台加载器而非当前 core 加载器：否则父优先委派会命中<b>正在运行的旧 Core</b>，
 * 测不到新 jar。
 */
final class UrlClassLoaderSelfTest implements CoreSelfTest {

    @Override
    public boolean test(Path coreJar) {
        try {
            URL[] urls = { coreJar.toUri().toURL() };
            // 平台加载器无应用类 → top.jm.updater.core.* 必从新 jar 加载（非委派旧版）。
            ClassLoader parent = ClassLoader.getSystemClassLoader().getParent();
            try (URLClassLoader loader = new URLClassLoader(urls, parent)) {
                Class<?> coreClass = Class.forName("top.jm.updater.core.Core", true, loader);
                // 入口必须存在（ABI 完整）。
                coreClass.getMethod("run", Map.class);
                Method selfTest = coreClass.getMethod("selfTest");
                Object rc = selfTest.invoke(null);
                return rc instanceof Integer && (Integer) rc == Core.SELFTEST_OK;
            }
        } catch (Throwable t) {
            // 任何加载/反射/执行失败都判不通过——绝不切换到坏 core。
            return false;
        }
    }
}
