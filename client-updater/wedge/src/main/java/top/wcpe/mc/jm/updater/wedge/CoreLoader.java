package top.wcpe.mc.jm.updater.wedge;

import java.io.File;
import java.io.IOException;
import java.lang.reflect.Method;
import java.net.URL;
import java.net.URLClassLoader;
import java.nio.file.Files;
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.Callable;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.ThreadFactory;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;

/**
 * 动态加载 updater-core 并调用入口（契约 §6.3）。
 *
 * <p>以独立 {@link URLClassLoader} <b>内存加载</b> core jar（读 jar 字节经临时副本 URL，
 * 避免持续锁住原 jar，便于 FR-091 自更新替换），反射 {@code top.wcpe.mc.jm.updater.core.Core.run(Map)}，
 * 同步等待 + 超时（契约 §6.3）。
 *
 * <p>抽为独立类便于单测：用真实构建出的 updater-core.jar 端到端验证加载与反射调用。
 */
final class CoreLoader {

    /** 与 updater-core 约定的返回码（契约 §6.3）。 */
    static final int RESULT_OK = 0;
    static final int RESULT_TIMEOUT = -100;
    static final int RESULT_LOAD_ERROR = -101;

    private static final String CORE_CLASS = "top.wcpe.mc.jm.updater.core.Core";

    private CoreLoader() {
    }

    /**
     * 加载 core jar、反射调用 {@code Core.run(ctx)}，最多等待 {@code timeoutSec} 秒。
     *
     * @return core 返回码（0=成功）；超时返回 {@link #RESULT_TIMEOUT}；加载/反射失败返回 {@link #RESULT_LOAD_ERROR}
     * @throws IOException core jar 不存在/不可读
     */
    static int loadAndRun(File coreJar, Map<String, String> ctx, int timeoutSec) throws IOException {
        if (!coreJar.isFile()) {
            throw new IOException("updater-core jar 不存在: " + coreJar.getAbsolutePath());
        }
        // 内存加载：复制 jar 字节到临时文件作为 URLClassLoader 源，原 jar 不被持续锁（便于自更新替换）。
        File tempJar = File.createTempFile("jm-updater-core", ".jar");
        tempJar.deleteOnExit();
        Files.copy(coreJar.toPath(), tempJar.toPath(),
                java.nio.file.StandardCopyOption.REPLACE_EXISTING);

        URL[] urls = { tempJar.toURI().toURL() };
        // parent = 平台类加载器避免污染：core 仅需 JDK + 自带依赖（打入 core jar / 由其自身解析）。
        ExecutorService exec = Executors.newSingleThreadExecutor(new ThreadFactory() {
            @Override
            public Thread newThread(Runnable r) {
                Thread t = new Thread(r, "jm-updater-core");
                t.setDaemon(true); // 楔子超时放行后不阻止 JVM 进入游戏。
                return t;
            }
        });
        URLClassLoader loader = new URLClassLoader(urls, CoreLoader.class.getClassLoader());
        try {
            Future<Integer> future = exec.submit(new Callable<Integer>() {
                @Override
                public Integer call() throws Exception {
                    Class<?> coreClass = Class.forName(CORE_CLASS, true, loader);
                    Method run = coreClass.getMethod("run", Map.class);
                    Object ret = run.invoke(null, new HashMap<String, String>(ctx));
                    return ret instanceof Integer ? (Integer) ret : RESULT_OK;
                }
            });
            try {
                return future.get(timeoutSec, TimeUnit.SECONDS);
            } catch (TimeoutException e) {
                future.cancel(true);
                return RESULT_TIMEOUT;
            } catch (ExecutionException e) {
                // core 内部本应自兜底；若仍逃逸异常，按加载/执行错误 fail-static。
                return RESULT_LOAD_ERROR;
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return RESULT_TIMEOUT;
            }
        } finally {
            exec.shutdownNow();
            try {
                loader.close();
            } catch (IOException ignore) {
                // 关闭失败不影响放行。
            }
        }
    }
}
