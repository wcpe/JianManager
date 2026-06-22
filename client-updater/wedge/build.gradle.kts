// 楔子：javaagent jar。target Java 8 以广兼容各游戏 JVM（楔子先于 mod loader 加载，
// 唯一允许的失败模式是 fail-open 放行——见 ADR-021 决策 6）。
java {
    sourceCompatibility = JavaVersion.VERSION_1_8
    targetCompatibility = JavaVersion.VERSION_1_8
}

tasks.jar {
    manifest {
        attributes(
            "Premain-Class" to "top.jm.updater.wedge.Wedge",
            "Can-Redefine-Classes" to "false",
            "Can-Retransform-Classes" to "false"
        )
    }
}
