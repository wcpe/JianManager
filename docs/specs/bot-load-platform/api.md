# API 规格：500+ Bot 分布式压测平台

> 状态：已审核（2026-07-18）　·　关联 FR：FR-351～357
> 基础路径：`/api/v1`　·　共享设计：`super-spec.md`
> 所有 JSON 字段使用 camelCase；时间为 RFC3339；ID 路径参数使用数据库数字 ID，响应同时返回 UUID。

## 1. 权限与通用错误

| 操作 | 权限 | 资源隔离 |
|---|---|---|
| 节点容量、模板、运行、指标、报告读取 | `bot:read` | 必须可访问目标实例 |
| 模板写、运行创建/预检/启动/停止/取消/重试 | `bot:manage` | 必须可管理目标实例 |
| 平台管理员 | 全量 | 仍写审计 |

通用错误：

| HTTP | code | 含义 |
|---:|---|---|
| 400 | INVALID_REQUEST | JSON/Query/分页/字段格式错误 |
| 401 | UNAUTHORIZED | 未登录或 token 失效 |
| 403 | FORBIDDEN | 无权限 |
| 404 | NOT_FOUND | 资源不存在或无权访问时隐藏存在性 |
| 409 | BOT_LOAD_INVALID_STATE | 当前运行状态不允许操作 |
| 409 | BOT_LOAD_CAPACITY_CHANGED | 预检后节点容量/世代变化，必须重新预检 |
| 409 | BOT_LOAD_REPORT_NOT_READY | 运行未终态，报告尚不可导出 |
| 422 | BOT_LOAD_SCENARIO_INVALID | 场景 V2 校验失败，details 含 path/message |
| 422 | BOT_LOAD_PROFILE_INVALID | 负载曲线校验失败 |
| 422 | BOT_LOAD_THRESHOLDS_INVALID | 阈值校验失败 |
| 422 | BOT_LOAD_PROBE_REQUIRED | 场景含可信事件步骤，但目标实例探针未连接 |
| 422 | BOT_LOAD_CAPACITY_INSUFFICIENT | 可用容量小于目标 Bot 数 |
| 503 | BOT_LOAD_NODE_UNAVAILABLE | 发压节点/Worker/bot-worker 不可用 |
| 503 | BOT_LOAD_STREAM_UNAVAILABLE | 会话聚合流不可用 |

结构化错误沿现有 API 信封；`details` 可加性包含：

```json
{
  "code": "BOT_LOAD_SCENARIO_INVALID",
  "message": "场景步骤校验失败",
  "details": {
    "path": "cohorts[1].steps[3].timeoutMs",
    "reason": "必须大于 0"
  }
}
```

## 2. 公共类型

### 2.1 BotLoadNodeCapacity

```ts
interface BotLoadNodeCapacity {
  nodeId: number
  nodeUuid: string
  nodeName: string
  online: boolean
  tunnelConnected: boolean
  botWorkerReady: boolean
  legacy: boolean
  maxBots: number
  activeBots: number
  reservedBots: number
  availableBots: number
  capacityGeneration: number
  workerEpoch?: string
  botWorkerVersion?: string
  runtimeSource?: string
  rssBytes?: number
  eventLoopP95Ms?: number
  lastHeartbeatAt?: string
  unavailableReason?: string
}
```

`availableBots = max(0, maxBots-activeBots-reservedBots)`；旧 Worker/bot-worker `legacy=true`，不参与分布式预检。

### 2.2 BotLoadAllocation

```ts
interface BotLoadAllocation {
  batchId: string
  ordinal: number
  executorNodeId: number
  executorNodeUuid: string
  executorNodeName: string
  plannedCount: number
  connectStartAt: string
  connectIntervalMs: number
  idempotencyKey: string
}
```

### 2.3 BotLoadPreflightResult

