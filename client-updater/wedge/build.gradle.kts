// 楔子：javaagent jar。target Java 8 以广兼容各游戏 JVM（楔子先于 mod loader 加载，
// 唯一允许的失败模式是 fail-open 放行——见 ADR-021 决策 6）。
java {
    sourceCompatibility = JavaVersion.VERSION_1_8
    targetCompatibility = JavaVersion.VERSION_1_8
}

repositories {
    mavenCentral()
}

dependencies {
    // 注意：updater-core 不作为编译/运行时依赖加入 classpath——楔子刻意经 URLClassLoader
    // 动态加载其 jar（契约 §6.3，便于 FR-091 自更新替换）。CoreLoaderTest 只需 jar 文件路径，
    // 经下方 test.dependsOn(:updater-core:jar) + systemProperty 注入（而非 project 依赖，
    // 否则 Java 8 楔子无法把 Java 17 的 updater-core 放上 runtime classpath）。
    testImplementation("org.junit.jupiter:junit-jupiter:5.10.2")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

tasks.jar {
    manifest {
        attributes(
            "Premain-Class" to "top.wcpe.mc.jm.updater.wedge.Wedge",
            "Can-Redefine-Classes" to "false",
            "Can-Retransform-Classes" to "false"
        )
    }
}

// CoreLoader 测试需要 updater-core 的 jar 制品就绪。
tasks.test {
    useJUnitPlatform()
    dependsOn(":updater-core:jar")
    // 把 updater-core jar 路径作为系统属性传入测试，供 CoreLoaderTest 加载真实 jar。
    val coreJar = project(":updater-core").tasks.named("jar")
    inputs.files(coreJar)
    doFirst {
        systemProperty("jm.updater.core.jar",
            coreJar.get().outputs.files.singleFile.absolutePath)
    }
    testLogging {
        events("passed", "skipped", "failed")
    }
}
