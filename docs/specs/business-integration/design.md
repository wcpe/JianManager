# JianManager 业务对接平台设计文档

> **全能探针进化 + 业务对接标准（JM Business Integration Standard, JBIS）**
>
> 本文是该方向的**总体设计总纲**。它定义"ServerProbe 从监控探针进化为 JM 全能业务 agent"的完整架构，以及"任何业务插件按 JM 标准实现接入即可被掌控"的开放标准与 SDK。
>
> 本文**不是** feature 级 api.md / impl.md，而是其上位的方向性设计；正式 FR 拆解、PRD 登记、ADR 落地由后续 `/sdd-brainstorming` 完成。文中所有 FR / ADR 编号均为**建议占位**，标注"待登记"。

| 项 | 值 |
|---|---|
| 文档状态 | `draft`（草案，待 brainstorm 拆 FR） |
| 创建日期 | 2026-06-24 |
| 影响仓库 | JianManager（本仓）、ServerProbe（子模块）、MultiCurrencyEconomy、AllinInventorySync |
| 关联既有 ADR | ADR-012（插件桥通道）、ADR-014（探针只读+RCON，已被 016 取代）、ADR-016（ServerProbe 治理桥） |
| 建议新增 ADR | ADR-024 ~ ADR-029（见 §6，待登记） |
| 关联既有 FR | FR-065/066/067（插件桥地基/玩家事件/治理执行）、FR-076/077（全量状态查询） |
| 前置阅读 | `docs/ARCHITECTURE.md`、`docs/adr/016-serverprobe-governance-bridge.md`、`docs/specs/plugin-bridge/api.md` |

---

## 1. 背景与动机

### 1.1 现状

JianManager（下称 JM）是一个多节点游戏服务端管理平台，三进程模型：**Control Plane（CP）→ gRPC → Worker Node → exec/IPC → Bot Worker**。当前已经通过 **ServerProbe 探针**实现了对游戏服务端的**监控数据采集**与**轻量治理**：

- ServerProbe 作为 Bukkit/Bungee 插件运行在游戏服务端内，**反向 WebSocket** 连入本机 Worker 的 `/ws/plugin-bridge`（实例级 token，scope=plugin-bridge，见 ADR-016）。
- 桥协议为通用 JSON 帧：上行 `hello`/`ping`/`event`，下行 `welcome`/`pong`/`command`；命令以 `requestId` 关联同步回执 `command_result`（`internal/worker/ws/bridge.go`）。
- 现有治理能力：`kick`/`ban`/`unban`/`whitelist_add`/`whitelist_remove`/`list`/`whitelist_list`/`query_state`，探针侧由 `BukkitBridgeCommandHandler` 切回主线程同步执行。

也就是说：**双向通道、请求/响应往返、会话管理、鉴权、降级**——这些"接入面"的基础设施**已经存在且经过真机验证**。

### 1.2 诉求

运营者希望 JM 成为**顶层总管**：

> JM 下挂多个**节点**，每个节点跑多个**服务端**，每个服务端装有多个**业务插件**（经济、背包、领地、任务、称号……）。JM 要能穿透到每一个服务端的每一个业务插件，**读取其数据、操控其行为、汇聚其变更**，并提供**跨节点的统一管理入口**。

为实现这一点，ServerProbe 不应只是"监控探针"，而应进化为 **JM 在每个服务端内的唯一全能 agent**：既采监控，又对接业务插件。一个 agent、一条通道、一个身份。

### 1.3 从"集成"到"平台"的关键跃迁

实现业务对接有两种范式：

| | 范式 A：JM 适配器 | 范式 B：JM 标准 + 插件实现（**本设计采用**） |
|---|---|---|
| 谁理解谁 | JM 写适配器去理解每个插件的 API | 插件按 JM 标准实现接入部分 |
| 新增插件 | JM 团队写新适配器 | 插件方自行实现，即插即用 |
| JM 编译依赖 | 依赖各插件 api jar | 只依赖自己的 SDK 契约 |
| 生态 | 封闭（JM 决定支持谁） | 开放（任何插件按标准接入） |
| 风险 | JM 完全可控 | 依赖插件方正确实现标准 |

**本设计以范式 B 为主路线**：JM 定义一套**业务对接标准（JBIS）** + 探针暴露一个 **SDK**，业务插件实现标准契约、向探针注册，即可被发现、被掌控。范式 A（适配器）退化为"给改不动的闭源插件用的兼容垫片"，与 B 共存于同一套契约下（见 §7.10）。

这使 JM 从"能接几个特定插件的管理器"升级为"**有开放接入标准的掌控平台**"。

---

## 2. 设计目标与非目标

### 2.1 目标