```ts
interface BotLoadPreflightResult {
  runId: number
  runUuid: string
  ready: boolean
  planToken?: string
  expiresAt?: string
  targetBots: number
  totalAvailable: number
  allocations: BotLoadAllocation[]
  nodeCapacities: BotLoadNodeCapacity[]
  probe: {
    required: boolean
    connected: boolean
    instanceId: number
    instanceUuid: string
    message?: string
  }
  estimatedDurationSeconds: number
  warnings: Array<{ code: string; message: string }>
  blockers: Array<{ code: string; message: string; nodeId?: number }>
}
```

`planToken` 是服务端签名/哈希的短期计划标识，不是凭据；默认 60 秒过期。`ready=false` 时不返回 planToken。

### 2.4 场景类型

```ts
type BotLoadActionType =
  | 'wait_spawn'
  | 'roam_in_area'
  | 'send_command'
  | 'wait_probe_event'
  | 'barrier'
  | 'move_to_and_wait'
  | 'find_entity'
  | 'attack_until'
  | 'wait'
  | 'respawn_and_rejoin'

interface BotLoadScenarioV2 {
  version: 2
  seed: number
  cohorts: BotLoadCohort[]
}

interface BotLoadCohort {
  key: string
  percent: number
  steps: BotLoadAction[]
}

interface BotLoadAction {
  id: string
  type: BotLoadActionType
  timeoutMs?: number
  maxAttempts?: number
  retryBackoffMs?: number
  resumePolicy?: 'restart_step' | 'restart_scenario' | 'fail'
  [key: string]: unknown
}
```

详细字段按 `super-spec.md §8`；服务端响应保留完整结构。

### 2.5 负载与阈值

```ts
type BotLoadProfile =
  | { type: 'stable'; targetBots: number; rampUpSeconds: number; durationSeconds: number }
  | { type: 'step'; stages: Array<{ targetBots: number; holdSeconds: number }>; stopOnThresholdFailure: boolean }
  | { type: 'spike'; targetBots: number; connectWindowSeconds: number; barrierKey?: string; releaseWindowMs: number; holdSeconds: number }

interface BotLoadThresholds {
  minOnlineRate: number
  minRoomJoinRate: number
  minArrivalRate: number
  minAttackSuccessRate: number
  minTps: number
  maxMsptP95: number
  maxProcessCrashes: number
  safety?: {
    minTps: number
    maxMsptP95: number
    sustainSeconds: number
    maxExecutorMemoryRate: number
    maxEventLoopP95Ms: number
  }
}
```

### 2.6 BotLoadTemplate

```ts
interface BotLoadTemplate {
  id: number
  uuid: string
  name: string
  description: string
  scenario: BotLoadScenarioV2
  loadProfile: BotLoadProfile
  thresholds: BotLoadThresholds
  tags: string[]
  createdBy: number
  createdAt: string
  updatedAt: string
}
```

### 2.7 BotLoadRun

```ts
type BotLoadRunState =
  | 'pending' | 'preflighting' | 'ready' | 'starting'
  | 'running' | 'degraded' | 'stopping' | 'cancelling'
  | 'completed' | 'failed' | 'cancelled'

type BotLoadVerdict = 'pending' | 'passed' | 'failed' | 'aborted'

interface BotLoadRun {
  id: number
  uuid: string
  templateId?: number
  instanceId: number
  instanceName?: string
  name: string
  namePrefix: string
  targetBots: number
  status: string
  runState: BotLoadRunState
  verdict: BotLoadVerdict
  currentStage: number
  scenario: BotLoadScenarioV2
  loadProfile: BotLoadProfile
  thresholds: BotLoadThresholds
  allocations: BotLoadAllocation[]
  counts: {
    planned: number
    accepted: number
    connecting: number
    connected: number
    disconnected: number
    failed: number
    stopped: number
  }
  actionCounts: Record<string, { running: number; succeeded: number; failed: number; timedOut: number; cancelled: number }>
  maxStableBots: number
  failureSummary: Record<string, number>
  startedAt?: string
  endedAt?: string
  createdAt: string
  updatedAt: string
}
```

