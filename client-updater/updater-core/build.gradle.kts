import java.io.ByteArrayOutputStream
import java.time.Instant

// updater-core：更新主体，被楔子动态加载（URLClassLoader 内存加载，便于自更新换 jar）。
// target Java 8：须能被低版本（Java 8）MC 的 JVM 加载——老整合包/启动器仍在用 Java 8，
// 若编到 17 则 UnsupportedClassVersionError、楔子加载失败（见 FR-089 真机）。
// 代价：Java 8 无 java.net.http（改用 HttpURLConnection）。
// FR-256 起去掉 Ed25519 验签（BouncyCastle 依赖移除），信任靠 HTTPS + 拉取密钥鉴权。
// 仍只用 JDK 自带能力 + 轻量 JSON（自写）+ zstd 解压。
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

    testImplementation("org.junit.jupiter:junit-jupiter:5.10.2")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

fun gitRootDir() = rootProject.projectDir.parentFile ?: rootProject.projectDir

fun gitOutput(vararg args: String): String {
    val out = ByteArrayOutputStream()
    val result = exec {
        workingDir = gitRootDir()
        commandLine("git", *args)
        standardOutput = out
        errorOutput = ByteArrayOutputStream()
        isIgnoreExitValue = true
    }
    if (result.exitValue != 0) {
        return "unknown"
    }
    return out.toString(Charsets.UTF_8.name()).trim().ifBlank { "unknown" }
}

fun gitDirty(): String {
    val result = exec {
        workingDir = gitRootDir()
        commandLine("git", "diff", "--quiet", "HEAD", "--")
        standardOutput = ByteArrayOutputStream()
        errorOutput = ByteArrayOutputStream()
        isIgnoreExitValue = true
    }
    return when (result.exitValue) {
        0 -> "false"
        1 -> "true"
        else -> "unknown"
    }
}

val updaterBuildVersion = providers.gradleProperty("updaterVersion").orElse(project.version.toString()).get()
val updaterGitCommit = gitOutput("rev-parse", "--short=12", "HEAD")
val updaterGitDirty = gitDirty()
val updaterBuildTime = Instant.now().toString()
val generatedBuildInfoDir = layout.buildDirectory.dir("generated/resources/build-info")

val generateBuildInfo by tasks.registering {
    outputs.dir(generatedBuildInfoDir)
    doLast {
        val file = generatedBuildInfoDir.get().file("META-INF/jm-updater-core.properties").asFile
        file.parentFile.mkdirs()
        file.writeText(
            "version=$updaterBuildVersion\n" +
                "gitCommit=$updaterGitCommit\n" +
                "dirty=$updaterGitDirty\n" +
                "buildTime=$updaterBuildTime\n",
            Charsets.UTF_8,
        )
    }
}

sourceSets.named("main") {
    resources.srcDir(generatedBuildInfoDir)
}

tasks.processResources {
    dependsOn(generateBuildInfo)
}

tasks.test {
    useJUnitPlatform()
    // FR-099：测试一律 headless——既防 CI/本地误弹 Swing 窗口，也让进度工厂走文本降级路径可验。
    systemProperty("java.awt.headless", "true")
    // FR-091 自更新 selftest 需以独立 classloader 加载真实构建出的 core jar 自证可用，
    // 故把自身 jar 制品路径注入测试（CoreSelfTestRealJarTest）。test 依赖 jar 不成环（jar 不依赖 test）。
    // FR-256 起 SelfUpdater 已删，但 CoreSelfTestRealJarTest 仍验证真 jar 可加载、Core.selfTest()（zstd）通过。
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
    dependsOn(generateBuildInfo)
    duplicatesStrategy = DuplicatesStrategy.EXCLUDE
    from({
        configurations.runtimeClasspath.get()
            .filter { it.name.endsWith("jar") }
            .map { zipTree(it) }
    })
    manifest {
        attributes(
            "Implementation-Version" to updaterBuildVersion,
            "JM-Updater-Core-Version" to updaterBuildVersion,
            "JM-Git-Commit" to updaterGitCommit,
            "JM-Git-Dirty" to updaterGitDirty,
            "JM-Build-Time" to updaterBuildTime,
        )
    }
    // 排除被打包依赖自身的签名/模块描述，避免 SecurityException / 多 module-info 冲突。
    exclude("META-INF/*.SF", "META-INF/*.DSA", "META-INF/*.RSA", "META-INF/versions/**/module-info.class")
}
