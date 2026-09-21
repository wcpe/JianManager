# MC 直探（SLP + Query）与采集优先级降级编排（FR-446 / FR-447）

> 状态：📋 计划　·　关联 PRD：FR-446、FR-447　·　依赖：FR-451（注入 `enable-query`/`query.port`，否则 Query 档形同虚设）　·　关联 ADR：ADR-092（配置源明面化与 MC 直探）、ADR-003/081（CP 不直连游戏端口的进程边界）、ADR-013（心跳 30s 上报落库）、ADR-091（能力画像的数据来源偏好）

## 1. 背景与目标

**现状痛点**：实例的 MOTD / 版本 / 在线人数 / 玩家名单**全部**依赖 ServerProbe（第三方 Bukkit 插件 + Prometheus `/metrics`）。全仓无 SLP / Query 协议客户端——`internal/worker/metrics/` 只有 `collector.go`（节点指标）与 `serverprobe.go`（`ScrapeServerProbe`，抓 `/metrics`）。探针未装 / 未连 / 挂在非 Bukkit 服务（代理、二进制、beacon）时这些基础信息**一个都拿不到**：`internal/worker/grpc/server.go:524-525` 在探针不可用时把 `Tps=-1`、`OnlinePlayers=-1` 当占位符下发，前端 64 张卡片玩家数全是 `-1`（真机确认）。且**全仓已无 RCON 兜底**（FR-067 退役，见 ADR-016），探针是唯一来源。

**目标**
- 新增 **SLP（Server List Ping）** 客户端：零配置，任何在跑的 MC 服（含 BungeeCord/Velocity 代理）内建支持；取 MOTD / 版本 / 在线人数 / 最大人数 / favicon。
- 新增 **Query（GameSpy4）** 客户端：需 `enable-query=true` + `query.port`；取**准确实名玩家名单** / 插件列表 / 地图名。
- 新增**采集优先级链** `探针 → SLP → Query → 不可用`，同指标多源以探针为准；三者皆无则显式「不可用」。
- 清除 `-1` / `--` 垃圾占位：不可用是**明确的缺测语义**，不是伪造的数值。

**范围内**：SLP / Query 客户端（Worker 侧）、编排降级链、proto 字段扩展、`GetInstanceMetrics` 与心跳两条采集链路的「不可用」语义统一。

**不做（范围外）**：不动探针本身（`third_party/ServerProbe`）；不做 Java 版以外的协议（基岩版 RakNet、Steam A2S 等）；不改采集节奏（仍 30s 心跳，见 ADR-013）。

## 2. 设计

### 2.1 落点与数据流

直探必须落在 **Worker 侧**（与探针抓取同层）：CP 不直连游戏端口（ADR-003/081），且多节点下 CP 未必能连通游戏服内网。新增包内文件 `internal/worker/metrics/slp.go`、`query.go`、`orchestrate.go`，与既有 `serverprobe.go` 并列，均为**纯函数可单测**（协议编解码与网络 IO 分离，参照 `parseServerProbeMetrics` 的可测风格）。

数据流两条，均复用既有通道、**不新增 gRPC**：
1. **时序链路**（FR-060）：`heartbeat.collectInstanceMetrics`（`internal/worker/heartbeat/heartbeat.go:269`）对 RUNNING 实例抓探针 → 扩为编排链 → 结果塞进 `workerpb.InstanceMetricSample` → 心跳上报 → CP `MetricService.ingestHeartbeatAt`（`internal/controlplane/service/metric.go:232`）落库。
2. **实时链路**（详情页即时值）：`GetInstanceMetrics` RPC（`internal/worker/grpc/server.go:494-526`）→ CP `InstanceService.GetMetrics`（`internal/controlplane/service/instance.go:1806`）→ `MetricsData`。

两条链路必须走**同一个编排函数**，否则「探针挂→直探补」只在一处生效、另一处仍吐 `-1`。

### 2.2 SLP 客户端（协议要点）

面向 `server-port` 的 **TCP** 连接。包格式 `[length:VarInt][packetId:VarInt][payload]`：

