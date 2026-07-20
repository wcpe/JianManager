# API 规格：500+ Bot 分布式命令压测平台

> 状态：已审核（2026-07-20）　·　关联 FR：FR-351/352/354/358～361
> 基础路径：`/api/v1`　·　共享设计：`super-spec.md`　·　命令成功边界：`../../adr/075-bot-command-orchestration.md`
> 所有 JSON 字段使用 camelCase；时间为 RFC3339；路径 ID 使用数字 ID，响应同时返回 UUID。

## 1. 权限与通用错误

| 操作 | 权限 | 资源隔离 |
|---|---|---|
| 节点容量、运行、指标、报告读取 | `bot:read` | 必须可访问目标实例 |
| 运行创建/预检/启动/停止/取消/重试 | `bot:manage` | 必须可管理目标实例 |
| 模板读取 | `bot:read` | 非管理员仅可见 `createdBy=当前用户` 的个人模板；平台管理员可见全部 |
| 模板创建/更新/删除 | `bot:manage` | 创建者本人可写自己的模板；平台管理员可写全部；无权访问按 404 隐藏 |
| 平台管理员 | 全量 | 仍写审计 |

通用错误：

| HTTP | code | 含义 |
|---:|---|---|
| 400 | INVALID_REQUEST | JSON、Query、分页或字段格式错误 |
| 401 | UNAUTHORIZED | 未登录或 token 失效 |
| 403 | FORBIDDEN | 无权限 |
| 404 | NOT_FOUND | 资源不存在或无权访问时隐藏存在性 |
| 409 | BOT_LOAD_INVALID_STATE | 当前运行状态不允许操作 |
| 409 | BOT_LOAD_CAPACITY_CHANGED | 预检后节点容量或世代变化，必须重新预检 |
| 409 | BOT_LOAD_REPORT_NOT_READY | 运行未终态，报告尚不可导出 |
| 409 | BOT_LOAD_TEMPLATE_NAME_CONFLICT | 活跃模板名称冲突 |
| 422 | BOT_LOAD_SCENARIO_INVALID | 兼容 Scenario V2 或命令计划校验失败，details 含 path/message |
| 422 | BOT_LOAD_PROFILE_INVALID | 负载曲线校验失败 |
| 422 | BOT_LOAD_THRESHOLDS_INVALID | 阈值校验失败 |
| 422 | BOT_LOAD_CAPACITY_INSUFFICIENT | start/运行阶段即时可用容量不足 |
| 429 | RATE_LIMITED | 会话流连接数或请求频率超限 |
| 500 | INTERNAL_ERROR | 未分类服务端错误 |
| 503 | BOT_LOAD_NODE_UNAVAILABLE | start/运行阶段发压节点、Worker 或 bot-worker 不可用 |
| 503 | BOT_LOAD_STREAM_UNAVAILABLE | 会话聚合流不可用 |

不存在 `BOT_LOAD_PROBE_REQUIRED`：ServerProbe 或业务适配器缺失不得阻断通用命令压测。

结构化错误沿现有 API 信封：

```json
{
  "error":"BOT_LOAD_SCENARIO_INVALID",
  "message":"命令计划校验失败",
  "details":{"path":"commandSchedule.commands[1].atMs","message":"必须不超过 durationMs"}
}
```

## 2. 公共类型

### 2.1 节点容量

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

旧 Worker/bot-worker 返回 `legacy=true`，不参与 500+ 分布式预检。

### 2.2 命令计划与 Scenario V2 公共类型

```ts
interface BotLoadCommand {
  id: string
  atMs: number
  command: string
  repeat?: { intervalMs: number; count: number }
}

interface BotLoadCommandSchedule {
  commands: BotLoadCommand[]
  durationMs: number
  jitterMs?: number
}

interface ScenarioPosition { x:number; y:number; z:number }
type ScenarioArea =
  | {type:'radius';center:ScenarioPosition;radius:number;waypoints?:never}
  | {type:'waypoints';waypoints:ScenarioPosition[];center?:never;radius?:never}
interface ScenarioEntitySelector {
  kind?:string; types?:string[]; nameRegex?:string
  radius:number; priority?:'nearest'|'lowest_health'
}
type ScenarioBarrierRelease =
  | {type:'all';value?:never}
  | {type:'count'|'percent';value:number}
interface ScenarioAttackStop {
  durationMs:number; damageAtLeast?:number; killsAtLeast?:number
  probeEvent?:string; evidenceWindowMs?:number; minDamageEventsPerWindow?:number
  successPolicy?:'any'|'all'
}
interface ScenarioActionBase {
  id:string; timeoutMs?:number; maxAttempts?:number; retryBackoffMs?:number
  resumePolicy?:'restart_step'|'restart_scenario'|'fail'
}
type BotLoadScenarioAction = ScenarioActionBase & (
  | {type:'wait_spawn'}
  | {type:'roam_in_area';observationStep?:boolean;durationMs:number;area:ScenarioArea;pauseMs?:{min:number;max:number};maxPathFailures?:number}
  | {type:'send_command';command:string}
  | {type:'wait_probe_event';event:string}
  | {type:'barrier';key:string;release:ScenarioBarrierRelease;timeoutPolicy?:'fail'|'release-arrived'}
  | {type:'move_to_and_wait';pos:ScenarioPosition;radius:number;areaId?:string;requireProbeEvent?:string}
  | {type:'find_entity';selector:ScenarioEntitySelector}
  | {type:'attack_until';observationStep?:boolean;selector:ScenarioEntitySelector;stop:ScenarioAttackStop;attackIntervalMs:number;chase?:boolean;reacquire?:boolean;targetNotFoundTimeoutMs?:number}
  | {type:'wait';durationMs:number}
  | {type:'respawn_and_rejoin';entryStepId:string}
)
interface BotLoadScenarioV2 {
  version:2; seed:number
  cohorts:Array<{key:string;percent:number;steps:BotLoadScenarioAction[]}>
}
```

校验规则：`commands` 必须包含 1..100 个命令；`id` 长度 1..64 且匹配 `[A-Za-z0-9._-]+`，计划内唯一稳定；`command` 模板原文 UTF-8 长度 1..1024 bytes，且禁止 U+0000..U+001F 与 U+007F 控制字符（包括 CR/LF/NUL）；`durationMs` 为 1..86400000，`jitterMs` 省略时必须在校验、规范快照/config hash、occurrence 展开之前规范化为 0，提供时为 0..min(60000,durationMs)；`atMs` 为 0..durationMs；`repeat.intervalMs` 为 1..86400000，`repeat.count` 为 1..1000 且表示包含首次执行在内的总 occurrence 数；整份计划展开后的 occurrence 总数必须为 1..1000，规范 JSON 不超过 256KiB。任一基础时间 `atMs + occurrence*intervalMs` 超过 `durationMs` 时整份计划拒绝，不做运行时截断；拒绝未知字段、空变量、表达式和业务变量。展开全部 occurrence 后按 `(atMs + occurrence*intervalMs, commandDeclarationIndex, occurrence)` 稳定执行。V1 不暴露 retry 字段：仅 `bot.chat` 同步抛错最多尝试 3 次，失败后固定退避 250ms、500ms；参数/路由/取消/截止错误不重试，且任何重试不得越过 duration 或运行 deadline。