## 3. 发压节点与预检（FR-351）

### GET `/bots/load-nodes`

- **权限**：`bot:read`
- **Query**：`instanceId` 必填；目标实例用于权限和目标地址校验。
- **响应**：200

```json
{
  "items": [/* BotLoadNodeCapacity */],
  "totalCapacity": 600,
  "availableCapacity": 520,
  "updatedAt": "2026-07-18T12:00:00Z"
}
```

- **错误**：400、404。
- **关联 FR**：FR-351。

### POST `/bots/stress-sessions/:id/preflight`

- **权限**：`bot:manage`
- **请求**：

```json
{
  "executorNodeIds": [2,3,4,5,6,7,8,9,10,11],
  "connectRatePerSecondPerNode": 5
}
```

字段：

- `executorNodeIds` 可空；空表示由 CP 从所有可用节点选择。
- 节点 ID 去重，最多 256 个。
- `connectRatePerSecondPerNode` 可空，默认 5，范围 1..50；spike 模式可由 profile 覆盖为更高瞬时计划，但仍受每节点 maxBots 限制。

- **响应**：200 `BotLoadPreflightResult`。容量不足也返回 200 + `ready=false` + blocker，便于向导展示；场景/profile 本身非法仍返回 422。
- **副作用**：成功时把 allocationPlan 写入会话，并为容量建立 60 秒软预留；不创建 Bot、不启动连接。
- **错误**：404、409 INVALID_STATE、422 SCENARIO/PROFILE/PROBE。
- **关联 FR**：FR-351/355。

### POST `/bots/stress-sessions/:id/start`

扩展既有端点。

- **权限**：`bot:manage`
- **请求**：`{ "planToken": "..." }`
- **响应**：202 `BotLoadRun`
- **行为**：
  - planToken 必须未过期且节点容量 generation 未变化。
  - 创建 Bot/批次和后台运行任务后立即返回，不同步等待 500 Bot 连接。
  - 重复相同 planToken 幂等返回同一运行状态。
- **错误**：404、409 INVALID_STATE/CAPACITY_CHANGED、422 CAPACITY_INSUFFICIENT/PROBE_REQUIRED、503 NODE_UNAVAILABLE。
- **关联 FR**：FR-351/355。

## 4. 模板与运行（FR-355）

### GET `/bots/load-templates`

- **权限**：`bot:read`
- **Query**：`page=1&pageSize=20&q=&tag=`，pageSize 1..100。
- **响应**：200 `{items: BotLoadTemplate[], total, page, pageSize}`。
- **关联 FR**：FR-355/356。

### POST `/bots/load-templates`

- **权限**：`bot:manage`
- **请求**：

```json
{
  "name": "塔防 500 人严格压测",
  "description": "20% 主城，80% 战斗",
  "scenario": {/* BotLoadScenarioV2 */},
  "loadProfile": {/* BotLoadProfile */},
  "thresholds": {/* BotLoadThresholds */},
  "tags": ["tower-defense", "strict"]
}
```

- **响应**：201 `BotLoadTemplate`。
- **错误**：400、409 名称冲突使用现有 BUSINESS_ERROR 或 422 校验错误。
- **关联 FR**：FR-355/356。

### GET `/bots/load-templates/:id`

- **权限**：`bot:read`
- **响应**：200 `BotLoadTemplate`。
- **错误**：404。

### PUT `/bots/load-templates/:id`

- **权限**：`bot:manage`
- **请求**：同创建；全量替换可编辑字段。
- **响应**：200 `BotLoadTemplate`。
- **错误**：404、409 名称冲突、422 校验错误。

### DELETE `/bots/load-templates/:id`

- **权限**：`bot:manage`
- **响应**：200 `{message}`。
- **语义**：软删；历史运行 snapshot 不受影响。
- **错误**：404。

