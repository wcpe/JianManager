# ADR-014: 用 ServerProbe 作监控探针、退役自写插件桥

- **日期**: 2026-06-21
- **状态**: accepted（**部分被 [ADR-016](016-serverprobe-governance-bridge.md) 取代**，2026-06-22：监控部署链路保留有效；本 ADR「探针只读 + 玩家治理走 RCON」的决策被 ADR-016 推翻——探针改经反向 WS 承载治理/实时事件/全状态查询）
- **取代**: [ADR-012](012-plugin-bridge-channel.md)
- **上下文**: ADR-012 当初为同时满足「实时玩家事件 + 精确治理 + 富监控指标」三件事，决定在 Worker 旁起 WS 插件桥并自写 Bukkit/BungeeCord 双端插件（`tools/jianmanager-bridge/`）。但实际推进时三件事的耦合并不强：
  - **玩家治理**（踢/封/whitelist）在 FR-054 修复 RCON 鉴权包类型 bug（commit d1314b5）后，纯 RCON 路径已能真机踢出在线 Bot；当前需求不再依赖插件路径执行治理。
  - **实时玩家事件**（加入/退出/聊天）当前 V1 并无强需求（FR-055 处于候选/未排）。
  - **富监控指标**（TPS/MSPT/堆/线程/CPU/世界负载，FR-010）才是当前主要痛点：自写插件只采了基础几项，开源生态已有成熟的只读监控探针 [ServerProbe](https://github.com/wcpe/ServerProbe)（TabooLib，单 jar 多端 Bukkit+BungeeCord），原生 Prometheus `/metrics` 端点 + 90%+ 指标用现成 API/JMX，零侵入、无运行时反射注入。
  
  自写 jianmanager-bridge 既要维护双平台 Java 代码（编译需 Maven+JDK17），又只覆盖了三件事中目前仅次要的两件，沉没成本不可挽回，继续维护反而拖累项目。
- **决策**:
  1. **以 ServerProbe 作监控探针**：作 git 子模块引入 `third_party/ServerProbe`，按构建配方（`./gradlew :plugin:jar :plugin:taboolibMainTask`，JDK21）出 `ServerProbe-*.jar`（单 jar 多端，含 plugin.yml + bungee.yml）。
  2. **CP 内嵌探针 jar**：`internal/controlplane/embed/probe/ServerProbe.jar` 经 `go:embed` 注入 CP 二进制（mirror ADR-005 前端嵌入），`make embed-probe` 目标可选构建并复制；不跑也能编译（缺 jar 时优雅跳过部署）。
  3. **建服自动部署**：provision 时为实例系统分配一个 probe 端口（默认 29940 段，同节点唯一），经新 gRPC `DeployServerProbe(jar, config_yaml)` 把 jar 与最小 `plugins/ServerProbe/config.yml`（仅开启 `/metrics` 于本机回环 + 分配端口）写入实例 plugins 目录。
  4. **Worker 抓取**：`internal/worker/metrics/serverprobe.go` 提供 `ScrapeServerProbe(host,port,token)` 抓 Prometheus exposition 文本并解析为 `ProbeSnapshot`（TPS/MSPT/players_online/heap/threads/system_cpu/uptime/按世界 chunks·entities·tile）。`GetInstanceMetrics` RPC 优先经探针路径取指标，探针未部署/抓取失败时回退 RCON（TPS/在线）+ 进程 RSS，都不可用返回 N/A。
  5. **退役自写插件桥**：删除 `tools/jianmanager-bridge/`、Worker `/ws/plugin-bridge`、gRPC `StreamPluginEvents`/`SendPluginCommand`、CP `plugin_bridge` service/router/SSE、前端 PluginBridgePage 与侧栏入口、两端 i18n。**ADR-012 标记为 superseded-by ADR-014**。
- **理由**:
  - ServerProbe 是只读监控探针、与 Worker 进程边界天然解耦（HTTP 抓取，非反向 WS 连入），不破坏既有架构不变量（Worker 主动抓本机回环，零额外网络面）。
  - 不再额外维护 Java 双平台插件，监控能力反而大幅扩张（MSPT/线程/世界负载之前都没有）。
  - 玩家治理已由 RCON 路径覆盖（FR-054 已通真机），FR-055 真有需要时可再独立设计（不必再背 ADR-012 的链路成本）。
  - 探针 jar 内嵌走 `go:embed` 与前端一致，构建配方固化在 `make embed-probe`；不跑此目标也能完整 build（缺 jar 时部署优雅跳过、运维仍可手动放入）。
- **后果**:
  - 通信不变量清单收回 ADR-012 新增的「插件 ↔ Worker WS（token 鉴权）」一行；新增「Worker 主动抓 ServerProbe `/metrics`（本机回环）」一行——HTTP 客户端、无对外暴露。
  - proto 加性移除 `StreamPluginEvents`/`SendPluginCommand`/`PluginEvent`，加性新增 `DeployServerProbe(jar, config_yaml)` + `GetInstanceMetricsRequest` 扩展 `probe_port`/`rcon_port`/`rcon_password` + `GetInstanceMetricsResponse` 扩展 mspt/threads/cpu/heap_max/uptime/worlds/probe_available。
  - FR-055（玩家管理插件桥增强）改为「依赖 RCON 路径，未来真有需要再独立设计」，**deprecated**；FR-103（平台插件桥通道，作为 ADR-012 的载体）→ **deprecated**（其能力被 ServerProbe 取代）。
  - 端口分配增加 `ProbePort`（29940 段），instance 模型新增 `probe_port` 字段，节点端口占用展示同步加列。
  - 第三方依赖：ServerProbe 走子模块；构建需 JDK21 + Gradle 8.9（首次会联网拉 TabooLib 运行时依赖）。