模板变量仅允许：`botName`、`botOrdinal`、`cohortKey`、`runId`、`actionRunId`、`correlationToken`。其中 `runId` 展开为 `bot_stress_sessions.id` 的十进制数字，UUID 始终使用独立字段 `runUuid` 传递，不复用 `runId` 名称。`roomKey`、`areaId`、玩法字段和未知变量均返回路径级 422。CP 在每个 Bot/occurrence 冻结 actionRunId 后必须展开最终命令，并再次校验 UTF-8 长度 1..1024 bytes 且无 U+0000..U+001F/U+007F；任一项不合格则该 Bot 计划不得调用 Apply，所有未 skip occurrence 以 `COMMAND_ARGUMENT_INVALID` 终态收敛并标出首个违规 commandId。Bot Worker 接收侧重复防御校验，禁止截断、替换或带非法文本调用 `bot.chat`。

### 2.3 预检

```ts
interface BotLoadPreflightCurrent {
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
    required: false
    connected: boolean
    instanceId: number
    instanceUuid: string
    message?: string
  }
  estimatedDurationSeconds: number
  warnings: Array<{ code: string; message: string }>
  blockers: Array<{ code: string; message: string; nodeId?: number }>
}

type BotLoadPreflightSnapshot =
  | { scenario: BotLoadScenarioV2; commandSchedule?: never; orchestrationYaml?: never }
  | { scenario?: never; commandSchedule: BotLoadCommandSchedule; orchestrationYaml?: never }
  | { scenario?: never; commandSchedule?: never; orchestrationYaml: string }
  | { scenario?: never; commandSchedule?: never; orchestrationYaml?: never }

type BotLoadPreflightPlanned = BotLoadPreflightCurrent & { instanceId:number } & BotLoadPreflightSnapshot
```

`planToken` 是短期计划标识，不是凭据；默认 60 秒过期。容量不足或节点不可用返回 200 且 `ready=false`；Scenario/命令计划、profile 或 thresholds 结构与语义非法返回对应 422，不进入 `ready=false` 响应。当前代码响应严格使用 `BotLoadPreflightCurrent`；`probe` 固定 `required=false`，不执行 ServerProbe 连接校验，`connected=false` 或数据源缺失不产生 warning/blocker，也不改变 `ready`。service 内 dormant `Required=true/BOT_LOAD_PROBE_REQUIRED` 分支不被公开 handler 调用，不属于契约；FR-359 实现前必须删除或收进不可被产品路径调用的 legacy 私有适配器。FR-358/359 落地后响应升级为 `BotLoadPreflightPlanned`，加性返回顶层 `instanceId` 和互斥的三类快照；当前客户端从运行详情取得目标实例，不得依赖 `probe.instanceId`。

### 2.4 负载与阈值

```ts
type BotLoadProfile =
  | { type:'stable'; targetBots:number; rampUpSeconds:number; durationSeconds:number }
  | { type:'step'; stages:Array<{targetBots:number; holdSeconds:number}>; stopOnThresholdFailure:boolean }
  | { type:'spike'; targetBots:number; connectWindowSeconds:number; barrier?:{key:string;releaseWindowMs:number}; holdSeconds:number }

interface BotLoadThresholds {
  minOnlineRate: number
  minCommandSentRate: number
  minScheduleCompletionRate: number
  minWorkerHealthRate: number
  minBarrierArrivalRate: number
  maxScheduleLagP95Ms: number
  maxProcessCrashes: number
  safety?: {
    maxExecutorMemoryRate: number
    maxEventLoopP95Ms: number
    sustainSeconds: number
  }
  legacy?: {
    enabled: boolean
    minTps?: number
    maxMsptP95?: number
    requireBusinessObservation?: boolean
  }
}
```

所有 profile/threshold 数字必须是 JSON finite number，标为整数的字段不得含小数，拒绝 NaN/Infinity/字符串数字和未知字段：

- stable：targetBots 1..12800；rampUpSeconds 0..86400；durationSeconds 10..86400。
- step：stages 1..64；每个 targetBots 为 1..12800 且严格递增；holdSeconds 为 10..86400；所有 holdSeconds 总和不超过 604800；stopOnThresholdFailure 必须为 boolean。
- spike：targetBots 1..12800；connectWindowSeconds 1..3600；holdSeconds 10..86400；barrier 可省略，提供时 key 长度 1..64 且匹配 `[A-Za-z0-9._-]+`，releaseWindowMs 为 1..60000。
- 运行 count 必须等于 stable/spike targetBots 或 step 最大 targetBots；profile 预计总时长不得超过 604800 秒，且不得晚于运行总 deadline。
- `minOnlineRate|minCommandSentRate|minScheduleCompletionRate|minWorkerHealthRate|minBarrierArrivalRate` 均为 0..1；`maxScheduleLagP95Ms` 为整数 0..600000；`maxProcessCrashes` 为整数 0..1000。
- safety 若提供必须三字段齐全：maxExecutorMemoryRate 为 (0,1]，maxEventLoopP95Ms 为整数 1..60000，sustainSeconds 为整数 1..3600。
- legacy 若提供必须有 enabled boolean；enabled=false 时其他 legacy 判据必须省略，enabled=true 时至少提供 minTps/maxMsptP95/requireBusinessObservation 之一；minTps 为 0..20，maxMsptP95 为 0..60000，requireBusinessObservation 为 boolean。legacy 只形成附加判定。

`minWorkerHealthRate` 默认 `0.99`。每个 5 秒采样点以运行冻结的 `executorNodeIds` 为分母；节点同时满足 `online=true`、`botWorkerReady=true`、`lastHeartbeatAt` 距采样时刻不超过 15 秒，且 CP Worker 客户端池可通过反向 tunnel 或 direct 任一路径取得可用 RPC client 时计为健康。`tunnelConnected` 仅是诊断字段，直拨可达时不得因其为 false 判不健康。窗口内取最小健康率；无样本先为 `pending`，连续缺样超过 30 秒失败。legacy 阈值只生成独立附加结果，不改变默认 verdict 或 preflight ready。命令模板的 `commandSchedule.commands` 必须非空，因此命令发送率、调度完成率和 schedule lag 在通用命令运行中始终适用；屏障阈值只在 profile 配置 `barrier` 或兼容 Scenario 含 barrier 时适用。未配置的屏障及其他不适用指标返回 `not_applicable`，从 verdict 分母和样本覆盖率中排除，不得按 0 失败或按 100% 成功。

通用 commandSchedule 的 spike barrier 是独立时间屏障，不复用 Scenario `SignalBotActions`：

