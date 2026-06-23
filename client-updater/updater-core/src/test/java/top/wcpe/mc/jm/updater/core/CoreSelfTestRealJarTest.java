package top.wcpe.mc.jm.updater.core;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.File;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * 生产 selftest 对真实构建出的 core fat jar 的端到端验证（FR-091）。
 * 真 jar 路径经 build.gradle.kts 注入系统属性 {@code jm.updater.core.jar}。
 * 证明 {@link UrlClassLoaderSelfTest} 能以独立 classloader 加载真 jar、调 {@code Core.selfTest()}（含 zstd 解码链路）通过；
 * 并对损坏 jar 判否——保证自更新绝不切到坏 core。
 */
class CoreSelfTestRealJarTest {

    private File realCoreJar() {
        String p = System.getProperty("jm.updater.core.jar");
        assertTrue(p != null && !p.isEmpty(), "需经 build.gradle.kts 注入 updater-core jar 路径");
        File jar = new File(p);
        assertTrue(jar.isFile(), "updater-core jar 应已构建: " + p);
        return jar;
    }

    @Test
    void acceptsRealBuiltCoreJar() {
        assertTrue(new UrlClassLoaderSelfTest().test(realCoreJar().toPath()),
                "真实构建的 core fat jar selftest 应通过（含 zstd 解码自检）");
    }

    @Test
    void rejectsGarbageJar(@TempDir Path tmp) throws Exception {
        Path bad = tmp.resolve("bad.jar");
        Files.write(bad, "this is not a valid jar".getBytes(StandardCharsets.UTF_8));
        assertFalse(new UrlClassLoaderSelfTest().test(bad), "损坏 jar selftest 必须判否");
    }

    @Test
    void rejectsMissingJar(@TempDir Path tmp) {
        assertFalse(new UrlClassLoaderSelfTest().test(tmp.resolve("nope.jar")),
                "不存在的 jar selftest 必须判否");
    }
}
