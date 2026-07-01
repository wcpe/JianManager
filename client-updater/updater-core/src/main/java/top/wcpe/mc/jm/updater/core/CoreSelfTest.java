package top.wcpe.mc.jm.updater.core;

import java.nio.file.Path;

/**
 * 新下载 core jar 的自检（FR-091）：切换前确认其可加载、ABI 完整。
 * 生产实现 {@link UrlClassLoaderSelfTest}，供 {@code CoreSelfTestRealJarTest} 验证真实构建的
 * core fat jar 可被独立 classloader 加载、{@code Core.selfTest()}（含 zstd 解码）通过。
 */
interface CoreSelfTest {

    /** 自检通过返回 true；任何加载/反射/执行异常都应收敛为 false（绝不抛出）。 */
    boolean test(Path coreJar);
}