1. stage 开始时冻结 `stageExpectedBotSet`；配置 barrier 时命令计划必须至少有一个基础 `atMs=0` occurrence，否则 422 `BOT_LOAD_PROFILE_INVALID`。代表 occurrence 固定取全量展开排序后基础时间为 0 的第一项，即最小 `(commandDeclarationIndex,occurrence)`，并把其 commandId/occurrence 冻结进 barrier 事件的 firstCommandId/firstOccurrence。
2. Bot 首次 connected 后，CP 以 `start.mode='barrier'` 调用 `ApplyBotCommandSchedules`；逐项 accepted 表示 prepared，并计入 barrier arrived。分母始终是冻结 expected set，断线/失败不移除。
3. prepare deadline 固定为 `min(stageStartedAt + connectWindowSeconds + 60秒, runDeadline)`。全部 expected Bot prepared 时可提前决策；否则到 deadline 释放已 prepared 子集，未 prepared 项计 timedOut，实际 arrived/expected 参与 `minBarrierArrivalRate` verdict。
4. CP 冻结唯一 `releaseAtUnixMs=决策时刻+2000ms`，并通过 `ReleaseBotCommandSchedules` 下发所有 prepared 计划；若该时间不早于 runDeadline，则 stage 失败且不释放。Bot Worker 仅在 release accepted 后以该时间作为 scheduleStartAt。
5. 每个 Bot 的首个基础 atMs=0 occurrence 在 `releaseAt + releaseWindowMs` 前达到 sent 时计 released，否则计 timedOut；release lag 样本为该 occurrence 的 `sentAt-releaseAt`。命令 failed/timedOut/cancelled 不产生 release-lag 样本，但继续进入命令率和失败分类。

### 2.5 运行、模板

```ts
interface BotLoadTemplate {
  id:number; uuid:string; name:string; description:string
  commandSchedule: BotLoadCommandSchedule
  loadProfile: BotLoadProfile
  thresholds: BotLoadThresholds
  tags:string[]; createdBy:number; createdAt:string; updatedAt:string
}

type BotLoadRunState =
  | 'pending'|'preflighting'|'ready'|'starting'|'running'|'degraded'
  | 'stopping'|'cancelling'|'completed'|'failed'|'cancelled'

type BotLoadVerdict = 'pending'|'passed'|'failed'|'aborted'
type BotLoadVerdictReasonState = 'pass'|'fail'|'pending'|'not_applicable'
type BotLoadVerdictReasonKey =
  | 'online_rate'|'command_sent_rate'|'schedule_completion_rate'|'worker_health_rate'
  | 'barrier_arrival_rate'|'schedule_lag_p95_ms'|'process_crashes'
  | 'sample_coverage_rate'|'consecutive_sample_gap_seconds'
  | 'safety_executor_memory_rate'|'safety_event_loop_p95_ms'
interface BotLoadVerdictReason {
  key:BotLoadVerdictReasonKey; state:BotLoadVerdictReasonState
  expected?:number|string; actual?:number|string; unit?:'ratio'|'ms'|'seconds'|'count'
  message:string; stageIndex?:number
}

interface BotLoadRunCommon {
  id:number;uuid:string;instanceId:number;instanceName?:string;name:string;namePrefix:string
  count:number;behavior:string;config:Record<string,unknown>;orchestrationSummary?:Record<string,unknown>
  status:string;counts:{total:number;byStatus:Record<string,number>}
  allocations:BotLoadAllocation[]
  batches:Array<{id:number;uuid:string;executorNodeId:number;ordinal:number;plannedCount:number;acceptedCount:number;connectedCount:number;failedCount:number;state:string;startedAt?:string;endedAt?:string}>
  startedAt?:string;stoppedAt?:string;endedAt?:string;createdAt:string;updatedAt:string
}
type BotLoadRunLegacy = BotLoadRunCommon & {schemaVersion:1;succeeded:number;failed:number;lastError?:string} & (
  | {scenario:BotLoadScenarioV2;orchestrationYaml?:never;commandSchedule?:never}
  | {commandSchedule:BotLoadCommandSchedule;scenario?:never;orchestrationYaml?:never}
  | {orchestrationYaml:string;scenario?:never;commandSchedule?:never}
  | {scenario?:never;orchestrationYaml?:never;commandSchedule?:never}
)
interface BotLoadRunV2Base extends BotLoadRunCommon {
  schemaVersion:2;templateId?:number
  targetBots:number;runState:BotLoadRunState;verdict:BotLoadVerdict;verdictReasons:BotLoadVerdictReason[];currentStage:number
  loadProfile:BotLoadProfile;thresholds:BotLoadThresholds
  loadCounts:{planned:number;accepted:number;connecting:number;connected:number;disconnected:number;failed:number;stopped:number}
  commandCounts:Record<string,{planned:number;sent:number;failed:number;timedOut:number;cancelled:number}>
  barrier:{waiting:number;arrived:number;released:number;timedOut:number}
  maxStableBots:number;failureSummary:Record<string,number>
}
type BotLoadRunV2 = BotLoadRunV2Base & (
  | {scenario:BotLoadScenarioV2;commandSchedule?:never;orchestrationYaml?:never}
  | {commandSchedule:BotLoadCommandSchedule;scenario?:never;orchestrationYaml?:never}
  | {orchestrationYaml:string;scenario?:never;commandSchedule?:never}
  | {scenario?:never;commandSchedule?:never;orchestrationYaml?:never}
)
type BotLoadRun = BotLoadRunLegacy | BotLoadRunV2

type BotLoadRunSnapshotKind = 'scenario'|'commandSchedule'|'orchestrationYaml'|'legacy'
interface BotLoadRunSummaryCommon {
  id:number;uuid:string;instanceId:number;instanceName?:string;name:string
  count:number;behavior:string;orchestrationSummary?:Record<string,unknown>
  status:string;counts:{total:number;byStatus:Record<string,number>};snapshotKind:BotLoadRunSnapshotKind
  startedAt?:string;stoppedAt?:string;endedAt?:string;createdAt:string;updatedAt:string
}
type BotLoadRunSummary =
  | (BotLoadRunSummaryCommon & {schemaVersion:1;succeeded:number;failed:number;lastError?:string;profileType?:never;runState?:never;verdict?:never})
  | (BotLoadRunSummaryCommon & {schemaVersion:2;templateId?:number;targetBots:number;runState:BotLoadRunState;verdict:BotLoadVerdict;currentStage:number;profileType:BotLoadProfile['type'];loadCounts:{planned:number;accepted:number;connecting:number;connected:number;disconnected:number;failed:number;stopped:number};commandCounts:Record<string,{planned:number;sent:number;failed:number;timedOut:number;cancelled:number}>;barrier:{waiting:number;arrived:number;released:number;timedOut:number};maxStableBots:number;failureSummary:Record<string,number>})

interface Page<T> { items:T[]; total:number; page:number; pageSize:number }

interface BotLoadTemplateInput {
  name:string; description:string
  commandSchedule:BotLoadCommandSchedule
  loadProfile:BotLoadProfile
  thresholds:BotLoadThresholds
  tags:string[]
}

interface BotLoadRunBot {
  id:number; uuid:string; name:string; status:string
  executorNodeId?:number; stepId?:string; commandId?:string
  reconnectCount:number; lastSeenAt?:string; lastError?:string
}

type BotLoadFailureCategory = 'target'|'executor'|'network'|'scenario'|'internal'

interface BotLoadFailure {
  id:string; runUuid:string; botUuid?:string; executorNodeId?:number
  actionRunId?:string; stepId?:string; commandId?:string
  category:BotLoadFailureCategory; legacyCategory?:'probe'
  errorCode:string; message:string; retryable:boolean; occurredAt:string
}

interface BotLoadRetryResult {
  requested:number; accepted:number; skipped:number
  errors:Array<{botUuid?:string;errorCode:string;message:string}>
}

interface BotLoadLatencySummary {
  connectP50Ms:number|null; connectP95Ms:number|null; connectP99Ms:number|null
  scheduleLagP50Ms:number|null; scheduleLagP95Ms:number|null; scheduleLagP99Ms:number|null
  barrierReleaseLagP50Ms:number|null; barrierReleaseLagP95Ms:number|null; barrierReleaseLagP99Ms:number|null
}
type BotLoadBarrierReportKey = `${number}:${string}:${number}` // stageIndex:barrierKey:round
interface BotLoadReport {
  run:BotLoadRunV2
  stages:Array<{stageIndex:number;targetBots:number;state:string;startedAt?:string;endedAt?:string;verdict:BotLoadVerdict;verdictReasons:BotLoadVerdictReason[]}>
  verdictReasons:BotLoadVerdictReason[]
  maxStableBots:number
  latency:BotLoadLatencySummary
  failures:{summary:Record<BotLoadFailureCategory,number>;items:BotLoadFailure[]}
  executors:Array<{nodeId:number;nodeUuid:string;health:string;peakActiveBots:number;peakRssBytes?:number;peakEventLoopP95Ms?:number}>
  commands:Record<string,{planned:number;sent:number;failed:number;timedOut:number;cancelled:number;lagP50Ms:number|null;lagP95Ms:number|null;lagP99Ms:number|null}>
  barriers:Record<BotLoadBarrierReportKey,{stageIndex:number;barrierKey:string;round:number;expected:number;arrived:number;released:number;timedOut:number;releaseLagP50Ms:number|null;releaseLagP95Ms:number|null;releaseLagP99Ms:number|null}>
  legacy?:Record<string,unknown>
  disclaimer:string
}
```