### POST `/bots/load-templates/:id/runs`

- **权限**：`bot:manage`
- **请求**：

```json
{
  "instanceId": 1,
  "name": "2026-07-18 晚间严格压测",
  "namePrefix": "td-0718",
  "config": {"server":"127.0.0.1","port":25565,"auth":"offline"},
  "executorNodeIds": [2,3,4,5,6,7,8,9,10,11],
  "scenarioOverride": null,
  "loadProfileOverride": null,
  "thresholdsOverride": null
}
```

- **响应**：201 `BotLoadRun`（runState=pending）。
- **语义**：冻结模板当前版本；后续编辑模板不影响该运行。
- **连接配置白名单**：V2 只接受 `server`（非空，≤255）、`port`（1..65535）、`auth="offline"`、可选 `version`；出现 password/accessToken/refreshToken/clientToken 等凭据字段或 auth!=offline 直接 422。旧 V1 Microsoft 配置只读兼容，不允许经 V2 创建/更新。
- **错误**：404、422。
- **关联 FR**：FR-355/356。

### POST `/bots/stress-sessions`

扩展既有临时运行创建接口。新增可选字段：

```json
{
  "instanceId": 1,
  "count": 500,
  "name": "临时压测",
  "namePrefix": "load",
  "config": {"server":"127.0.0.1","port":25565,"auth":"offline"},
  "scenario": {/* V2，可选 */},
  "loadProfile": {/* 可选；默认 stable */},
  "thresholds": {/* 可选；默认严格 */},
  "orchestrationYaml": "旧 V1，可选"
}
```

规则：

- `scenario` 与 `orchestrationYaml` 不得同时提供。
- 都不提供时，由旧 `behavior` 生成单 cohort 兼容场景。
- `count` 与 loadProfile 最大 targetBots 必须一致；不一致返回 422。
- V2 `config` 只接受 server/port/auth=offline/version 白名单；任何账号凭据字段或 auth!=offline 返回 422。
- thresholds 要求可信 arrival/attack 时，场景缺 area_arrived 或可信 damage/kill/probe 条件返回 422。
- 响应：201 `BotLoadRun`，保留旧客户端需要的 count/behavior/orchestrationSummary 字段。

### GET `/bots/stress-sessions`

扩展 Query：`page&pageSize&q&instanceId&runState&verdict&profileType&createdFrom&createdTo`。

响应：200 `{items: BotLoadRun[], total, page, pageSize}`；列表响应可省略完整 scenario，只返回 `scenarioSummary`，详情返回完整 snapshot。

### GET `/bots/stress-sessions/:id`

响应：200 `BotLoadRun` 完整快照。

### POST `/bots/stress-sessions/:id/stop`

- **权限**：`bot:manage`
- **请求**：`{ "reason": "人工停止" }`，reason 可空，最长 255。
- **响应**：202 `BotLoadRun`（runState=stopping）。
- **语义**：有序停止、等待动作收束并生成报告；最终 completed。
- **错误**：404、409 INVALID_STATE。
- **关联 FR**：FR-355。

### POST `/bots/stress-sessions/:id/cancel`

- **权限**：`bot:manage`
- **请求**：`{ "reason": "配置错误" }`。
- **响应**：202 `BotLoadRun`（runState=cancelling）。
- **语义**：尽快停止；最终 cancelled，verdict=aborted。
- **错误**：404、409。

### POST `/bots/stress-sessions/:id/retry-failed`

- **权限**：`bot:manage`
- **请求**：

```json
{
  "botIds": [101,102],
  "errorCodes": ["CONNECT_TIMEOUT"],
  "fromStepId": "join"
}
```

目标二选一：`botIds` 或 `errorCodes`；都空表示全部当前失败 Bot。仅运行中/degraded 可调用。

- **响应**：202 `{requested, accepted, skipped, errors:[{botId,error}]}`。
- **错误**：404、409、422。
- **关联 FR**：FR-354/355/357。