1. **统一全能 agent**：ServerProbe 对外是一个插件、一条桥连接、一个实例身份；对内监控与业务分层。
2. **开放业务对接标准（JBIS）**：定义域（domain）、能力（capability）、命令（command）、事件（event）、结果（result）、幂等、审计、版本协商的统一契约。
3. **探针侧 SDK**：业务插件 `compileOnly` 依赖即可实现 `JmBusinessProvider` 并注册，无需理解 WS/gRPC/JM 内部。
4. **首发两域**：经济（MultiCurrencyEconomy）与背包（AllinInventorySync）按标准原生接入，验证标准完备性。
5. **跨节点掌控**：CP 提供统一 API，向任意节点/服务端/玩家下发业务命令并取回结果。
6. **数据汇聚**：业务变更事件汇聚回 CP 持久化，可查询、可审计、可去重。
7. **事故隔离**：业务对接层的任何异常/卡顿/插件缺失，绝不影响监控采集，绝不拖垮探针（守住 ServerProbe "绝不成为事故源"）。

### 2.2 非目标（明确不做）

1. **不替代业务插件做存储/同步**。经济（MySQL `mce_*` + zoneId 分区 + outbox relay）、背包（MySQL/JSON + data-group + CAS + Redis 租约）各自已解决跨服一致性。JM **绝不**自建经济/背包存储或同步逻辑。
2. **不做业务逻辑本身**。JM 不实现"怎么算余额""怎么合并背包"，只透传命令到插件、由插件用自己的权威入口执行。
3. **不绕过插件的安全模型**。背包"净改动合并、绝不刷物品"由插件保证，JM 只调其权威入口。
4. **首版不追求覆盖所有业务域**。先把标准骨架 + 经济 + 背包立起来，其余域（领地/任务/称号）作为标准的后续验证。
5. **不在本设计内落地代码与 FR/PRD**。本文是设计；实现走 SDD 流程。

---

## 3. 术语

| 术语 | 含义 |
|---|---|
| **全能 agent** | 进化后的 ServerProbe：监控层 + 业务对接层合一的单一插件 |
| **JBIS** | JM Business Integration Standard，业务对接标准（本文 §7） |
| **SDK** | `jianmanager-business-sdk`，JBIS 的契约 jar + 注册/发现封装（§8） |
| **Provider** | 业务插件实现的 `JmBusinessProvider`，代表一个业务域的接入实现 |
| **domain** | 业务域标识，如 `economy` / `inventory` |
| **verb** | 域内的动作，如 `deposit` / `edit`；命令以 `domain.verb` 寻址 |
| **capability** | Provider 声明支持的 verb 集合，供探针/CP 做能力发现 |
| **BusinessHost** | 探针侧的业务宿主服务：发现 Provider、路由命令、桥接事件 |
| **dedupKey** | 事件去重键（经济用 `ledgerId`，背包用 `dataVersion`） |
| **汇聚** | 业务变更事件经桥上行、由 CP 落库的过程 |

---

## 4. 总体架构

### 4.1 全能 agent 的内部分层

**对外统一，对内分层**——这是整套设计的地基（已与运营者对齐确认）：

```
                   ServerProbe（一个插件 = JM 在本服务端的唯一 agent）
                   ┌─────────────────────────────────────────────┐
                   │  监控层（既有，纯只读，保持纯净）            │
                   │    JMX / TPS / 世界 / 启动剖析 …             │
                   ├─────────────────────────────────────────────┤
   反向 WS 桥 ──────┤  桥层（既有，通用 JSON 帧 + requestId 往返） │
   （单连接、       │    hello/ping/event ↑  welcome/pong/command ↓│
    单身份）        ├─────────────────────────────────────────────┤
                   │  业务对接层（新增，独立事故域）              │
                   │    BusinessHost → 发现/路由/桥接              │
                   │      ├ JmBusinessProvider(economy)  ← mce 实现 │
                   │      └ JmBusinessProvider(inventory)← 背包实现 │
                   └─────────────────────────────────────────────┘
```

**事故域隔离铁律**：业务对接层与监控层**不共享线程、不共享异常边界**。业务 Provider 卡死/抛异常/插件缺失，只影响该域命令的回执（降级为失败），监控采集与桥心跳完全不受影响。

### 4.2 在三进程模型中的位置

业务对接**完全复用现有桥**，不新增进程、不新增通信协议，不违反架构不变量：

```
浏览器 ──HTTP/WS──▶ Control Plane ──gRPC──▶ Worker Node ──反向WS桥──▶ ServerProbe ──ServicesManager──▶ 业务插件
   │                    │                      │                       │                          │
 业务掌控台          下发业务命令          桥透传(command/event)     BusinessHost 路由          mce / 背包 Provider
 数据可视化          汇聚事件落库          会话/鉴权/降级            事故隔离/降级              插件权威入口执行
```

- **依赖方向不变**：CP → Worker → 探针 → 插件，反向仅事件冒泡（既有桥语义）。
- **进程边界不变**：CP 不直接碰插件，必经 Worker → 桥；探针不碰 DB/gRPC，只经桥上报。
- **数据所有权不变**：业务数据真源仍在各插件的存储；JM 侧存的是**汇聚镜像 + 操作审计**（§11）。

### 4.3 控制流与数据流（两个方向）