1. **Handshake**（packetId `0x00`）：`protocolVersion:VarInt`（探测用 `-1`，编码为 5 字节 `FF FF FF FF 0F`，表示"我只要状态"）、`serverAddr:String`（VarInt 长度前缀 + UTF-8，可用 `localhost`）、`serverPort:uint16`（**大端**）、`nextState:VarInt=1`（status）。
2. **Status Request**（packetId `0x00`，空 payload）。
3. **Status Response**（packetId `0x00`）：`String`（VarInt 长度前缀）= JSON：

```json
{ "version": { "name": "1.20.4", "protocol": 765 },
  "players": { "max": 20, "online": 5,
               "sample": [ { "name": "Steve", "id": "uuid" } ] },
  "description": { "text": "A Minecraft Server" },
  "favicon": "data:image/png;base64,..." }
```

- `description` 可能是字符串或 chat 组件对象（`{"text":...}` / `extra` 数组），需扁平化为纯文本。
- `players.sample` **不保证返回**、也**不含可信实名**（可被插件伪造/截断），仅作"可能在线名单"弱信息；**实名名单只认 Query**（ADR-092）。
- 代理服（BungeeCord/Velocity）与后端服均支持 SLP，是"保底可见"的关键。
- **Legacy 回退**（可选）：`0xFE 0x01` → `0xFF` + UTF-16BE `motd§online§max`，用于 pre-1.7 老服；首版可不实现，遇老服走"不可用"。

### 2.3 Query 客户端（协议要点）

面向 `query.port` 的 **UDP**，GameSpy4/UT3 协议：

1. **Handshake**：发 `FE FD 09` → 响应 `09` + **4 字节 session token**（大端 int32 challenge）。
2. **Full Stat**：发 `FE FD 00` + `token`(4 字节) → 响应 `00` + `token` + 键值对段。响应可能**跨多个 UDP 包**分片，需按 `token` 与包内偏移拼接（首包含总长度）。
3. 键值对段格式 `key\0value\0...`，末端 `\0\x01player_\0\0` 之后是**玩家名列表**（每个以 `\0` 结尾，末尾再补 `\0`）。
   - 关键键：`hostname`（MOTD）、`version`、`plugins`（如 `Paper on 1.20.4`）、`map`（世界名）、`numplayers`、`maxplayers`、`hostport`、`hostip`。
   - `plugins` 是**逗号分隔字符串**，需切分；MOTD 可能含 `§` 颜色码，需剥离。
4. **前置条件**：`enable-query=true` 且 `query.port` 可达；否则 **UDP 无响应/超时**→ 该档记"不可用"（不是错误）。Basic Stat（无玩家列表）首版可不实现。

> **端口来源**：Worker 需知道 `server-port`（SLP）与 `query.port`（Query）。二者现由 CP 持有（`model.Instance.ServerPort/QueryPort`，`internal/controlplane/model/instance.go:122-123`；`allocPortsForNode` 分配，`internal/controlplane/service/ports.go:82`），但**未下发 Worker**——`CreateInstanceRequest` 只有 `probe_port`（`proto/worker.proto:482`）。需在 proto 补 `server_port` / `query_port`，并加进 `process.Instance` + `InstanceSnapshot`（`internal/worker/process/manager.go:187`）。

### 2.4 采集优先级链与「不可用」语义

统一编排函数（`orchestrate.go`）产出**每指标带来源标记**的结构：

| 指标 | 探针（权威） | SLP | Query | 全无 |
|---|---|---|---|---|
| MOTD / 版本 / favicon | — | ✅ | hostname/version | 不可用 |
| 在线人数 / 最大人数 | ✅ `players_online` | ✅ `online`/`max` | ✅ `numplayers`/`maxplayers` | 不可用 |
| 实名玩家名单 | —（探针无实名） | ⚠️ sample 弱信息 | ✅✅ | 不可用 |
| 插件列表 / 地图名 | — | — | ✅ | 不可用 |
| TPS / MSPT / JVM / 分世界 | ✅✅ | — | — | 不可用 |

规则：**同指标多源以探针为准**（探针最细：TPS/MSPT/JVM/世界）；探针缺该指标时按 SLP → Query 回落；三档皆无 → 该指标标 `available=false`，前端渲染「不可用」。玩家名单优先级例外：**Query 优先**（唯一实名来源），Query 无则展示 SLP sample 并标注"可能不完整"。

