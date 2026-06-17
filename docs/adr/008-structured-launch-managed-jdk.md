# ADR-008: MC 实例结构化启动与托管 JDK

- **日期**: 2026-06-18
- **状态**: accepted
- **上下文**: MC 群组服里不同 MC 版本子服需要不同 Java（1.8→Java 8，1.18+→17，1.20.5+→21），用户不应手动装 Java 或手填带绝对路径的启动命令。自由文本 startCommand 还引发过 BUG-005（引号/空格路径）。
- **决策**:
  1. **平台按节点托管多 JDK**：节点维护 JDK 注册表，支持安装多个版本（下载源默认 Adoptium/Temurin，可配 Zulu），装入系统分配的 runtimes 目录；也支持登记系统已有 JDK。
  2. **实例绑定 JDK 或 Java 大版本**；启动时由 Worker 注入 `JAVA_HOME` 并将 JDK/bin 接入 `PATH`，再叠加实例自定义 `env_vars`（模型已有）。
  3. **MC 实例改用结构化启动规格**：`绑定JDK + JVM参数(内存/GC) + core.jar + 额外args`，由 Worker 组装 `cd <workDir> && <jdk>/bin/java <args> -jar core.jar nogui`，**取代自由文本 startCommand**；通用 / universal 实例仍可自由命令。
- **理由**:
  - 结构化启动消除引号/路径歧义（根治 BUG-005），并让「换 Java 版本」「调内存」成为表单操作。
  - 多 JDK 是群组服混版本运行的硬需求。
- **后果**:
  - JDK 体积较大，计入节点磁盘；删除被占用 JDK 需拒绝。
  - 启动规格需要 schema；MC 实例的 `start_command` 由系统派生而非用户输入。
  - **改变已 done 的 BUG-005 / FR-005 在 MC 实例上的「自由文本启动命令」表现**。
- **替代方案**:
  - 继续自由文本启动命令 — 易出错、无法统一管理 Java，否决（universal 实例除外）。
  - 依赖宿主机已装 Java — 多版本共存与可移植性差。
