# jm-client-updater

JM 客户端 OTA 更新器（JM 仓内 monorepo 子目录，类比 `bot-worker/`；**非独立仓**，见 ADR-021 修订）。两 jar：

- **wedge**：javaagent 楔子。启动器 `-javaagent:wedge.jar=<gameDir>` 注入；premain 自定位 + 动态加载 updater-core + 同步等待 + **fail-open**（target Java 8，广兼容游戏 JVM）。
- **updater-core**：更新主体。拉签名 manifest（latest）→ 验签 + 防降级 → 文件级增量/减量 → CAS 缓存 → 自更新 + N-1 回退（**target Java 8**，兼容低版本游戏 JVM；HTTP 用 `HttpURLConnection`、Ed25519 用 BouncyCastle、zstd 用 zstd-jni）。

接口契约见 [../docs/specs/client-distribution/contract.md](../docs/specs/client-distribution/contract.md)；架构决策见 ADR-021（纯 JVM 方案）/ ADR-022（签名信任 + 防降级 + 密钥轮换）/ ADR-023（端点防护）。

## 构建

```bash
cd client-updater
./gradlew :wedge:jar :updater-core:jar     # 产 wedge / updater-core 两 jar
./gradlew test                             # 跑 JUnit 单测
```

独立 Gradle 构建（wrapper 8.10，FR-089 已补），产物 jar；**不进 JM Go 主构建**。
`updater-core` 为 fat jar（自包含 zstd-jni + BouncyCastle，因被楔子 URLClassLoader 独立加载，契约 §6.3）；
`wedge` 极小、`Premain-Class=top.wcpe.mc.jm.updater.wedge.Wedge`。打包入基础整包时按 `jm-updater.json` 的
`coreJar` 字段命名 core jar（默认 `updater-core.jar`），楔子 jar 名由 `-javaagent:` 路径决定。

## 状态

各能力按以下 FR 在 JM 仓内 `sdd-develop-feature` 推进：

| 模块 | FR | 状态 |
|---|---|---|
| wedge | FR-089（自定位 + 引导 + fail-open + multi-agent 共存） | 🔨 核心实现 + 单测就绪；真机（HMCL/PCL2、authlib 共存）待验 |
| updater-core | FR-090（reconcile：验签 + 防降级 + 增量减量 + 隔离 + CAS + 锁） | 🔨 核心实现 + 单测就绪；端到端待 FR-087 端点 + 真机 |
| updater-core | FR-091（自更新 + N-1 回退）/ FR-092（机器码）/ FR-094（遥测）/ FR-097（.jmpack 解包） | 📋 后续 FR |
