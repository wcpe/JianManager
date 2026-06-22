// updater-core：更新主体，被楔子动态加载（URLClassLoader 内存加载，便于自更新换 jar）。
// target Java 17：用 java.net.http；现代整合包自带 JRE 17+。
// 仅 JDK 自带能力 + 轻量 JSON（契约约束，jar 精简）；zstd 解压库待 FR-090 选型。
java {
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}
