// updater-core：更新主体，被楔子动态加载（URLClassLoader 内存加载，便于自更新换 jar）。
// target Java 17：用 java.net.http、内置 EdDSA（Ed25519 验签）；现代整合包自带 JRE 17+。
// 仅 JDK 自带能力 + 轻量 JSON（自写零依赖）+ zstd 制品解压（contract §4 codec=zstd）。
java {
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}

repositories {
    mavenCentral()
}

dependencies {
    // 制品按 contract §2 artifact.codec=zstd 压缩；zstd-jni 是轻量、广用的 zstd 绑定。
    implementation("com.github.luben:zstd-jni:1.5.6-4")

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