### 2.5 协议与模型改动

- `proto/worker.proto`：
  - `CreateInstanceRequest` 加 `int32 server_port = 19;`、`int32 query_port = 20;`（SLP/Query 探测端口）。
  - `InstanceMetricSample`（第 397 行）扩字段：`string motd`、`string version`、`int32 max_players`、`repeated string player_names`、`repeated string plugins`、`string map`，以及 `bytes source_mask` / `bool slp_available` / `bool query_available`（记录本拍各来源可用性，供前端标注数据来源）。
  - `GetInstanceMetricsResponse`：把 `-1` 占位改为显式可用性位（`players_available`），移除 `Tps=-1`/`OnlinePlayers=-1` 约定（`internal/worker/grpc/server.go:524-525`）。
- `internal/worker/process/manager.go`：`Instance`/`InstanceSnapshot` 加 `ServerPort`/`QueryPort`；`Create(...)` 签名加两参（同步更新所有调用点与 `SetServerPort/SetQueryPort` 热更新，参照 `SetProbePort`）。
- CP 注册链 `registerOnWorkerLocked` / `ResyncInstances`：下发 `server_port`/`query_port`。
- CP `MetricService.ingestHeartbeatAt`：`!ProbeAvailable` 分支不只写 TPS 断点，改为按直探结果补落 `inst_players_online` 等；直探也无则不落点（维持 NULL 断点，曲线断而不造假值）。判定在线人数是否可落点用 `players_online_available`，对不置该位的旧/中间版本 Worker 保留**兼容回退**：`probe_available`（探针必给 `players_online`）或 `slp_available`（SLP `players.online` 是协议必带字段）为真即落真实值；**Query-only 不回退**（Query 可响应而缺 `numplayers`，回退会重新引入伪造 0）。

### 2.6 超时 / 错误处理 / 并发

- **超时可配（上界由心跳护栏反推，不是另一个拍脑袋的常量）**：SLP/Query 各默认 3s（对齐 `ScrapeServerProbe` 的 5s 量级，但直探更轻），经平台设置下发（与 `graceful_stop.timeout` 同风格）。**下发通道**：平台设置 `direct_probe.slp_timeout` / `direct_probe.query_timeout`（Go duration）→ 心跳响应 `HeartbeatResponse.direct_probe_{slp,query}_timeout_ms` → Worker 存进程生效值（`metrics.SetDirectProbeTimeouts`），命中**两条**采集链路（心跳时序 + `GetInstanceMetrics` 实时），改设置后 Worker 不重启即在下一拍（≤30s）生效。超时/连接拒绝/UDP 无响应 → 该来源记"不可用"，**不阻塞**其它来源、**不 panic**。
  - **上界 = 10s，且必须由「单拍采集预算 < 心跳节拍」反推得出**（FR-446 复审 NEW-ISSUE A）：单实例同源串行最坏 = `ProbeScrapeTimeoutCap(5s) + slp + query`，单拍采集总预算 = `余量 + 上述最坏`，且必须 ≤ `节拍(30s) − 节拍余量(4s)`。即 `slp + query ≤ 30 − 4 − 1 − 5 = 20s` → 两来源对称上界 ≤ 10s。放宽容许会让「超时可配」与「心跳护栏」互相矛盾（旧实现：硬编码 15s 预算 vs 可配 30s 超时 → 最坏 65s，该实例时序每拍被预算静默砍成「不可用」而无任何告警）。
  - **单一数值来源**：默认值、上界、探针抓取上限、节拍与预算余量统一定义在 `internal/platform/directprobe`，由下列四处共同引用，不得各写一份字面量——**写路径** `settings.go:validateSettingValue`（拒收 > 上界）、**读/下发路径** `parseDurationOr`（钳制）、**Worker 归一** `metrics.normalizeProbeTimeout`（独立钳制）、**心跳采集预算** `heartbeat.collectInstanceBudget`。跨包一致性由 `internal/platform/directprobe/contract_test.go` 的不变量断言与 CP/Worker 两侧的"上界同源"用例锁定。故 yaml/env 基线或历史落库值也无法突破。