## 5. 指标、失败和报告（FR-355/357）

### GET `/bots/stress-sessions/:id/metrics`

- **权限**：`bot:read`
- **Query**：
  - `from`/`to` RFC3339，可空；默认运行全时段或最近 1 小时。
  - `resolution=raw|15s|1m|5m`，默认服务端按区间自动选择。
- **响应**：

```ts
interface BotLoadMetricPoint {
  timestamp: string
  stageIndex: number
  botCounts: Record<string, number>
  actionCounts: Record<string, number>
  executor: Array<{ nodeId: number; activeBots: number; rssBytes: number; eventLoopP95Ms: number; cpuPercent?: number }>
  target: { tps?: number; msptP95?: number; onlinePlayers?: number; cpuPercent?: number; memoryBytes?: number }
  latency: { connectP50?: number; connectP95?: number; connectP99?: number; actionP95?: number }
  errors: Record<string, number>
}
interface BotLoadMetricsResponse {
  points: BotLoadMetricPoint[]
  resolution: string
  from: string
  to: string
}
```

单响应点数默认 ≤1200；超出自动下采样。

### GET `/bots/stress-sessions/:id/bots`

运行内 Bot 明细，避免前端用全局 `/bots` 拼接。

- **Query**：`page&pageSize&q&status&cohortKey&executorNodeId&stepId&errorCode`；pageSize 1..100。
- **响应**：

```json
{
  "items": [{
    "id": 1,
    "uuid": "...",
    "name": "td-001",
    "status": "connected",
    "cohortKey": "combat",
    "executorNodeId": 2,
    "executorNodeName": "load-01",
    "currentStepId": "attack",
    "generation": 2,
    "reconnectCount": 1,
    "lastSeenAt": "...",
    "lastErrorCode": "",
    "lastError": ""
  }],
  "total": 500,
  "page": 1,
  "pageSize": 50
}
```

### GET `/bots/stress-sessions/:id/failures`

- **Query**：`page&pageSize&errorCode&executorNodeId&stepId&from&to`。
- **响应**：

```json
{
  "summary": [{"errorCode":"CONNECT_TIMEOUT","count":8,"category":"network"}],
  "items": [{
    "botId": 101,
    "botUuid": "...",
    "botName": "td-101",
    "executorNodeId": 3,
    "stepId": "join",
    "actionRunId": "...",
    "errorCode": "PROBE_EVENT_TIMEOUT",
    "message": "30 秒内未收到 room_joined",
    "occurredAt": "...",
    "retryable": true
  }],
  "total": 8,
  "page": 1,
  "pageSize": 20
}
```

### GET `/bots/stress-sessions/:id/events`

- **权限**：`bot:read`。
- **Query**：`page&pageSize&type&eventId&actionRunId&botId&executorNodeId&stepId&matchState&from&to`，pageSize 1..100。
- **响应**：

```json
{
  "items": [{
    "eventId": "uuid",
    "type": "area_arrived",
    "botId": 101,
    "botUuid": "...",
    "botName": "td-101",
    "executorNodeId": 3,
    "stepId": "move",
    "actionRunId": "...",
    "correlationToken": "...",
    "roomId": "room-1",
    "areaId": "combat-zone-a",
    "occurredAt": "...",
    "receivedAt": "...",
    "matchState": "consumed",
    "unmatchedReason": "",
    "late": false,
    "payload": {}
  }],
  "total": 100,
  "page": 1,
  "pageSize": 20
}
```

- **来源**：持久化 ActionResult、ProbeEvent 和关键 run/stage/executor 事件的统一只读投影；普通每次攻击尝试不进入列表。
- **关联 FR**：FR-353/355/357。

### GET `/bots/stress-sessions/:id/report`

- **Query**：`format=json|csv`，默认 json。
- **响应**：
  - json：200 `BotLoadReport`。
  - csv：200 `text/csv; charset=utf-8`，attachment。
