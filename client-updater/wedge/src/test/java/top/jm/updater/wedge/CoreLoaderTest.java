package top.jm.updater.wedge;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.File;
import java.io.IOException;
import java.lang.reflect.Method;
import java.net.URL;
import java.net.URLClassLoader;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.HashMap;
import java.util.Map;
import java.util.jar.JarEntry;
import java.util.jar.JarOutputStream;
import javax.tools.JavaCompiler;
import javax.tools.ToolProvider;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * CoreLoader 端到端测试：用真实构建出的 updater-core.jar 验证内存加载 + 反射调用 +
 * 返回码/超时处理（契约 §6.3）。真实 jar 路径经系统属性 {@code jm.updater.core.jar} 注入（见 build.gradle.kts）。
 */
class CoreLoaderTest {

    private File realCoreJar() {
        String p = System.getProperty("jm.updater.core.jar");
        assertTrue(p != null && !p.isEmpty(), "需经 build.gradle.kts 注入 updater-core jar 路径");
        File jar = new File(p);
        assertTrue(jar.isFile(), "updater-core jar 应已构建: " + p);
        return jar;
    }

    @Test
    void missingJarThrows(@TempDir Path tmp) {
        File missing = tmp.resolve("nope.jar").toFile();
        assertThrows(IOException.class,
                () -> CoreLoader.loadAndRun(missing, new HashMap<>(), 5));
    }

    @Test
    void loadsRealCoreAndGetsReturnCode() throws Exception {
        // 真实 Core.run：ctx 缺 channel/endpoint → 立即返回 FAIL_STATIC(1)。
        // 证明内存加载 + 反射 Core.run(Map) + 返回码透传全链路可用。
        Map<String, String> ctx = new HashMap<>();
        ctx.put("gameDir", System.getProperty("java.io.tmpdir"));
        int rc = CoreLoader.loadAndRun(realCoreJar(), ctx, 30);
        assertEquals(1, rc, "缺配置时真实 core 应返回 fail-static(1)，验证反射调用链路");
    }

    @Test
    void packagedCoreJarBundlesZstdForStandaloneLoad() throws Exception {
        // 生产场景：core 经 URLClassLoader 仅以自身 jar URL 加载（无父 classpath 提供 zstd）。
        // 验证打包出的 fat jar 内含 zstd-jni 且能在仅该 jar 的 classloader 下解压——
        // 否则真机解压 zstd 制品会 ClassNotFoundException（此前 thin jar 的隐患）。
        URL[] urls = { realCoreJar().toURI().toURL() };
        // parent = platform loader：不含应用 classpath（即不含本测试的 zstd），逼近真机隔离加载。
        ClassLoader parent = ClassLoader.getSystemClassLoader().getParent();
        try (URLClassLoader loader = new URLClassLoader(urls, parent)) {
            Class<?> zstd = Class.forName("com.github.luben.zstd.Zstd", true, loader);
            byte[] original = "zstd roundtrip via standalone core jar".getBytes(StandardCharsets.UTF_8);

            Method compress = zstd.getMethod("compress", byte[].class);
            byte[] compressed = (byte[]) compress.invoke(null, (Object) original);

            // 用 core 自己的 Codec 解压（同样仅经该 classloader），验证整条解码链路自包含。
            Class<?> codec = Class.forName("top.jm.updater.core.Codec", true, loader);
            Method decode = codec.getDeclaredMethod("decode", byte[].class, String.class);
            decode.setAccessible(true);
            byte[] decoded = (byte[]) decode.invoke(null, compressed, "zstd");

            assertArrayEquals(original, decoded, "打包 core 应能独立加载并解压 zstd");
        }
    }

    @Test
    void timesOutOnSlowCore(@TempDir Path tmp) throws Exception {
        // 构造一个会睡很久的假 core jar，验证超时返回 RESULT_TIMEOUT 且不阻塞放行。
        File slowJar = buildSlowCoreJar(tmp);
        long start = System.currentTimeMillis();
        int rc = CoreLoader.loadAndRun(slowJar, new HashMap<>(), 1);
        long elapsed = System.currentTimeMillis() - start;
        assertEquals(CoreLoader.RESULT_TIMEOUT, rc, "超时应返回 RESULT_TIMEOUT");
        assertTrue(elapsed < 10_000, "超时后应及时返回（约 1s），实际 " + elapsed + "ms");
    }

    @Test
    void loadErrorWhenCoreThrows(@TempDir Path tmp) throws Exception {
        File throwingJar = buildThrowingCoreJar(tmp);
        int rc = CoreLoader.loadAndRun(throwingJar, new HashMap<>(), 5);
        assertEquals(CoreLoader.RESULT_LOAD_ERROR, rc, "core 抛异常逃逸时应返回 RESULT_LOAD_ERROR（fail-static）");
    }

    /** 编译并打包一个 {@code Core.run} 会 sleep 60s 的假 core jar。 */
    private File buildSlowCoreJar(Path tmp) throws Exception {
        String src =
                "package top.jm.updater.core;\n"
                        + "import java.util.Map;\n"
                        + "public final class Core {\n"
                        + "  public static int run(Map<String,String> ctx) {\n"
                        + "    try { Thread.sleep(60000); } catch (InterruptedException e) "
                        + "{ Thread.currentThread().interrupt(); }\n"
                        + "    return 0;\n"
                        + "  }\n"
                        + "}\n";
        return compileToJar(tmp, "slow", src);
    }

    /** 编译并打包一个 {@code Core.run} 抛运行时异常的假 core jar。 */
    private File buildThrowingCoreJar(Path tmp) throws Exception {
        String src =
                "package top.jm.updater.core;\n"
                        + "import java.util.Map;\n"
                        + "public final class Core {\n"
                        + "  public static int run(Map<String,String> ctx) {\n"
                        + "    throw new RuntimeException(\"boom\");\n"
                        + "  }\n"
                        + "}\n";
        return compileToJar(tmp, "throwing", src);
    }

    private File compileToJar(Path tmp, String name, String src) throws Exception {
        Path workDir = tmp.resolve(name);
        Path pkgDir = workDir.resolve("top/jm/updater/core");
        Files.createDirectories(pkgDir);
        Path srcFile = pkgDir.resolve("Core.java");
        Files.write(srcFile, src.getBytes("UTF-8"));

        JavaCompiler compiler = ToolProvider.getSystemJavaCompiler();
        assertTrue(compiler != null, "需要 JDK（含 javac）运行此测试");
        int res = compiler.run(null, null, null, srcFile.toString());
        assertEquals(0, res, "假 core 编译应成功");

        Path classFile = pkgDir.resolve("Core.class");
        File jar = workDir.resolve(name + "-core.jar").toFile();
        try (JarOutputStream jos = new JarOutputStream(Files.newOutputStream(jar.toPath()))) {
            jos.putNextEntry(new JarEntry("top/jm/updater/core/Core.class"));
            jos.write(Files.readAllBytes(classFile));
            jos.closeEntry();
        }
        return jar;
    }
}
