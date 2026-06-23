# Spec — FR-076 全量 Bukkit 状态探查（异步非侵入）+ FR-077「服务器状态」专属 tab

> 关联 FR: FR-076 / FR-077 | 优先级: P2 | 状态: 🔨 in-progress | 关联 ADR: ADR-016（复用 FR-065 的反向 WS 桥 + `QueryServerState` 通道）

## 概述

FR-076 把 ServerProbe 探针 fork 的能力从「指标采集（/metrics）+ 治理（FR-067）」扩展到「**全量内部状态按需探查**」：探针在 Bukkit 子服内异步、非侵入地采集服务器/世界/JVM/**class 加载器**/调度器/监听器等内部数据，经已有的反向 WS 桥按需请求/响应返回。FR-077 在实例工作区新增「服务器状态」段，以 FR-061 风格的分区密集表展示这份全量状态 + class 加载器专区，手动刷新（按需，不持续轮询）。

**复用而非新建通道**：FR-065 已铺好反向 WS 桥与 gRPC `QueryServerState`/`SendPluginCommand`（带 request_id 同步往返）。本 FR 不改 proto，只填 `QueryServerState` 的 `state_json` 语义、加一个探针侧 `query_state` 治理动作、CP 加一个查询服务/端点、前端加一个 tab。

## 数据流（复用 FR-065/067 链路）

```
前端开 tab/点刷新
  → GET /api/v1/instances/:id/server-state (CP HTTP)
  → ServerStateService 解析 instance→node→gRPC client
  → gRPC QueryServerState(instance_uuid) (CP→Worker)
  → Worker bridge.SendCommandAndWait(action=query_state, request_id, 5s) (Worker→探针 WS)
  → 探针 BukkitBridgeCommandHandler.dispatch(query_state)
       → 主线程快照（有界）服务器/世界/调度器/监听器 + 任意线程采 JVM/classloader
       → Json.encode(全状态 map) 作为 command_result.output 回传
  → Worker 把 output 填入 QueryServerStateResponse.state_json
  → CP 透传 state_json（已是 JSON 字符串）+ connected
  → 前端解析渲染分区密集表
```

- **轻指标仍走 /metrics**（TPS/MSPT/堆/在线 等历史时序，FR-060/061 不变）；本 FR 只承载「开 tab/刷新才查」的全量结构化快照。
- **探针未连入**：`QueryServerState` 回 `connected=false`，CP 返回 `connected:false` + 空状态，前端降级提示「探针未连入」。

## proto

**不改 proto**。复用 FR-065 既有：

- `rpc QueryServerState(QueryServerStateRequest) returns (QueryServerStateResponse)`
- `QueryServerStateResponse { bool success; string error; bool connected; string state_json; }`

Worker 侧 `QueryServerState` 从骨架（仅回 connected）升级为：探针在线时经 `SendCommandAndWait(action=query_state)` 取回 `state_json`；离线/超时时 `success=true, connected=false, state_json=""`（优雅降级，不报错）。

## Worker 侧

`internal/worker/grpc/plugin_bridge.go` 的 `QueryServerState`：

- `bridge == nil` → `success=false, error="本节点未启用插件桥"`。
- `!bridge.IsConnected(uuid)` → `success=true, connected=false, state_json=""`（探针未连入，降级）。
- 已连入 → 构造 `query_state` 指令帧（`pluginCommandFrame` 复用，action=`query_state`，新 request_id），`bridge.SendCommandAndWait(uuid, requestID, frame, pluginCommandTimeout)`：
  - 成功 → `success=res.Success, connected=true, state_json=res.Output, error=res.Error`。
  - `ErrBridgeNotConnected` → `connected=false, state_json=""`（探针刚断）。
  - `ErrBridgeCommandTimeout` → `success=true, connected=true, state_json="", error="状态查询超时"`（探针在但采集超时，降级 N/A）。