FR-358/共享地基先加性新增 `schema_version`（NOT NULL DEFAULT 1）并启用 schemaVersion=1 联合序列化；现有行及 FR-358 commandSchedule 运行保持 1。FR-359 再新增 V2 列，新创建的 FR-359 运行事务写 2；FR-358 先行创建且带 command_schedule_snapshot 的会话仍为 schemaVersion=1，并由 legacy commandSchedule 分支与兼容 runner 真实表达，不伪造 FR-359 profile/verdict。V2 专属 DB 列为兼容历史行允许 null，但 service 对 schemaVersion=2 强制完整非空；不得把 schemaVersion=1 的旧 `status` 猜成 runState/verdict，也不得伪造 loadProfile/thresholds。部署时已存在的非终态 schemaVersion=1 会话继续由旧兼容 runner 收束，不原地升级；需要 V2 判定/报告时必须复制为新运行。列表/详情按 schemaVersion 判别联合序列化，报告仅为 schemaVersion=2 生成。

时间兼容规则：数据库 `ended_at` 是唯一终止时间列。schemaVersion=1 响应只返回兼容字段 `stoppedAt=ended_at`，`endedAt` 省略；schemaVersion=2 终态响应同时返回 `endedAt` 与兼容别名 `stoppedAt`，两者必须完全相等，非终态两者均省略。V2 的旧 `status` 与 runState 同事务映射固定为：`pending|preflighting|ready|starting → pending`，`running|degraded|stopping|cancelling → running`，`completed|cancelled → stopped`，`failed → error`；禁止其他映射。

所有命令统计必须满足 `planned = sent + failed + timedOut + cancelled`；`COMMAND_DEADLINE_EXCEEDED` 只计 timedOut，不同时计 failed。该守恒规则适用于运行详情/摘要、metric、历史/SSE 聚合、报告、CSV 和前端 KPI。

每个运行及每个 stage 的 `verdictReasons` 必须按上述 key 稳定输出：在线、命令发送、调度完成、Worker 健康、schedule lag、crash、样本覆盖率和连续缺样始终出现；屏障未配置时仍输出 `barrier_arrival_rate + not_applicable`；safety key 仅在配置对应 safety 阈值或触发 safety stop 时出现。`worker_health_rate` 使用 `minWorkerHealthRate`；覆盖率不足先 pending、窗口关闭或连续缺样超 30 秒后 fail。`message` 仅作服务端回退文案，前端 i18n 必须以 `key/state` 为稳定键，不解析 message。

FR-352 的公开 Scenario V2 创建、详情和执行契约保持可用；ADR-075 只修订 `send_command` 成功边界，不废弃场景引擎。FR-358～361 实现后，新命令模板和运行以 `commandSchedule` 为通用主字段，Scenario V2 继续作为显式兼容能力。

## 3. 发压节点与预检（FR-351/359）

### GET `/bots/load-nodes`

- **关联 FR**：FR-351
- **权限**：`bot:read`，并可访问 Query 指定的目标实例
- **请求**：Query `instanceId:number` 必填
- **响应 200**：`{items:BotLoadNodeCapacity[];totalCapacity:number;availableCapacity:number;updatedAt:string}`
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；500 `INTERNAL_ERROR`

### POST `/bots/stress-sessions/:id/preflight`

- **关联 FR**：FR-351；FR-358/359 加性扩展命令计划、profile、thresholds 校验
- **权限**：`bot:manage`，并可管理路径运行的目标实例
- **请求**：`{executorNodeIds?:number[];connectRatePerSecondPerNode?:number}`；节点最多 256 个且去重，省略表示自动选择，速率范围 1..50
- **响应 200**：当前代码为 `BotLoadPreflightCurrent`；FR-358/359 实现后的目标契约为 `BotLoadPreflightPlanned`。容量不足或候选节点不可用仍为 200、`ready=false`、原因在 `blockers[]`
- **副作用**：ready 时保存 allocation plan 并建立 60 秒软预留；不创建 Bot、不启动连接
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；409 `BOT_LOAD_INVALID_STATE`；422 `BOT_LOAD_SCENARIO_INVALID|BOT_LOAD_PROFILE_INVALID|BOT_LOAD_THRESHOLDS_INVALID`；500 `INTERNAL_ERROR`

### POST `/bots/stress-sessions/:id/start`

- **关联 FR**：FR-351/359
- **权限**：`bot:manage`，并可管理路径运行的目标实例
- **请求**：`{planToken:string}`
- **响应 202**：`BotLoadRun`；只表示事务提交且后台派发接受，不等待 accepted/connected
- **幂等**：同一未失效 planToken 重放返回同一运行；创建 Bot/批次和后台任务后立即返回
- **状态限制**：FR-359 目标状态机仅 `ready`；planToken 未过期且 capacity generation 未变化。当前 FR-351 的旧 V1 空 body/pending 内部预检仅按 §7 兼容，不放宽新状态机
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；409 `BOT_LOAD_INVALID_STATE|BOT_LOAD_CAPACITY_CHANGED`；422 `BOT_LOAD_CAPACITY_INSUFFICIENT`；503 `BOT_LOAD_NODE_UNAVAILABLE`；500 `INTERNAL_ERROR`