- **控制（下行，JM 掌控）**：CP 生成命令（`domain.verb` + `requestId` + args + 操作者身份）→ gRPC → Worker → 桥 `command` 帧 → BusinessHost 路由到对应 Provider → 插件权威入口执行 → `command_result` 原路返回。
- **汇聚（上行，数据回流）**：插件变更事件 → Provider 订阅 → BusinessHost 转标准 `JmEvent`（带 dedupKey）→ 桥 `event` 帧 → Worker → gRPC 流 → CP → 去重 → 落库。

---

## 5. 关键设计原则

1. **复用优先**：桥通道、`requestId` 往返、鉴权、会话管理已存在且验证过——业务层在其上扩展，Worker 侧改动最小化。
2. **依赖反转**：JM 定标准，插件实现标准；JM 不编译依赖业务插件。
3. **同构抽象**：经济与背包 API 已被证明高度同构（读/写幂等/事件/错误码/离线），标准从这个同构性提炼。
4. **幂等一链到底**：`requestId` 从 CP 生成、经桥透传、直达插件幂等键，跨节点重试天然防重。
5. **审计可追溯**：每条业务命令携带"哪个 JM 管理员、哪个节点、为什么"，映射进插件审计流水。
6. **降级即默认**：插件不在场/版本不符/超时/异常，一律优雅降级为该域不可用，绝不 5xx、绝不拖垮探针。
7. **标准向后兼容**：能力（capability）+ 版本（sdkVersion）协商，标准演进不破存量接入。

---

## 6. 关键设计决策与建议 ADR 清单

> 以下为本设计引出的架构决策。**编号为建议占位（接 ADR-023 之后），待 `/sdd-brainstorming` 后正式创建**。ServerProbe 子模块侧另有其自身 ADR（见 §9.6）。

| 建议 ADR | 决策 | 摘要 |
|---|---|---|
| **ADR-024** | ServerProbe 从监控探针进化为 JM 全能业务 agent | 确立"对外单 agent、对内监控/业务分层、事故域隔离"为既定架构 |
| **ADR-025** | 采用"标准+SDK"依赖反转而非 JM 适配器 | 插件实现 JBIS 接入；适配器降为兼容垫片 |
| **ADR-026** | Provider 经 Bukkit ServicesManager 服务发现 | 而非自建静态注册中心；定 SDK 单一共享 jar 的类加载铁律 |
| **ADR-027** | 业务命令复用桥 `command/event` 帧 + `domain.verb` 寻址 | Worker 侧零语义改动，仅按前缀路由；proto 增 `domain`/`dedupKey` 可选字段 |
| **ADR-028** | CP 业务数据与时序监控数据分表分策略 | 业务数据持久可审计、按 dedupKey 去重；监控可降采样可丢 |
| **ADR-029** | 业务高危操作（改余额/改背包）的权限与二次确认模型 | 操作者身份透传 + 审计 + 阈值确认 |

> 注：ADR-014（探针只读+RCON）已被 ADR-016 取代；本方向进一步扩展 ADR-016 的桥语义，不与之冲突——ADR-016 的"治理执行"与本设计的"业务对接"同属桥的下行命令，分属不同 `domain` 命名空间（治理为内建 `core.*`，业务为 `economy.*`/`inventory.*`）。

<!-- APPEND-POINT -->

---

## 7. brainstorming 拆解结果（2026-06-24，已落地）

经 `/sdd-brainstorming` 拆解并经用户确认登记：

- **13 FR（FR-115~127）/ 3 里程碑**，登记于 `docs/PRD.md`「JBIS 业务对接平台（全能业务 agent，3 里程碑程序）」段。M1 垂直切片（economy.balance 读穿全 5 层）→ M2 经济整域 → M3 背包整域。计划全文 `.tmp/brainstorm-jbis-business-integration-2026-06-24.md`。
- **范围**：经济 + 背包两域读+写（范围 B），含扩 AllinInventorySync 自身 api 模块导出写门面（跨仓 FR-124）。
- **agent 形态敲定**：一个 ServerProbe 分模块（业务对接层落 platform 层、独立事故域），**非**独立 JianAgent 插件（同 JVM 下两插件不提供更强隔离，徒增双连接/双身份）。
- **范式敲定**：范式 A 适配器 + manifest 能力发现（探针侧 per-plugin Provider wrap native API），**非**范式 B 插件实现 SPI——修正本文 §6 倾向，记于 ADR-026。
- **ADR 改号**：本文 §6 建议占位 ADR-024~029，因 **ADR-024 已被 FR-113（全文索引后台化）实占**，正式编号顺延为 **ADR-025~029**：
  - ADR-025 ServerProbe 监控探针→全能业务 agent（= 本文建议 024）
  - ADR-026 适配器+manifest 能力发现路线（取代本文建议 025/026 的范式 B/SDK 倾向）
  - ADR-027 业务命令复用桥 command/event + domain.verb 寻址（= 建议 027）
  - ADR-028 CP 业务数据与时序监控分表分策略（= 建议 028）
  - ADR-029 业务高危写权限与二次确认模型（= 建议 029）
- 两插件对接契约事实基础见 `.tmp/business-integration-plugin-contracts.md`。