- **心跳链路的一拍采集预算（推导值，非定值）**：`heartbeat.collectInstanceBudget()` = `directprobe.CollectBudgetFor(生效 slp, 生效 query)` = `余量(1s) + 探针上限(5s) + slp + query`，恒 ≥ 单实例同源串行最坏，且上界 26s < 30s 节拍（默认值下为 12s，与旧硬编码 15s 同量级）。预算耗尽仍按既有语义处理：用**已完成部分**返回、未完成实例补「全部不可用」空样本（CP 落 NULL 断点，绝不把缺测伪装成 0），并记 WARN——**不静默丢弃**。另有防御性闸门：若预算与节拍不再自洽（`directprobe.BudgetCoversTick` 为假），首次即 WARN 一次，避免退化成"部分实例指标静默消失"。
- **实时链路的 CP 侧预算**：`InstanceService.GetMetrics` 的 gRPC 截止时间随生效直探超时与实例实际配置的来源端口动态给出（`metricsFetchTimeout`），以覆盖 Worker 侧**串行** `探针(HTTP ≤5s)→SLP(t)→Query(t)` 的最坏时延（固定 10s 在默认 3s 下即已偏紧、上界放大时必然超时被 DROP）。
- **并发**：复用 `heartbeat.go` 的 `maxConcurrentProbeScrapes=8` 信号量；SLP/Query 在**同一实例任务内串行**（先探针，失败才 SLP，再 Query），避免为每实例翻倍 UDP/TCP 连接。
- **明文约束**：Query 是 UDP、SLP 是 TCP，均**无 TLS**（MC 协议本身明文）——仅限**同机 localhost / 内网**直连，CP 侧不下发游戏端口外的裸探（与二进制 FR-441 的 https 约束不同：此处是协议内建限制，不是传输选择）。
- **降噪**：按**稳定判据**做"变化才告警"（`unreachableSourcesSignature` 只含"哪些来源不可用"的来源标签集合），错误类别抖动（timeout↔connection refused↔unreachable）或来源进出退避（错误文案↔"退避中"）都不触发重发 WARN；含错误类别/退避标注的完整文案（`unreachableSourcesReason`）只作告警正文附带。
- **插件名形态判别**：Query 的 `plugins` 字段只在 ":" 之前**像服务端软件描述段**时才剥离该前缀（`looksLikeServerSoftware`：含 `" on "` 的 `<软件> on <平台>` 形态，或整个前缀恰为已知软件名），避免把含 ":" 的插件名（如 `MyPlugin:v1.0`）误截。

## 3. 任务拆分

1. **SLP 客户端 + 单测**（`metrics/slp.go`）：VarInt 编解码、握手/请求构造、响应 JSON 解析、MOTD 扁平化；含超时与 legacy 判定。**无依赖，可先做。**
2. **Query 客户端 + 单测**（`metrics/query.go`）：UDP 握手取 token、full stat 分片拼接、键值 + 玩家列表解析。**无依赖。**
3. **proto 扩展 + 端口下发**（`worker.proto` → `workerpb`、`manager.go`、CP 注册链）：`server_port`/`query_port` 贯通到 Worker。**依赖 1/2 确定所需字段。**
4. **编排链 +「不可用」语义**（`metrics/orchestrate.go` + 改写 `heartbeat.go:collectInstanceMetrics` 与 `grpc/server.go:GetInstanceMetrics`，两条链共用）。**依赖 1/2/3。**
5. **CP 落库与 API**（`metric.go` 补落点、`instance.go:GetMetrics`/`MetricsData` 扩字段、`api/metrics.ts` 类型）。**依赖 4。**
6. **前端清除 `-1`/`--`**：`InstanceConsolePage.tsx`、`MetricsSegment.tsx`、`InstanceWorktableCard.tsx` 等按可用性位渲染「不可用」/来源标注。**依赖 5。**
7. **文档同步**：PRD 状态、ARCHITECTURE（采集链）、API（`GetInstanceMetrics`）、CHANGELOG。

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | 任意在跑 MC 服**不装探针**即可通过 SLP 拿到 MOTD / 版本 / 在线人数 / 最大人数 | 真机（要真机过） |
| 2 | 开 `enable-query=true` 后可经 Query 拿到**实名玩家名单** / 插件列表 / 地图名 | 真机（要真机过） |
| 3 | BungeeCord/Velocity 代理实例（无法装 Bukkit 探针）经 SLP 可见 | 真机（要真机过） |
| 4 | 三态（探针+直探 / 仅直探 / 都无）下详情页均正确：有探针以探针为准，仅直探回落直探，都无显「不可用」 | 真机 + 三态构造 |
| 5 | **无 `-1`/`--` 占位垃圾**：不可用显示为文字「不可用」，不出现伪造数值 | 真机 + 前端断言 |
| 6 | Query 未开 / 端口封堵时**超时**返回，不卡住心跳、不 panic | 真机（负例） |
| 7 | SLP/Query 超时可配；心跳 30s 节奏不被拖慢（实例多时并发受限） | 单元 + 真机 |
| 8 | 时序链路（心跳落库）与实时链路（`GetInstanceMetrics`）来源一致、语义一致 | 单元 + 真机 |
| 9 | 玩家名单优先级：Query 有则用 Query，Query 无回 SLP sample 并标注"可能不完整" | 单元 + 真机 |