## 4. 模板与运行（FR-359/360）

V1 模板是个人所有权资源，不绑定目标实例、不做团队共享。`createdBy` 由服务端取当前用户写入且不可由请求覆盖；名称先 trim，大小写敏感。持久层增加 nullable `active_name_key=hex(SHA-256(UTF-8(trimmedName)))`，建立数据库唯一索引 `(created_by, active_name_key)`：活跃行必须非 null，软删事务同时把该列置 null；SQLite/MySQL 均允许多条 null，从而允许软删后复用名称，并由数据库最终阻断并发同名创建/改名。唯一冲突映射 409 `BOT_LOAD_TEMPLATE_NAME_CONFLICT`，service 预检只用于友好提示。非平台管理员的 list/get/update/delete 只作用于自己的模板，无权访问统一返回 404；平台管理员可读取和管理全部模板并写审计。由模板创建运行还必须独立校验调用者可管理请求中的目标实例。团队/组织共享需未来新增显式 visibility/scope 契约，不得通过猜测实例权限实现。

### GET `/bots/load-templates`

- **关联 FR**：FR-359/360
- **权限**：`bot:read`
- **请求**：Query `page:number=1&pageSize:number=20&q?:string&tag?:string&ownerId?:number`，pageSize 1..100；ownerId 仅平台管理员可用；非管理员提交该参数返回 403 `FORBIDDEN`，省略时固定过滤当前用户
- **响应 200**：`Page<BotLoadTemplate>`
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；500 `INTERNAL_ERROR`

### POST `/bots/load-templates`

- **关联 FR**：FR-359/360
- **权限**：`bot:manage`；创建者固定为当前用户
- **请求**：`BotLoadTemplateInput`，不得提交 createdBy/ownerId
- **响应 201**：`BotLoadTemplate`
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；409 `BOT_LOAD_TEMPLATE_NAME_CONFLICT`；422 `BOT_LOAD_SCENARIO_INVALID|BOT_LOAD_PROFILE_INVALID|BOT_LOAD_THRESHOLDS_INVALID`；500 `INTERNAL_ERROR`

### GET `/bots/load-templates/:id`

- **关联 FR**：FR-359/360
- **权限**：`bot:read`；仅创建者本人或平台管理员
- **请求**：无 body
- **响应 200**：`BotLoadTemplate`
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；500 `INTERNAL_ERROR`

### PUT `/bots/load-templates/:id`

- **关联 FR**：FR-359/360
- **权限**：`bot:manage`；仅创建者本人或平台管理员
- **请求**：`BotLoadTemplateInput` 全量替换可编辑字段，createdBy 不可变
- **响应 200**：更新后的 `BotLoadTemplate`
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；409 `BOT_LOAD_TEMPLATE_NAME_CONFLICT`；422 `BOT_LOAD_SCENARIO_INVALID|BOT_LOAD_PROFILE_INVALID|BOT_LOAD_THRESHOLDS_INVALID`；500 `INTERNAL_ERROR`

### DELETE `/bots/load-templates/:id`

- **关联 FR**：FR-359/360
- **权限**：`bot:manage`；仅创建者本人或平台管理员
- **请求**：无 body
- **响应 204**：空 body；软删模板，历史运行快照不变
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；500 `INTERNAL_ERROR`

### POST `/bots/load-templates/:id/runs`

- **关联 FR**：FR-359/360
- **权限**：`bot:manage`；模板须为本人所有或平台管理员可访问，并可管理请求中的目标实例
- **请求**：
  ```ts
  interface CreateBotLoadRunFromTemplateRequest {
    instanceId:number; name:string; namePrefix:string
    config:{server:string;port:number;auth:'offline';version?:string}
    executorNodeIds?:number[]
    commandScheduleOverride?:BotLoadCommandSchedule|null
    loadProfileOverride?:BotLoadProfile|null
    thresholdsOverride?:BotLoadThresholds|null
  }
  ```
- **响应 201**：`BotLoadRun`，冻结模板与 override 合并后的快照；不自动 start
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；422 `BOT_LOAD_SCENARIO_INVALID|BOT_LOAD_PROFILE_INVALID|BOT_LOAD_THRESHOLDS_INVALID`；500 `INTERNAL_ERROR`

V2 连接配置只接受 server、port、auth=offline 和可选 version；拒绝 password/accessToken/refreshToken/clientToken 等凭据。

### POST `/bots/stress-sessions`

- **关联 FR**：FR-352/358/359
- **权限**：`bot:manage`，并可管理请求中的目标实例
- **请求**：
  ```ts
  type CreateBotLoadRunRequest = {
    instanceId:number; count:number; name:string; namePrefix:string
    config:{server:string;port:number;auth:'offline';version?:string}
    executorNodeIds?:number[]; loadProfile?:BotLoadProfile; thresholds?:BotLoadThresholds
  } & (
    | {scenario:BotLoadScenarioV2;commandSchedule?:never;orchestrationYaml?:never}
    | {scenario?:never;commandSchedule:BotLoadCommandSchedule;orchestrationYaml?:never}
    | {scenario?:never;commandSchedule?:never;orchestrationYaml:string}
    | {scenario?:never;commandSchedule?:never;orchestrationYaml?:never;behavior:string}
  )
  ```
- **响应 201**：`BotLoadRun`
- **规则**：`count` 与 profile 最大目标数一致；Scenario V2 继续由 FR-352 执行，commandSchedule 仅在 FR-358 落地后启用
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；422 `BOT_LOAD_SCENARIO_INVALID|BOT_LOAD_PROFILE_INVALID|BOT_LOAD_THRESHOLDS_INVALID`；500 `INTERNAL_ERROR`

### GET `/bots/stress-sessions`

- **关联 FR**：FR-351/352/359/361
- **权限**：`bot:read`
- **请求**：Query `page&pageSize&q&instanceId&runState&verdict&profileType&createdFrom&createdTo`，pageSize 1..100；runState/verdict/profileType 仅匹配 schemaVersion=2，schemaVersion=1 历史行只在未传这些筛选时返回
- **响应 200**：`Page<BotLoadRunSummary>`；列表不返回完整 scenario/commandSchedule/orchestrationYaml、thresholds、allocations 或 batches。旧摘要字段 `count/behavior/orchestrationSummary/status/counts` 保留，完整冻结快照仅由详情端点返回
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；500 `INTERNAL_ERROR`

### GET `/bots/stress-sessions/:id`

- **关联 FR**：FR-351/352/359/361
- **权限**：`bot:read`，并可访问运行的目标实例
- **请求**：无 body
- **响应 200**：`BotLoadRun` 完整冻结快照
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；500 `INTERNAL_ERROR`

