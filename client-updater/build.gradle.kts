// JM 客户端 OTA 更新器（monorepo 子目录，类比 bot-worker/）。
// 接口契约见 ../docs/specs/client-distribution/contract.md；决策见 ADR-021/022/023。
// 独立 Gradle 构建产 wedge / updater-core 两 jar，不进 JM Go 主构建。

allprojects {
    group = "top.wcpe.mc.jm.updater"
    version = "0.1.0-SNAPSHOT"
    repositories {
        mavenCentral()
    }
}

subprojects {
    apply(plugin = "java")
}
