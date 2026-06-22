# jm-client-updater

JM 客户端 OTA 更新器（JM 仓内 monorepo 子目录，类比 `bot-worker/`；**非独立仓**，见 ADR-021 修订）。两 jar：

- **wedge**：javaagent 楔子。启动器 `-javaagent:wedge.jar=<gameDir>` 注入；premain 自定位 + 动态加载 updater-core + 同步等待 + **fail-open**（target Java 8，广兼容游戏 JVM）。
- **updater-core**：更新主体。拉签名 manifest（latest）→ 验签 + 防降级 → 文件级增量/减量 → CAS 缓存 → 自更新 + N-1 回退（target Java 17）。

接口契约见 [../docs/specs/client-distribution/contract.md](../docs/specs/client-distribution/contract.md)；架构决策见 ADR-021（纯 JVM 方案）/ ADR-022（签名信任 + 防降级 + 密钥轮换）/ ADR-023（端点防护）。

## 构建

```bash
cd client-updater
./gradlew :wedge:jar :updater-core:jar     # gradle wrapper 待补（FR-089）
```

独立 Gradle 构建，产物 jar；**不进 JM Go 主构建**。

## 状态

骨架（task #2）。各能力按以下 FR 在 JM 仓内 `sdd-develop-feature` 推进：

| 模块 | FR |
|---|---|
| wedge | FR-089（自定位 + 引导 + fail-open + multi-agent 共存） |
| updater-core | FR-090（reconcile）/ FR-091（自更新 + N-1 回退）/ FR-092（机器码）/ FR-094（遥测）/ FR-097（.jmpack 解包） |