新增动作常量 `pluginActionQueryState = "query_state"`（与探针侧约定一致）。

## 探针 fork 侧（submodule）

### core（平台无关）

- `BukkitStateCollector` 接口下沉到 `core`（mirror `BridgeCommandHandler` 的解耦范式），或直接在 platform-bukkit 实现 `query_state` 分支——**采前者**：core 不依赖 Bukkit；platform-bukkit 实现并经 `query_state` 注入。
- 新增 core 动作常量 `query_state`。

### platform-bukkit

`BukkitBridgeCommandHandler.dispatch` 加 `query_state` 分支 → 调用 `BukkitServerStateCollector.collect()` → `Json.encode(map)` 作为 `BridgeCommandResult.ok(output=json)`。

`BukkitServerStateCollector`（新增）采集（**异步非侵入、有界、超时降级**）：

- **server**：版本 / Bukkit 版本 / MOTD / 视距(view-distance) / 模拟距离 / 在线·最大人数 / 白名单开关 / 在线模式 / 难度 / 插件清单（名/版本/启用态，**有界**：超 N 个截断计数）。
- **worlds**：每个世界名 / 环境 / 已加载区块数 / 实体数 / 方块实体(tile entity)数 / 玩家数 / 难度 / 部分关键 gamerule（**有界**：世界数与每世界字段都设上限）。
- **jvm**：堆 used/max/committed、非堆、线程数、JVM 名/版本/vendor、运行时长、GC 次数/耗时、可用处理器数。
- **classloader**（专区，FR-076 重点）：插件类加载器层级（每插件名 → classloader 类名 + parent 链摘要）、**已加载类计数**（经 `ClassLoadingMXBean.loadedClassCount`/`totalLoadedClassCount`/`unloadedClassCount`）。类**枚举不全量 dump**（避免大数据 + 反射越界）：仅计数 + 按插件分组的加载器标识 + 可选采样前 N 个类名。
- **scheduler**：待执行任务数（`Bukkit.getScheduler().getPendingTasks().size`）、活跃 worker 数。
- **listeners**：已注册事件数 / 监听器数摘要（经 `HandlerList`，**有界**）。

非侵入约束（PRD 验收）：

- **主线程快照尽量短**：仅取需要 Bukkit 主线程的只读数据（世界/区块/实体/插件/调度器/监听器），用 `submit(async=false)` + `CountDownLatch` 限时（复用 handler 已有的 `SYNC_TIMEOUT_SECONDS=3s`），超时即该段降级为 N/A，**绝不拖慢服务器**。
- **JVM / classloader 计数**经 `ManagementFactory` MXBean，**不需主线程**，可在桥读线程直接采。
- 单次有界：插件/世界/类名采样均设上限计数，避免大对象。
- 整体 `runCatching` 兜底：任何子项异常 → 该项 N/A，不影响其余、不抛（沿用「探针只读优先、绝不成为事故源」）。

## CP 侧

`internal/controlplane/service/server_state.go`（新增 `ServerStateService`）：

- 持有 `pool *cpgrpc.ClientPool` + `db *gorm.DB`（mirror `PlayerService`）。
- `QueryState(instanceID uint)`：按 ID 取实例 → 取 node → `pool.Get(node.UUID)` → `client.Worker.QueryServerState(uuid)`；返回 `ServerStateResult { connected bool; available bool; stateJson json.RawMessage; error string }`。
- `state_json` 已是探针手拼的 JSON 字符串，CP **不解析**，作为 `json.RawMessage` 透传给前端（前端按结构渲染；探针字段演进不需改 CP）。
- 节点未连/探针未连/空 state_json → `available=false` / `connected=false` + 友好提示，HTTP 200（降级，不 5xx）。

`internal/controlplane/router/instance.go`（或新增 `server_state.go` handler，**采新文件**避免膨胀 instance.go）：