### POST `/bots/stress-sessions/:id/stop`

- **关联 FR**：FR-351/359
- **权限**：`bot:manage`，并可管理运行的目标实例
- **请求**：`{reason?:string}`，reason 最长 255
- **响应 202**：`BotLoadRun`；只表示有序停止 intent 已接受，最终目标为 completed
- **状态限制**：`starting|running|degraded` 接受新 stop intent；`stopping` 幂等返回当前运行；其他状态 409
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；409 `BOT_LOAD_INVALID_STATE`；500 `INTERNAL_ERROR`

### POST `/bots/stress-sessions/:id/cancel`

- **关联 FR**：FR-354/359
- **权限**：`bot:manage`，并可管理运行的目标实例
- **请求**：`{reason?:string}`，reason 最长 255
- **响应 202**：`BotLoadRun`；只表示尽快取消 intent 已接受，最终为 cancelled + aborted
- **状态限制**：任一非终态；`pending|preflighting|ready` 无命令计划可取消时直接收束，`stopping` 可升级为 cancelling，`cancelling` 幂等返回当前运行；终态 409
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；409 `BOT_LOAD_INVALID_STATE`；500 `INTERNAL_ERROR`

### POST `/bots/stress-sessions/:id/retry-failed`

- **关联 FR**：FR-354/359/361
- **权限**：`bot:manage`，并可管理运行的目标实例
- **请求**：`{requestId:string;botUuids?:string[];errorCodes?:string[];fromStepId?:string}`；botUuids/errorCodes 均省略表示全部失败 Bot，requestId 为 UUID 幂等键
- **响应 202**：`BotLoadRetryResult`
- **状态限制**：`running|degraded`；fromStepId 必须属于冻结运行快照
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；409 `BOT_LOAD_INVALID_STATE`；422 `BOT_LOAD_SCENARIO_INVALID`；500 `INTERNAL_ERROR`

## 5. 指标、失败、事件和报告（FR-359/361）

### GET `/bots/stress-sessions/:id/metrics`

- **关联 FR**：FR-359/361
- **权限**：`bot:read`，并可访问运行的目标实例
- **请求**：Query `from?:string&to?:string&resolution:raw|15s|1m|5m`；默认运行全程与服务端自选分辨率
- **响应 200**：`{items:BotLoadMetricPoint[];from:string;to:string;resolution:'raw'|'15s'|'1m'|'5m'}`，单响应默认不超过 1200 点
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；500 `INTERNAL_ERROR`

默认指标：连接率、命令发送率、调度完成率、schedule lag、屏障、Worker 健康、crash。`targetLegacy` 可包含 nullable `tps/msptP95/onlinePlayers`，缺失不填 0。

```ts
interface BotLoadMetricPoint {
  timestamp:string; stageIndex:number
  counts:Record<string,number>
  command:Record<string,number>
  barrier:Record<string,number>
  executor:Array<{nodeId:number;activeBots:number;rssBytes?:number;eventLoopP95Ms?:number;cpuPercent?:number;health:string}>
  latency:{connectP50Ms:number|null;connectP95Ms:number|null;connectP99Ms:number|null;scheduleLagP50Ms:number|null;scheduleLagP95Ms:number|null;scheduleLagP99Ms:number|null;barrierReleaseLagP50Ms:number|null;barrierReleaseLagP95Ms:number|null;barrierReleaseLagP99Ms:number|null}
  targetLegacy?:{tps?:number;msptP95?:number;onlinePlayers?:number}
  errors:Record<string,number>
}
```

三类 latency 均以整数毫秒统计，并在每个 stage/查询聚合窗口内对有效样本使用 nearest-rank：排序后 `rank=ceil(p*n)`、取 1-based 第 rank 项；`n=0` 时对应 p50/p95/p99 字段固定为 JSON null，CSV 留空，不允许省略。原始终点早于起点时将样本钳制为 0 并记录 `CLOCK_SKEW` warning。

- connect latency：起点为该 Bot 冻结的 `connectNotBeforeUnixMs`，终点为首次 Fleet runtime snapshot 进入 connected 的 `observedAtUnixMs`；仅成功 connected 的 Bot 纳入，连接失败、未连接和 cancelled 不进入 percentile，由连接率/失败分类体现。
- schedule lag：起点为 occurrence 的绝对 `plannedAtUnixMs`，终点为 `sentAtUnixMs`；仅最终状态 sent 的 occurrence 纳入，failed/timedOut/cancelled 不进入 percentile，其最后尝试/取消时刻仍保留在事件和 checkpoint。不得另算未定义的“发送延迟”。
- barrier release lag：仅在配置屏障时适用。通用 commandSchedule 以共同 `releaseAtUnixMs` 为起点、首个基础 atMs=0 occurrence 的 sentAt 为终点；兼容 Scenario V2 以 releaseAt 为起点、屏障后首个动作 running 的 observedAt 为终点。未到达、超时、failed/cancelled 或无后续动作不进入 percentile，由屏障到达率/失败计数体现；未配置屏障时全部 release-lag 字段为 null 且 reason 为 not_applicable。

### GET `/bots/stress-sessions/:id/bots`

- **关联 FR**：FR-359/361
- **权限**：`bot:read`，并可访问运行的目标实例
- **请求**：Query `page&pageSize&q&status&executorNodeId&stepId&errorCode`，pageSize 1..100
- **响应 200**：`Page<BotLoadRunBot>`
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；500 `INTERNAL_ERROR`

### GET `/bots/stress-sessions/:id/failures`

- **关联 FR**：FR-359/361
- **权限**：`bot:read`，并可访问运行的目标实例
- **请求**：Query `page&pageSize&category&errorCode&botUuid&executorNodeId&stepId&from&to`，pageSize 1..100
- **响应 200**：`Page<BotLoadFailure>`
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；500 `INTERNAL_ERROR`

失败分类固定为 target、executor、network、scenario、internal；Worker/bot-worker 不可用归入 executor，命令、调度、屏障和兼容场景步骤归入 scenario。失败链显示 Bot→Worker→命令或兼容步骤→调度/发送结果→错误。历史 `probe` category 读取时归一为 `scenario`，只在 `legacyCategory:"probe"` 元数据保留原值，绝不返回第六枚举。当前契约不承诺 ProbeEvent 数据源；未来独立适配器实际提供数据后，才可作为 optional legacy 扩展追加。

### GET `/bots/stress-sessions/:id/events`

- **关联 FR**：FR-359/361
- **权限**：`bot:read`，并可访问运行的目标实例
- **请求**：Query `page&pageSize&type&eventId&actionRunId&botUuid&executorNodeId&stepId&from&to&snapshotEventId`，pageSize 1..100。第一页省略 snapshotEventId；后续页必须回传第一页响应值
- **响应 200**：`BotLoadRunEventPage`
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；500 `INTERNAL_ERROR`