## 5. 风险 / 待定

- **Query 分片头格式未真机验证**（FR-446 审计项 6）：本实现按「**每一片**负载都以 11 字节
  `splitnum\0` + count + index 头开头」判定多分片（`internal/worker/metrics/query.go` 的
  `querySplitHeaderLen` / `queryResponseComplete` / `reassembleQueryFullStat`）。该假设仅来自社区对
  Notchian 服务端的逆向描述，**本项目真机上只覆盖了单包路径**（现代服务端响应很小，几乎总是单包；
  只有玩家/插件极多时才分片）。遇到分片响应解析失败时，须先用 tcpdump/Wireshark 抓真实 UDP 分片再据实
  修正，不要只调常量。**标注为未真机验证项，待有条件时补真机证据。**
- **心跳采集节奏的护栏**（FR-446 审计项 2，NEW-ISSUE A 修正）：单拍采集总预算**由生效直探超时推导**
  （`heartbeat.collectInstanceBudget` = `directprobe.CollectBudgetFor`，上界 26s < 30s 节拍；不再是硬编码
  15s，否则与"可配超时"自相矛盾），
  超预算即用已完成部分返回、未完成实例按「不可用」落 NULL；持续性失败的来源按「连续失败 ≥2 次后
  指数退避（60s→120s，首档严格大于 30s 心跳节拍，且退避窗口自**探测完成时刻**起算）」跳拍，
  避免 `query.port` 已分配但 `enable-query` 未开这类实例每拍白等
  一个 UDP 超时。退避**只作用于 30s 时序采样**，详情页实时链路（`GetInstanceMetrics`）不做退避。
- **SLP sample 的可信度**：`players.sample` 可被服务端插件伪造/截断，且不含稳定 UUID。规格明确它是弱信息，**实名以 Query 为准**；若某部署对玩家身份敏感，可配置为"仅 Query 展示名单"。
- **Query 分片拼接**：跨包 UDP 顺序/丢包在公网不稳；同机 localhost 基本可靠。首版按 token 校验 + 首包总长度拼接，超长响应设上限（如 64KB）防内存放大。
- **代理服 Query**：BungeeCord 的 Query 支持情况随版本而异，SLP 才是代理的可靠保底；Query 对代理可能长期"不可用"，属预期。
- **端口下发与热更新**：`server_port`/`query_port` 变更（导入实例、端口迁移）须经 `SetServerPort/SetQueryPort` 热刷 Worker 内存表，否则直探打旧端口；与 `EnsureProbePort` 同风格补口。
- **旧 CP/Worker 兼容**：新 Worker 对旧 CP 时直探采样字段为空——`InstanceMetricSample` 新字段为可选，旧 CP 忽略即可；`-1` 语义移除需前后端同步发布，避免中间态前端把 0 当"0 人在线"。新 CP 对旧/中间版本 Worker 时，在线人数落库保留 `probe_available || slp_available` 兼容回退（不含 Query，见 §2.5），保证升级期 SLP-only 实例的在线人数不整段落 NULL。