### GET /api/v1/instances/:id/server-state
- **描述**: 按需查询某实例的全量 Bukkit 内部状态（server/worlds/jvm/classloader/scheduler/listeners），经探针反向 WS 桥的 `QueryServerState` 同步取回（FR-076）。轻指标走 /metrics；本端点仅在前端开 tab/手动刷新时调用，探针未连入或采集超时时降级（`connected=false` 或 `available=false`）
- **权限**: `instance.read`（且实例须可访问）
- **响应**: `{ "instanceId":3, "connected":true, "available":true, "state": { "server":{...}, "worlds":[...], "jvm":{...}, "classloader":{...}, "scheduler":{...}, "listeners":{...} }, "error":"" }`
  - `connected=false`：探针未连入 → `state` 为 `null`，前端提示部署/连接探针
  - `available=false` 且 `connected=true`：探针在但本次采集超时/失败 → `error` 说明，前端提示重试
- **错误**: `403 FORBIDDEN`、`404 NOT_FOUND`（实例不可见/不存在）
- **关联 FR**: FR-076 ｜ **关联 ADR**: ADR-016

## 前端（FR-077）

- `web/src/stores/console.ts`：`WorkspaceSegment` 加 `'serverstate'`。
- `web/src/components/console/WorkspacePane.tsx`：tab 列表加「服务器状态」（i18n `serverState.tab`），段渲染 `ServerStateSegment`。
- `web/src/api/serverState.ts`（新增）：`useServerState(instanceId)` 调 `GET /instances/:id/server-state`，**默认不自动轮询**（`refetchInterval: false`），手动 `refetch` 刷新（按需）。
- `web/src/components/console/ServerStateSegment.tsx`（新增）：
  - 顶部：探针连接状态 + 「刷新」按钮（FR-061 风格的密集头）。
  - 探针未连入 → 降级卡片提示。
  - 分区密集表（复用 `Panel` + 紧凑 `<table>`）：server / worlds（每世界一行）/ jvm / **classloader 专区**（加载器层级 + 类计数）/ scheduler / listeners。
  - 大数据（插件清单、类采样、世界列表）**分页/折叠不卡**：超阈值默认折叠，点击展开；纯前端切片，不二次请求。
- i18n：`web/src/i18n/locales/{zh,en}.ts` 各加 `serverState.*` 键（仅加自己键，不动他人）。

## 验收对齐（PRD FR-076 / FR-077）

FR-076：
- [ ] 探针扩展状态面：server/worlds/JVM/**class 加载器**/调度器/监听器等内部数据。
- [ ] 异步 off-main-thread（JVM/classloader 经 MXBean 不占主线程）+ 主线程快照有界限时 + 超时降级 N/A，绝不拖慢服务器。
- [ ] 经 WS 按需请求/响应（开 tab/刷新才查），复用 FR-065 `QueryServerState`；轻指标仍走 /metrics；不支持项降级。
- [ ] 真机：真 Paper 按需拉全状态返回真实数据，TPS 无可感下降（无 Paper/JDK21 时如实标「待真机验」）。

FR-077：
- [ ] 新增「服务器状态」tab；展示 FR-076 全量状态分区密集表（FR-061 风格）+ class 加载器专区。
- [ ] 手动刷新（按需，不持续轮询）；加载/失败/降级态清晰；大数据分页/折叠不卡。
- [ ] 真机：tab 渲染真 Bukkit 全状态、刷新生效、classloader 区有数据（无 Paper 时如实标「待真机验」）。

## 测试

- Go 单测：`QueryServerState`（worker）分支——bridge nil / 未连接 / 连接成功（mock bridge 返回 state_json）/ 超时降级；`ServerStateService.QueryState`（CP）——节点未连/实例不存在/成功透传 state_json。CP handler 权限/可见性。
- Kotlin 单测（若 core 可测）：`BukkitServerStateCollector` 的有界/降级逻辑——平台无关部分（计数截断、N/A 容错）抽到 core 可测；Bukkit API 部分真机验。