- **错误**：404、409 REPORT_NOT_READY。

```ts
interface BotLoadReport {
  run: BotLoadRun
  verdictReasons: Array<{metric:string; expected:string; actual:string; passed:boolean}>
  stages: Array<{index:number; targetBots:number; startedAt:string; endedAt:string; verdict:string}>
  maxStableBots: number
  latencySummary: Record<string, number>
  failureSummary: Array<{category:string; errorCode:string; count:number}>
  executorSummary: Array<{nodeId:number; nodeName:string; peakBots:number; peakRssBytes:number; eventLoopP95Ms:number; crashes:number}>
  targetSummary: {minTps?:number; msptP95?:number; peakPlayers?:number; crashes:number}
  actionSummary: Record<string, {total:number;succeeded:number;failed:number;timedOut:number;rate:number}>
  generatedAt: string
}
```

## 6. 会话级 SSE（FR-357）

### GET `/bots/stress-sessions/:id/stream`

- **权限**：`bot:read`。
- **协议**：SSE，支持 `Last-Event-ID`。
- **事件**：

| event | data |
|---|---|
| init | `{run: BotLoadRun, lastEventId}` |
| run-state | `{runState, verdict, currentStage, timestamp}` |
| counts | `{counts, actionCounts, timestamp}` |
| stage | `{stageIndex, targetBots, state, timestamp}` |
| metric | 单个聚合 `BotLoadMetricPoint` |
| failure | `{errorCode, category, delta, sample?}` |
| warning | `{code, message, timestamp}` |
| complete | `{verdict, reportReady, timestamp}` |

约束：

- 心跳注释帧每 15 秒。
- 每运行每秒最多推送 5 条聚合事件；高频 Bot 事件在服务端合并。
- 所有 SSE 都是可丢的增量，持久化 run 快照/metric/event 表才是真源。服务端每连接发送队列上限 256；慢消费者队列满时主动断开，不做无界缓存。
- `run-state/stage/complete` 必须先持久化再发布；客户端断线或 CP 重启后通过 init 快照恢复，不承诺内存事件永久重放。
- eventId 仅在单次 CP 进程 epoch 内单调；Last-Event-ID 命中当前 ring 时补发，过期/跨进程 epoch 时返回 init 最新快照。
- 运行终态后连接发送 complete 并关闭。
- 错误：404、503 STREAM_UNAVAILABLE。

## 7. ServerProbe 事件契约（FR-353）

不新增浏览器端点；复用 Worker `PluginEvent` 上行流。事件必须：

```text
domain = bot_load
dedup_key = eventId
request_id = correlationToken
raw_json = BotLoadProbeEnvelope JSON
```

支持事件类型：

- `target_server_entered`
- `room_joined`
- `room_ready`
- `area_arrived`
- `game_started`
- `wave_started`
- `damage_dealt`
- `monster_killed`
- `player_died`
- `player_respawned`
- `game_ended`

`raw_json` schema 见超级规格。未知事件类型持久化供诊断，但不推动动作。

## 8. 审计 action

新增 action 键并同步中英翻译：

- `bot_load.template.create`
- `bot_load.template.update`
- `bot_load.template.delete`
- `bot_load.run.create`
- `bot_load.run.preflight`
- `bot_load.run.start`
- `bot_load.run.stop`
- `bot_load.run.cancel`
- `bot_load.run.retry_failed`
- `bot_load.report.export`

## 9. 兼容性

- 原 `/bots/stress-test` 创建别名继续可用。
- 旧 start/stop 客户端未传 body 时：若会话为旧 V1 且单节点容量足够，可由服务端内部预检并执行；V2/500+ 运行缺 planToken 返回 409 CAPACITY_CHANGED 并引导预检。
- 旧响应字段 `count/behavior/orchestrationYaml/orchestrationSummary/status/counts` 保留。
- 新字段全为加性；未知字段不影响旧客户端。
