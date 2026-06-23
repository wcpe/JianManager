package top.jm.updater.core;

import java.nio.file.Path;

/**
 * 新下载 core jar 的自检（FR-091）：切换前确认其可加载、ABI 完整。
 * 生产实现 {@link UrlClassLoaderSelfTest}；测试可注入桩以验证 {@link SelfUpdater} 的下载/校验/暂存逻辑。
 */
interface CoreSelfTest {

    /** 自检通过返回 true；任何加载/反射/执行异常都应收敛为 false（绝不抛出）。 */
    boolean test(Path coreJar);
}
