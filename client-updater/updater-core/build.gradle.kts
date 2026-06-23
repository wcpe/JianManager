// updater-core：更新主体，被楔子动态加载（URLClassLoader 内存加载，便于自更新换 jar）。
// target Java 8：须能被低版本（Java 8）MC 的 JVM 加载——老整合包/启动器仍在用 Java 8，
// 若编到 17 则 UnsupportedClassVersionError、楔子加载失败（见 FR-089 真机）。
// 代价：Java 8 无 java.net.http（改用 HttpURLConnection）、无内置 Ed25519（JDK15+，改引 BouncyCastle）。
// 仍只用 JDK 自带能力 + 轻量 JSON（自写）+ zstd 解压 + BouncyCastle（Ed25519 验签）。
java {
    sourceCompatibility = JavaVersion.VERSION_1_8
    targetCompatibility = JavaVersion.VERSION_1_8
}

// 用 --release 8 强制按 Java 8 API 编译：若误用 Java 9+ API（如 java.net.http、List.of）直接编译失败，
// 而非 source/target 那样编过却在真 Java 8 上运行期崩。
tasks.withType<JavaCompile>().configureEach {
    options.release.set(8)
}

repositories {
    mavenCentral()
}

dependencies {
    // 制品按 contract §2 artifact.codec=zstd 压缩；zstd-jni 是轻量、广用的 zstd 绑定（兼容 Java 8）。
    implementation("com.github.luben:zstd-jni:1.5.6-4")
    // manifest/.jmpack Ed25519 验签（ADR-022）。JDK 内置 EdDSA 自 15 起才有，Java 8 须引入。
    // bcprov-jdk18on = JDK 1.8+ 构建；以 Provider 实例直用（不全局注册），打进 fat jar。
    implementation("org.bouncycastle:bcprov-jdk18on:1.78.1")

    testImplementation("org.junit.jupiter:junit-jupiter:5.10.2")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

tasks.test {
    useJUnitPlatform()
    // FR-091 自更新 selftest 需以独立 classloader 加载真实构建出的 core jar 自证可用，
    // 故把自身 jar 制品路径注入测试（CoreSelfTestRealJarTest）。test 依赖 jar 不成环（jar 不依赖 test）。
    dependsOn(tasks.named("jar"))
    val selfJar = tasks.named("jar")
    inputs.files(selfJar)
    doFirst {
        systemProperty("jm.updater.core.jar", selfJar.get().outputs.files.singleFile.absolutePath)
    }
    testLogging {
        events("passed", "skipped", "failed")
    }
}

// 楔子经 URLClassLoader 仅以 core jar 自身的 URL 动态加载 updater-core（契约 §6.3），
// 故 core 必须自包含运行时依赖（zstd-jni）——否则真机解压 zstd 制品时 ClassNotFoundException。
// 用内置能力打 fat jar（不引 shadow 插件，保持构建零额外插件依赖）。
tasks.jar {
    duplicatesStrategy = DuplicatesStrategy.EXCLUDE
    from({
        configurations.runtimeClasspath.get()
            .filter { it.name.endsWith("jar") }
            .map { zipTree(it) }
    })
    // 排除被打包依赖自身的签名/模块描述，避免 SecurityException / 多 module-info 冲突。
    exclude("META-INF/*.SF", "META-INF/*.DSA", "META-INF/*.RSA", "META-INF/versions/**/module-info.class")
}