```ts
interface BotLoadRunEventBase {
  eventId: string
  runId: number
  runUuid: string
  timestamp: string
  legacy?: { category?: string; data?: Record<string, unknown> }
}

interface BotLoadBarrierEventCounts {
  barrierKey:string;round:number;expected:number;arrived:number;released:number;timedOut:number
}
type BotLoadBarrierEventSource =
  | {source:'command';prepareDeadlineAt:string;releaseWindowMs:number;firstCommandId:string;firstOccurrence:number;scenarioDeadlineAt?:never}
  | {source:'scenario';scenarioDeadlineAt:string;prepareDeadlineAt?:never;releaseWindowMs?:never;firstCommandId?:never;firstOccurrence?:never}
type BotLoadBarrierEventState =
  | {state:'preparing';decision?:never;decidedAt?:never;releaseAt?:never;reason?:never;affectedBotUuids?:never;chunkIndex?:never;chunkCount?:never}
  | {state:'decision';decision:'release';decidedAt:string;releaseAt:string;reason?:never;affectedBotUuids?:never;chunkIndex?:never;chunkCount?:never}
  | {state:'decision';decision:'fail';decidedAt:string;releaseAt?:never;reason:string;affectedBotUuids?:never;chunkIndex?:never;chunkCount?:never}
  | {state:'release_dispatched'|'release_accepted';decision?:never;decidedAt:string;releaseAt:string;reason?:never;affectedBotUuids?:never;chunkIndex?:never;chunkCount?:never}
  | {state:'released';decision?:never;decidedAt:string;releaseAt:string;reason?:never;affectedBotUuids?:never;chunkIndex?:never;chunkCount?:never}
  | {state:'timed_out'|'cancelled';decision?:never;decidedAt:string;releaseAt?:string;reason:string;affectedBotUuids:string[];chunkIndex:number;chunkCount:number}
type BotLoadBarrierEventPayload = BotLoadBarrierEventCounts & BotLoadBarrierEventSource & BotLoadBarrierEventState
interface BotLoadScenarioActionEventBase {
  cohortKey:string;actionType:BotLoadScenarioAction['type'];attempt:number;durationMs:number;correlationToken:string
}
type BotLoadScenarioActionEventPayload = BotLoadScenarioActionEventBase & (
  | {status:'succeeded';errorCode?:never;message?:never;result?:Record<string,unknown>}
  | {status:'failed'|'timed_out'|'cancelled';errorCode:string;message:string;result?:Record<string,unknown>}
)
type BotLoadRunEvent = BotLoadRunEventBase & (
  | { type:'run-state'; payload:{runState:string;previousRunState?:string;verdict?:string;reason?:string} }
  | { type:'stage'; payload:{stageIndex:number;targetBots:number;state:string} }
  | { type:'barrier'; stageIndex:number;stepId:string; payload:BotLoadBarrierEventPayload }
  | { type:'scenario-action'; actionRunId:string;botUuid:string;stepId:string;payload:BotLoadScenarioActionEventPayload }
  | { type:'command-schedule'; stepId:string; payload:{scheduleRunId:string;state:string;planned:number;sent:number;failed:number;timedOut:number;cancelled:number} }
  | { type:'command-send'; stepId:string; payload:{mode:'aggregate';windowStart:string;windowEnd:string;planned:number;sent:number;failed:number;timedOut:number;cancelled:number;lagP95Ms:number|null} }
  | { type:'command-send'; actionRunId:string;botUuid:string;stepId:string;commandId:string;payload:{mode:'item';occurrence:number;attempt:number;status:'failed'|'timed_out'|'cancelled';lagMs:number|null;errorCode:string;message?:string} }
  | { type:'worker-health'; executorNodeId:number; payload:{health:string;activeBots:number;rssBytes?:number;eventLoopP95Ms?:number;cpuPercent?:number} }
  | { type:'executor-crash'; executorNodeId:number; payload:{workerEpochGeneration:number;reason:string} }
  | { type:'safety-stop'; payload:{reason:string;metric?:string;threshold?:number;actual?:number} }
  | { type:'report-ready'; payload:{reportReady:true;formats:Array<'json'|'csv'>} }
)
type BotLoadRunEventType = BotLoadRunEvent['type']

interface BotLoadRunEventPage {
  items: BotLoadRunEvent[]
  total: number
  page: number
  pageSize: number
  snapshotEventId: string
}
```

`eventId` 是 `bot_load_run_events.id` 的十进制字符串，客户端视为不透明值；服务端固定按数值 `eventId DESC` 排序，同一响应窗口只返回 `eventId <= snapshotEventId`。第一页冻结当时最大 eventId，空窗口返回 `snapshotEventId:"0"`、`items:[]`、`total:0`；后续分页在该快照内稳定，`snapshotEventId:"0"` 没有后续页，刷新第一页才看到更新事件。

- 通用命令成功发送按 stepId 与 1 秒窗口持久化 `command-send/mode:'aggregate'`；每个 failed/timed_out/cancelled occurrence 另持久化 `mode:'item'` 并携 actionRunId/botUuid/commandId。
- Scenario V2 每个 actionRunId 的首个终态持久化一条 `scenario-action`；running/waiting 不进入历史高频流。result 沿 ActionResultService 的 16KiB 截断规则，失败/超时/取消必须携 errorCode。FailureTrace 可按 actionRunId 在刷新后恢复移动、攻击、等待外部事件等兼容场景步骤。
- barrier 事件强制携带 stageIndex，报告 map key 固定为 `stageIndex:barrierKey:round`；barrier 在 expected set 冻结时写 preparing，在全员到达或 prepare deadline 写 decision；发起 Release RPC 写 release_dispatched，accepted 回执收敛写 release_accepted，这两者都不增加 released。只有代表 occurrence 实际 sent 且未超过 release window 时才累计 released，并在窗口关闭时写 released 终局事件；未 prepared、代表 occurrence 超窗/failed/timedOut/cancelled、人工取消分别写 timed_out/cancelled。timed_out/cancelled 的 `affectedBotUuids` 每事件最多 100 个，超过时按稳定 botUuid ASC 分块并填写 0-based chunkIndex/chunkCount；其他状态不得携该数组。command barrier 固定 `source:'command'` 并记录 prepareDeadlineAt/releaseWindowMs/firstCommandId/firstOccurrence；Scenario barrier 固定 `source:'scenario'` 并记录 scenarioDeadlineAt。decision 必须在 release（releaseAt 必填）与 fail（reason 必填）之间二选一；两类来源都按状态联合记录 decidedAt/releaseAt 或失败原因。由这些 append-only 事件恢复 arrived/released/timedOut、决策时间和受影响 Bot，不依赖内存 SSE。
- `bot_load_action_results` 仍是动作终态真源，事件投影与 action result 在同事务或可靠 outbox 中生成，重复投影按 actionRunId 幂等。legacy 数据置于顶层 `legacy` 元数据，不扩展失败分类。

### GET `/bots/stress-sessions/:id/report`

- **关联 FR**：FR-359/361
- **权限**：`bot:read`，并可访问运行的目标实例；写审计 `bot_load.report.export`
- **请求**：Query `format:'json'|'csv'` 必填
- **响应 200**：format=json 时 `application/json` 的 `BotLoadReport` 且 `Content-Disposition: attachment; filename="bot-load-<runUuid>.json"`；format=csv 时 `text/csv; charset=utf-8` 且带 UTF-8 BOM，`Content-Disposition: attachment; filename="bot-load-<runUuid>.csv"`
- **状态限制**：仅终态 `completed|failed|cancelled`
- **错误**：400 `INVALID_REQUEST`；401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；409 `BOT_LOAD_REPORT_NOT_READY`；500 `INTERNAL_ERROR`

报告包含 run、stage、maxStableBots、latency、failure、executor、command、barrier 和 optional legacy 摘要。必须包含免责声明：默认 verdict 只证明连接、命令发送、调度、已配置屏障和 Worker 健康达到阈值；`bot.chat` 成功不证明服务器接受、权限通过或业务效果；TPS/MSPT 和业务事件仅是附加观测。

CSV 使用固定 header：

```text
section,run_uuid,stage_index,key,id,node_id,node_uuid,bot_uuid,command_id,status,verdict,reason_state,expected,actual,unit,count,planned,sent,failed,timed_out,cancelled,p50_ms,p95_ms,p99_ms,started_at,ended_at,occurred_at,error_code,message,value_json
```

| section | 行来源与专用列映射 | value_json | section 内排序 |
|---|---|---|---|
| summary | 恰一行；run_uuid=run.uuid，key=run，status=run.runState，verdict=run.verdict，actual=maxStableBots，unit=count | `{run,maxStableBots}` | 固定首行 |
| stage | 每 stage 一行；stage_index=stageIndex，status=state，verdict/started_at/ended_at 对应同名字段，key=`stage` | 完整 stage 对象 | stageIndex ASC |
| verdict_reason | run reasons 后接各 stage reasons；stage_index 可空，key/reason_state/expected/actual/unit/message 逐字段映射 | 空 | run 级先行；再 stageIndex ASC；同级按 key ASC |
| failure_summary | 五类各一行；key=category，count=summary[category] | 空 | target、executor、network、scenario、internal 固定顺序 |
| failure | 每 failure 一行；id/node_id/bot_uuid/command_id/error_code/message/occurred_at 映射，status=category | 完整 failure 对象 | occurredAt ASC、id ASC |
| executor | 每 executor 一行；node_id/node_uuid/status=health，actual=peakActiveBots，unit=count | 完整 executor 对象 | nodeId ASC、nodeUuid ASC |
| command | 每 commandId 一行；key=command_id=map key，planned/sent/failed/timedOut/cancelled 与 lag p50/p95/p99 映射 | 完整 command value | commandId ASC |
| barrier | 每 barrier key 一行；key=map key，expected→planned、arrived→actual、released→sent、timedOut→timed_out，release lag p50/p95/p99 映射 | 完整 barrier value | barrier key ASC |
| latency | connect、schedule_lag、barrier_release_lag 各一行；key 与 p50/p95/p99 映射 | 空 | 前述固定顺序 |
| legacy | 每个顶层 legacy key 一行；key=map key | 对应完整值 | key ASC |
| disclaimer | 恰一行；key=disclaimer，message=BotLoadReport.disclaimer | 空 | 固定末行 |

- 不存在/null 使用空字段，不写字面量 `null`；时间统一 RFC3339；布尔为 `true|false`；数字使用十进制点且不带本地化分组。`value_json` 使用 UTF-8 紧凑 canonical JSON：对象键按 Unicode code point 递归 ASC，数组保持权威报告的冻结顺序；生成报告时 allocations 按 ordinal、batches 按 ordinal/id、stages 按 stageIndex、failures 按 occurredAt/id、executors 按 nodeId/nodeUuid 先行稳定化。
- 按 RFC 4180 转义逗号、双引号与换行，双引号写成两个双引号；记录分隔符固定 CRLF。文件最前为 UTF-8 BOM，header 仅出现一次；section 总顺序严格按上表。相同冻结报告必须生成字节完全一致的 CSV。

## 6. 会话级 SSE（FR-361）

### GET `/bots/stress-sessions/:id/stream`

- **关联 FR**：FR-359/361
- **权限**：`bot:read`，并可访问运行的目标实例
- **请求**：Header `Last-Event-ID?:string`；无 body
- **响应 200**：`text/event-stream`，支持 init 快照补偿、慢消费者断开和终态关闭
- **错误**：401 `UNAUTHORIZED`；403 `FORBIDDEN`；404 `NOT_FOUND`；429 `RATE_LIMITED`；503 `BOT_LOAD_STREAM_UNAVAILABLE`；500 `INTERNAL_ERROR`

事件：

| event | data |
|---|---|
| init | `{run,lastEventId}` |
| run-state | `{runState,verdict,verdictReasons,currentStage,timestamp}` |
| counts | `{counts,commandCounts,barrier,timestamp}` |
| stage | `{stageIndex,targetBots,state,timestamp}` |
| metric | 聚合 `BotLoadMetricPoint` |
| command | `{commandId?,stepId,planned,sent,failed,timedOut,cancelled,lagP95Ms,timestamp}` |
| failure | `{errorCode,category,delta,timestamp}` |
| warning | `{code,message,timestamp}` |
| history | 完整 `BotLoadRunEvent`，用于 Events 首屏增量与 trace 去重 |
| complete | `{verdict,verdictReasons,reportReady,disclaimer,timestamp}` |

SSE 是可丢增量；持久化 run、metric 和 event 才是真源。只有 `history` 事件可按 `eventId` 插入 Events 列表，并与历史 API 使用同一 `BotLoadRunEvent` 联合；`init/counts/metric/failure/warning` 只是聚合投影，不得伪造成历史事件。`run-state/stage/command/complete` 更新页面摘要；对应持久化关键事件由独立 `history` 同步投影，其中命令计划/发送分别使用 `command-schedule`/`command-send`，报告完成使用 `report-ready`。barrier、worker-health、executor-crash、safety-stop 也通过 `history` 传递。单运行每秒最多 5 条聚合事件；`history` 可按 100ms 窗口合并后发送，单用户同 run 最多 5 条连接。

## 7. 兼容性与审计

- 原 `/bots/stress-test` 别名继续可用。
- 旧 V1 单节点且 `count<=50` 可在空 body start 时内部预检；V2、500+ 或包含 commandSchedule/loadProfile/thresholds/executor pool 时缺 planToken 返回 409 并要求预检。
- 新字段全部加性；公开 Scenario V2 继续按 FR-352 创建、读取和执行。当前契约不提供 ProbeEvent 持久化数据源；房间、区域或其他业务字段仅在未来独立适配器实际实现后作为 optional legacy 扩展返回。
- 审计 `bot_load.template.create/update/delete`、`bot_load.run.create/preflight/start/stop/cancel/retry_failed`、`bot_load.report.export`。

## 8. 不变的平台边界

本文仅定义 Bot 压测 API。实例级 ServerProbe `/metrics`、在线玩家、插件桥和通用监控 API 继续按正式 `docs/API.md` 与 `docs/ARCHITECTURE.md` 执行；它们不因此文档重定向而删除或改变。
