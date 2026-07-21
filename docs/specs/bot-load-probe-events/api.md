# API：通用Bot命令编排与调度扩展

> 目录名 `bot-load-probe-events` 为历史沿用；本 API 属于 FR-369“通用 Bot 命令编排与调度扩展”，并取代未实施的旧 FR-364 API 方向。

## 1. command_schedule

本 FR 提供命令计划数据结构，不新增业务事件接口，也不依赖 ServerProbe、塔防或 `ProbeEvent` 表。

```json
{
  "commands": [
    {
      "id": "announce-ready",
      "atMs": 0,
      "command": "/say ready",
      "repeat": { "intervalMs": 1000, "count": 3 }
    }
  ],
  "durationMs": 5000,
  "jitterMs": 20
}
```

- `commands`：按相对时间排列的命令列表，必须包含 1..100 个命令。
- `id`：计划内唯一稳定字符串，作为 checkpoint、重试和结果聚合的 `commandId`；不得使用数组下标代替。
- `atMs`：相对计划起点的毫秒数。
- `repeat.intervalMs` / `repeat.count`：重复间隔和包含首次执行在内的总 occurrence 数；任一基础时间超出 duration 时整份计划拒绝，不做运行时截断。
- `durationMs`：计划有效持续时间。
- `jitterMs`：允许的执行时间随机偏移，不得破坏顺序或屏障窗口。

实现必须统一校验：commands 1..100；id 1..64 且匹配 `[A-Za-z0-9._-]+`；command 模板原文及变量展开后的最终文本均为 UTF-8 1..1024 bytes，并禁止 U+0000..U+001F/U+007F；durationMs 1..86400000；jitterMs 省略时在快照/hash/展开前规范化为 0，提供时为 0..min(60000,durationMs)，下发 occurrence plan 必须显式携带该规范值；atMs 0..durationMs；intervalMs 1..86400000；repeat.count 1..1000；展开后 occurrence 总数 1..1000；规范 JSON ≤256KiB。越界或未知字段一律提交时拒绝。

## 2. 调度与执行语义

由集中 scheduler 管理每个 `scheduleRunId` 的计划、取消、重试和结果聚合；每个 `commandId + occurrence` 是独立执行项，并拥有独立确定性 `actionRunId`。调用 `send_command` 时执行 `bot.chat(command)`：未同步抛错即记录 `sent`，最终同步抛错则记录 `failed`。不等待 Minecraft 服务端回执，不从聊天、日志或业务事件推断结果。

V1 重试策略不可配置：单执行项最多 3 次尝试；仅 `bot.chat` 同步抛错可重试，调用前路由、模板展开/参数错误、Bot 不存在、取消和 deadline/duration 到期均不可重试；第 1、2 次失败后分别退避 250ms、500ms。若下一次尝试的开始时间不早于计划 `durationMs` 截止或运行总 deadline，则不再重试，以 `COMMAND_DEADLINE_EXCEEDED` 收敛为 timedOut/timed_out；先前发送异常只保留在 attemptErrors，不计 failed。schedule lag 从原始计划时间计算到最终成功发送时间；失败项记录到最后一次尝试时间。

取消操作幂等。取消后未发送命令不得执行；已产生的结果保留并将未完成项聚合为 `cancelled`。计划结束、取消、失败或 scheduler 关闭时必须释放定时器和计划状态，禁止 timer 泄漏。

每个执行项只产生一个最终结果事件；中间失败尝试仅进入该终态的 `attemptErrors`，不得作为多个 Fleet 终态发送。聚合结果按命令声明顺序和重复序号稳定返回，至少包含：总执行项数、`sent`、`failed`、`timedOut`、`cancelled`、重试次数、开始时间、结束时间及每项错误摘要。

## 3. 计划中的 Control Plane ↔ Worker gRPC

FR-369 必须在现有 WorkerService 上加性新增独立批量 RPC，不复用 `ApplyBotBatch` 或 `SignalBotActions`：

```ts
type CommandOccurrenceKey = {commandId:string;occurrence:number}
type CommandOccurrenceRef = CommandOccurrenceKey & {actionRunId:string;plannedAtUnixMs:number|null}
interface AppliedCommandOccurrence {
  commandId:string;occurrence:number;commandDeclarationIndex:number
  baseAtMs:number;jitterOffsetMs:number;actionRunId:string;command:string
}
interface AppliedCommandOccurrencePlan {
  durationMs:number;jitterMs:number;occurrences:AppliedCommandOccurrence[]
}
type CommandDispatchDisposition = 'accepted'|'rejected'|'unknown'

type CommandScheduleStart =
  | {mode:'absolute';scheduleStartAtUnixMs:number;barrierKey?:never}
  | {mode:'barrier';barrierKey:string;scheduleStartAtUnixMs?:never}
interface ApplyBotCommandScheduleItem {
  runId:number; runUuid:string; botUuid:string; generation:number; stepId:string
  scheduleRunId:string; correlationToken:string
  start:CommandScheduleStart; runDeadlineUnixMs:number; jitterSeed:string
  plan:AppliedCommandOccurrencePlan
  skipOccurrences:CommandOccurrenceKey[]
}
interface ApplyBotCommandSchedulesRequest {requestId:string;items:ApplyBotCommandScheduleItem[]}
interface ApplyBotCommandSchedulesResponse {
  requestId:string
  results:Array<{botUuid:string;scheduleRunId:string;disposition:CommandDispatchDisposition;errorCode?:string;message?:string}>
}

interface ReleaseBotCommandScheduleItem {
  runUuid:string; botUuid:string; generation:number; stepId:string; scheduleRunId:string
  barrierKey:string; releaseAtUnixMs:number
}
interface ReleaseBotCommandSchedulesRequest {requestId:string;items:ReleaseBotCommandScheduleItem[]}
interface ReleaseBotCommandSchedulesResponse {
  requestId:string
  results:Array<{botUuid:string;scheduleRunId:string;disposition:CommandDispatchDisposition;alreadyReleased?:boolean;errorCode?:string;message?:string}>
}

interface CancelBotCommandScheduleItem {
  runUuid:string; botUuid:string; generation:number; stepId:string; scheduleRunId:string
  correlationToken:string; reason:string; unresolvedOccurrences:CommandOccurrenceRef[]
}
interface CancelBotCommandSchedulesRequest {requestId:string;items:CancelBotCommandScheduleItem[]}
interface CancelBotCommandSchedulesResponse {
  requestId:string
  results:Array<{botUuid:string;scheduleRunId:string;disposition:CommandDispatchDisposition;alreadyCancelled?:boolean;errorCode?:string;message?:string}>
}
```

- 三个 RPC 每批 `items` 均为 1..100，按 `botUuid + generation + scheduleRunId + operation` 幂等并逐项返回，不因单项失败回滚其他项；RPC transport 失败保持整批 unknown，由 CP 以同一 requestId/计划身份重试。
- 批次 `requestId` 必须是 UUID。Worker 为每个 item 派生唯一稳定 IPC requestId：UUIDv5(namespace=`requestId`, name=UTF-8 `operation + NUL + botUuid + NUL + generation十进制 + NUL + scheduleRunId`)，operation 固定为 `apply|release|cancel`。同一幂等项并发/重放时 Worker 必须合并到同一 in-flight/result，不得向 Manager pending map 注册重复 ID；异步 occurrence 终态不携该 requestId。
- `ApplyBotCommandSchedules` 由 CP 在 Bot assignment accepted 且到达计划派发点后调用，载荷是已完成变量展开、actionRunId/jitter 冻结和文本复验的 occurrence plan，不是模板 schedule。Worker 校验本节点 Bot/generation，转发 Worker↔Bot Worker IPC；收到 `accepted=true` 返回 accepted，收到显式拒绝返回 rejected，RPC deadline 前未取得确定 accepted 或 child 状态不明返回 unknown。unknown 不合成 occurrence 失败；CP 通过 Fleet snapshot/stream、checkpoint 和同幂等键重放收敛。
- `start.mode='absolute'` 直接携带计划起点；`start.mode='barrier'` 只准备计划并等待独立 release，不启动 timer。`skipOccurrences` 由 CP 根据稳定 checkpoint 键生成，正常首次派发为空；恢复时必须列出全部已 `sent` 的 commandId+occurrence。新 scheduleRunId 会产生新 actionRunId，因此跳过集不得携带旧 actionRunId。Worker 原样传给 Bot Worker，列中项不得再次调用 `bot.chat`，也不得重复产生终态。
- `ReleaseBotCommandSchedules` 只接受已 prepared 且 barrierKey 匹配的计划；releaseAt 是共同绝对起点，必须晚于当前时刻且早于 run deadline。重复 release 使用相同时间返回 accepted/alreadyReleased=true；同一计划用不同时间重放返回 rejected/COMMAND_SCHEDULE_REJECTED；未知态不伪造已释放。Worker/child 在 prepare 后重启导致状态丢失时，CP 先以相同 scheduleRunId/start.mode=barrier 重放 Apply，accepted 后再重放同一仍在未来的 releaseAt；releaseAt 已过则取消该计划并判 stage 失败，不把迟到释放改成当前时刻。
- `CancelBotCommandSchedules` 的请求本身携带 CP 已持久化的 cancel intent、计划级 correlationToken 和未终态 occurrence 的 actionRunId/plannedAt；prepared 但尚未 release 的项 plannedAt 固定为 null，release 后必须为精确整数毫秒；Worker 不访问 CP 数据库。若活动 scheduler 存在则转发 cancel IPC；若 child 已重启、计划状态/tombstone 已丢失，则 Worker 以 `unresolvedOccurrences` 逐项合成唯一 `cancelled/ACTION_CANCELLED` Fleet action_event：`correlation_token` 使用请求值，`attempt=1`、`duration_ms=0`、`observed_at_unix_ms=合成时刻`，`result_json` 保留 scheduleRunId/commandId/occurrence/plannedAtUnixMs（可为 null）/status=cancelled，并返回 accepted、alreadyCancelled=true；空 unresolved 集直接幂等成功。只有既无活动计划、无 tombstone，且 CP 未提供可归真的 occurrence 时才 rejected；等待 child 回执超时且无法确定状态时返回 unknown。
- 最终 occurrence 状态不放入同步 gRPC response，统一经既有 `StreamBotFleetEvents.action_event` 回传并由 ActionResultService/checkpoint 首终态幂等。`SignalBotActions` 只属于 FR-363 Scenario 屏障/外部信号。

### disposition 有界收敛

每个 item 独立收敛；初次调用后最多再重试 2 次，退避 250ms、500ms，始终复用批次/派生 IPC requestId，且不得越过 operation deadline：absolute Apply 为 `min(scheduleStartAt,runDeadline)`，barrier Apply 为 `min(prepareDeadlineAt,runDeadline)`，Release 为 `min(releaseAt,runDeadline)`，Cancel 为 `min(cancelIntentAt+15s,runDeadline)`。每次重试前先消费 Fleet/checkpoint/Worker 幂等结果；已见终态或 accepted 不再重发。

| 操作 | accepted | rejected | 到 operation deadline 仍 unknown |
|---|---|---|---|
| Apply | 进入 scheduled/prepared；barrier 项计 arrived | 返回 `COMMAND_DEADLINE_EXCEEDED` 时所有非 skip occurrence 合成 timed_out，其余具体错误码合成 failed；barrier 项计 timedOut | 最后一次 snapshot/reconcile 仍无活动计划/终态时，以 `COMMAND_IPC_FAILED` 合成 failed；不得在 deadline 前合成 |
| Release | 以冻结 releaseAt 启动；同值重放幂等 | 立即 Cancel prepared 计划，未终态 occurrence 以 `COMMAND_SCHEDULE_REJECTED` failed，写 barrier decision=fail/timed_out | releaseAt 到达仍不确定时不得改成当前时间；立即 Cancel，未终态 occurrence 以 `COMMAND_DEADLINE_EXCEEDED` 收敛到命令桶 timedOut / ActionResult timed_out，写 barrier timed_out |
| Cancel | 等待/接收 cancelled 终态 | CP 使用持久 checkpoint 上下文直接为所有未终态 occurrence 写 `ACTION_CANCELLED`；同时重试停止 Bot | cancel deadline 到达后执行与 rejected 相同的 CP 合成，保证 run 可进入 cancelled |

所有合成仍服从 actionRunId 首终态胜出：若真实 sent/failed 先落账，后续合成跳过；若合成先落账，迟到 Node 结果只记诊断。部分批次拒绝/未知不阻塞其他 item。Apply/Release 最终失败使当前 stage 至少进入 degraded 并令默认 verdict failed；step 且 stopOnThresholdFailure=true 时停止升压，stable/spike 在保存证据后有序停止。Cancel 收敛完或 15 秒上限到达后运行进入 cancelled/aborted，不得因 unresolved 永久悬挂。

## 4. 计划中的 Worker ↔ Bot Worker IPC

FR-369 新增的批量命令 IPC 与现有单 Bot `send-command` 分离；旧消息继续只确认 Go→Node stdin 写入，不追溯 `bot.chat` 结果。

### Go → Node：`command-schedule`

```json
{
  "cmd": "command-schedule",
  "requestId": "uuid",
  "runId": 123,
  "runUuid": "uuid",
  "botUuid": "uuid",
  "generation": 7,
  "stepId": "command-schedule",
  "scheduleRunId": "globally-unique-uuid",
  "correlationToken": "uuid",
  "start": {"mode":"absolute","scheduleStartAtUnixMs":1784563200000},
  "runDeadlineUnixMs": 1784566800000,
  "jitterSeed": "20260720",
  "skipOccurrences": [],
  "plan": {
    "durationMs": 5000,
    "jitterMs": 20,
    "occurrences": [{
      "commandId":"announce-ready","occurrence":0,"commandDeclarationIndex":0,
      "baseAtMs":0,"jitterOffsetMs":-13,"actionRunId":"uuid","command":"/say ready"
    }]
  }
}
```

- `requestId` 只关联本次 IPC 请求和同步 accepted 回执；异步终态不依赖 requestId。计划幂等身份为 `runUuid + botUuid + generation + scheduleRunId`。`correlationToken` 是计划级追踪值，同一 schedule 的所有 occurrence 原样复用；逐 occurrence 唯一关联由 `actionRunId` 提供。
- CP 在 Apply 前使用运行快照中的 botName/botOrdinal/cohortKey/runId 及确定性 actionRunId/correlationToken 展开全部 occurrence，排序后下发 `plan.occurrences`；Worker/Bot Worker 不再执行模板变量替换。plan 必须携原冻结 jitterMs；接收端用 jitterSeed+jitterMs 复算每项 jitterOffset，并复验 declarationIndex/baseAt/actionRunId 与最终 command，数组顺序必须等于权威排序。`skipOccurrences` 来自 CP checkpoint，首次派发为空；恢复派发列出已 `sent` 的 `{commandId,occurrence}`。Bot Worker 必须验证引用存在于当前 occurrence plan，匹配项不排队、不调用 `bot.chat`、不重复发终态；非法或重复引用以 `COMMAND_ARGUMENT_INVALID` 显式拒绝整项计划。
- `stepId` 是运行快照内稳定的命令计划步骤 ID；当前单计划主路径固定为 `command-schedule`。`scheduleRunId` 必须是跨所有运行/Bot/step attempt 全局唯一 UUIDv4，并在 CP occurrence checkpoint 创建时持久化；同一 IPC 重放复用，restart_step 必须生成新值。计划级 `correlationToken` 固定为 UUIDv5(namespace=scheduleRunId, name=UTF-8 `correlation + NUL + 小写标准botUuid + NUL + stepId`)；`jitterSeed` 固定为 SHA-256(UTF-8 `scheduleRunId + NUL + 小写标准botUuid + NUL + stepId`) 前 8 字节 unsigned big-endian 的十进制字符串。CP 重启后从持久 scheduleRunId 复算，Worker/Bot Worker 必须校验请求值匹配，禁止随机重生成。
- 重复命令执行项身份为 `commandId + occurrence`，`occurrence` 从 0 开始；其 `actionRunId` 固定为 UUIDv5(`scheduleRunId`, UTF-8 `botUuid + "\u0000" + stepId + "\u0000" + commandId + "\u0000" + occurrence`)。同一计划重放可复算相同值，重试不改变该值；恢复 checkpoint 仍按运行/Bot/步骤/commandId/occurrence 判断是否跳过既有 sent 项。
- `start.mode='absolute'` 时 scheduleStartAtUnixMs 与 runDeadlineUnixMs 由 CP 从冻结运行快照计算并跨恢复保持不变；`start.mode='barrier'` 时只保存 prepared 计划，直至收到匹配 release。Worker/Bot Worker 不以接收时刻作为计划起点。
- `jitterSeed` 是规范十进制字符串，必须匹配 `0|[1-9][0-9]{0,19}`，禁止符号、前导零、空白和 JSON number；CP/Worker/Bot Worker 原样传递。Go/Node 必须按同一算法计算 plannedAt：先展开全部 occurrence，记录 commandDeclarationIndex、occurrence 和基础时间 `base=scheduleStartAtUnixMs+atMs+occurrence*intervalMs`，再按 `(base ASC, commandDeclarationIndex ASC, occurrence ASC)` 排序；随后对该有序序列逐项计算 jitter。jitter 输入为 UTF-8 `jitterSeed原字符串 + NUL + 小写标准botUuid + NUL + commandId + NUL + occurrence十进制`，取 SHA-256 前 8 字节按 unsigned big-endian 得到 u（Go 使用 uint64，Node.js 必须使用 BigInt，禁止 Number 丢精度），先令 `r=有符号整数(u % (2*jitterMs+1))`，再计算 `offset=r-jitterMs`，禁止在 uint64 上直接相减导致下溢。固定测试向量（jitterMs=20、botUuid=`00000000-0000-0000-0000-000000000001`、occurrence=0）：seed `20260720`/commandId `a` 的 SHA 前缀 `064411579a38e3cc`，r=7、offset=-13；commandId `b` 的前缀 `adf654ab3f1e96d0`，r=30、offset=10。原始值 `base+offset` 先钳制到 `[scheduleStartAtUnixMs, min(scheduleStartAtUnixMs+durationMs, runDeadlineUnixMs-1)]`，再按上述排序后的序列钳制为不小于前一 plannedAt；同毫秒项仍按该排序序列执行。jitterMs=0 时 offset=0；runDeadline 必须晚于 scheduleStart。CP 生成取消 plannedAt、Bot Worker 排程和结果回报必须复用该算法。
- Worker 写入成功不等于命令发送成功；只有收到 Node 最终结果事件后才能更新 `sent/failed/timedOut/cancelled`。

### Node → Go：`command-schedule-accepted` / `command-schedule-result`

```ts
type CommandScheduleAccepted =
  | {evt:'command-schedule-accepted';requestId:string;scheduleRunId:string;accepted:true}
  | {evt:'command-schedule-accepted';requestId:string;scheduleRunId:string;accepted:false;errorCode:'COMMAND_ARGUMENT_INVALID'|'COMMAND_RUNTIME_UNAVAILABLE'|'COMMAND_SCHEDULE_REJECTED'|'COMMAND_DEADLINE_EXCEEDED';message:string}

type CommandAttemptError = {attempt:number;errorCode:string;message:string;observedAtUnixMs:number}
interface CommandScheduleResultBase {
  evt:'command-schedule-result';runId:number;runUuid:string;botUuid:string;generation:number
  stepId:string;scheduleRunId:string;actionRunId:string;correlationToken:string
  commandId:string;occurrence:number;attempt:number;durationMs:number;observedAtUnixMs:number
  attemptErrors:CommandAttemptError[]
}
type CommandFailedErrorCode =
  | 'COMMAND_ROUTE_FAILED'|'COMMAND_IPC_FAILED'|'COMMAND_ARGUMENT_INVALID'
  | 'COMMAND_RUNTIME_UNAVAILABLE'|'COMMAND_SCHEDULE_REJECTED'
  | 'COMMAND_SEND_FAILED'|'ACTION_INTERNAL_ERROR'
type CommandScheduleResult = CommandScheduleResultBase & (
  | {status:'sent';plannedAtUnixMs:number;sentAtUnixMs:number;errorCode?:never;message?:never}
  | {status:'failed';plannedAtUnixMs:number|null;sentAtUnixMs:null;errorCode:CommandFailedErrorCode;message:string}
  | {status:'timed_out';plannedAtUnixMs:number|null;sentAtUnixMs:null;errorCode:'COMMAND_DEADLINE_EXCEEDED';message:string}
  | {status:'cancelled';plannedAtUnixMs:number|null;sentAtUnixMs:null;errorCode:'ACTION_CANCELLED';message?:string}
)
```

显式 `accepted=false` 必须携带上述唯一具体错误码，Worker 原样用于逐项终态；`COMMAND_DEADLINE_EXCEEDED` 生成 timed_out，其他码生成 failed，不得改写为 `COMMAND_IPC_FAILED`。若在 deadline 前未收到 accepted，状态视为未知，不构造 accepted=false，也不立即合成终态。

```json
{
  "evt": "command-schedule-result",
  "runId": 123,
  "runUuid": "uuid",
  "botUuid": "uuid",
  "generation": 7,
  "stepId": "command-schedule",
  "scheduleRunId": "uuid",
  "actionRunId": "uuid",
  "correlationToken": "uuid",
  "commandId": "announce-ready",
  "occurrence": 0,
  "attempt": 1,
  "plannedAtUnixMs": 0,
  "sentAtUnixMs": 0,
  "durationMs": 4,
  "observedAtUnixMs": 0,
  "status": "sent",
  "attemptErrors": []
}
```

- `accepted` 表示计划已进入 Bot Worker scheduler；absolute 模式将按绝对起点执行，barrier 模式仅表示 prepared/arrived，release 前不得启动 occurrence。拒绝时返回 `accepted=false + errorCode/message`。`requestId` 到此结束生命周期，不进入异步结果或动作身份。
- `status` 固定为 `sent | failed | timed_out | cancelled`。deadline/duration 到期只能返回 `timed_out + COMMAND_DEADLINE_EXCEEDED`，禁止用 failed 表达。`sent` 仅表示调用 `bot.chat` 未同步抛错，plannedAt/sentAt 必须均为整数；调用前路由、参数展开、scheduler 状态或 IPC 处理失败，以及最终 `bot.chat` 同步抛错，均返回 `failed`，sentAt 固定 null；cancelled 的 sentAt 固定 null，prepared 未 release 时 plannedAt 为 null，release 后取消未开始项时 plannedAt 为整数。禁止用 0 代替未知时间。`attemptErrors` 只保存先前失败尝试摘要，单项最多 2 条。
- 每个 `commandId + occurrence` 只发送一次最终 `command-schedule-result`。Worker 将其映射为一个既有 `BotActionEvent`：`session_uuid=runUuid`、`bot_uuid=botUuid`、`generation` 原样、`action_run_id=actionRunId`、`step_id=stepId`、`attempt=最终尝试次数`、`correlation_token` 原样、`duration_ms/observed_at_unix_ms` 原样；状态映射为 `sent→succeeded`、`failed→failed`、`timed_out→timed_out`、`cancelled→cancelled`，错误字段原样。`result_json` 保留 `scheduleRunId/commandId/occurrence/plannedAtUnixMs/sentAtUnixMs/status/attemptErrors`。
- 该映射不发送逐尝试 Fleet 终态，不与首终态胜出规则冲突；ActionResultService 仍按唯一 `actionRunId` 接收首个终态。不新增 ProbeEvent 或业务事件通道。
- CP checkpoint 的稳定唯一键为 `runUuid + botUuid + stepId + commandId + occurrence`；`generation/scheduleRunId/actionRunId/attempt` 是最近一次执行记录，不是跨恢复身份。取消产生的未执行项必须逐项返回 `cancelled`，不得静默丢失。

### Go → Node：`command-schedule-release`

```ts
interface CommandScheduleRelease {
  cmd:'command-schedule-release'; requestId:string
  runUuid:string; botUuid:string; generation:number; stepId:string
  scheduleRunId:string; barrierKey:string; releaseAtUnixMs:number
}
type CommandScheduleReleaseResult =
  | {evt:'command-schedule-release-result';requestId:string;scheduleRunId:string;accepted:true;alreadyReleased:boolean}
  | {evt:'command-schedule-release-result';requestId:string;scheduleRunId:string;accepted:false;errorCode:'COMMAND_RUNTIME_UNAVAILABLE'|'COMMAND_SCHEDULE_REJECTED'|'COMMAND_DEADLINE_EXCEEDED';message:string}
```

- 仅 start.mode=barrier 且已 accepted/prepared 的计划可 release；barrierKey 必须匹配，releaseAt 必须晚于当前时刻并早于 run deadline。accepted 后以 releaseAt 作为 scheduleStartAtUnixMs，并按统一 jitter 算法一次性生成 plannedAt。
- release 幂等键为 `runUuid + botUuid + generation + scheduleRunId`。相同 releaseAt 重放返回 accepted/alreadyReleased=true，不重建 timer；不同 releaseAt 冲突显式拒绝。未收到 result 时状态 unknown，Worker/CP 不伪造释放成功。
- 该 release 只属于 FR-369 通用命令时间屏障，不使用 `SignalBotActions`；Scenario V2 仍使用自己的 barrier signal。

### Go → Node：`command-schedule-cancel`

```ts
interface CommandScheduleCancel {
  cmd:'command-schedule-cancel'; requestId:string
  runUuid:string; botUuid:string; generation:number
  stepId:string; scheduleRunId:string; reason:string
}
type CommandScheduleCancelResult =
  | {evt:'command-schedule-cancel-result';requestId:string;scheduleRunId:string;accepted:true;alreadyCancelled:boolean}
  | {evt:'command-schedule-cancel-result';requestId:string;scheduleRunId:string;accepted:false;errorCode:'COMMAND_RUNTIME_UNAVAILABLE'|'COMMAND_SCHEDULE_REJECTED';message:string}
```

- 取消幂等键为 `runUuid + botUuid + generation + scheduleRunId`。Bot Worker 释放 scheduler 资源后保留轻量取消 tombstone 30 分钟，且每 child 最多 10000 条、按完成时间 LRU 淘汰；保留期内重复请求返回 `accepted=true, alreadyCancelled=true`，不重复产生终态。
- cancel-result 只确认取消 intent 已进入对应 Bot Worker scheduler，不表示所有 occurrence 已终态。尚未开始的执行项逐项发送 `command-schedule-result(status='cancelled', errorCode='ACTION_CANCELLED', attempt=1)`；正在执行的同步 `bot.chat` 不强制中断，其完成后不再启动后续项。
- tombstone 不作为持久真源。Bot Worker child 重启或 tombstone 淘汰后，Worker 必须以 CP 已持久化的 cancel intent 与 occurrence checkpoint 为准：已终态项不重放，未终态项由 Worker 合成 cancelled 并可直接合成 `accepted=true, alreadyCancelled=true`；仅在 CP 无取消 intent 且 Bot Worker 无活动计划/tombstone 时返回 `accepted=false, COMMAND_SCHEDULE_REJECTED`。未收到 cancel-result 时状态未知，不立即伪造取消成功；运行 deadline/重连 reconcile 按 checkpoint 收敛。FR-369 取消不使用 `SignalBotActions`。

### 错误码、attempt 与前置失败

| 场景 | errorCode | 是否重试 | 终态来源 |
|---|---|---|---|
| CP 无法解析执行节点/路由 | `COMMAND_ROUTE_FAILED` | 否 | CP CommandScheduleCoordinator 合成 |
| Worker 在命令未交付前确定写 stdin 失败，或 child 在 accepted 前确定退出 | `COMMAND_IPC_FAILED` | 否 | Worker 合成并经 Fleet stream 上报 |
| 模板展开或参数非法 | `COMMAND_ARGUMENT_INVALID` | 否 | 发现该错误的 CP/Worker/Bot Worker |
| Bot 不存在或 generation 不匹配 | `COMMAND_RUNTIME_UNAVAILABLE` | 否 | Worker/Bot Worker |
| accepted 明确返回 scheduler 已关闭或计划冲突 | `COMMAND_SCHEDULE_REJECTED` | 否 | Bot Worker 返回具体码；Worker 原样合成 |
| duration/run deadline 到期 | `COMMAND_DEADLINE_EXCEEDED` | 否 | Bot Worker；accepted 前到期由 Worker 合成；统一落 ActionResult timed_out |
| `bot.chat` 同步抛错且重试耗尽 | `COMMAND_SEND_FAILED` | 是，最多 3 次 | Bot Worker |
| stop/cancel 使执行项未发送 | `ACTION_CANCELLED` | 否 | Bot Worker；尚未 accepted 时由 CP/Worker 合成 |
| 其他不可归类内部错误 | `ACTION_INTERNAL_ERROR` | 否 | 发现该错误的层 |

- FR-369 实现时必须把上述 `COMMAND_*` 加入 ActionResultService 冻结 allowlist；未落地前正式当前 API 不宣称可产生这些码。
- 所有终态 `attempt >= 1`：发送过时取最终尝试次数；发送前失败或取消统一取 1。`failed/timed_out/cancelled` 必须有非空 errorCode。
- CP 从冻结 schedule 生成 occurrence plan，Worker/Bot Worker 从该 plan 校验全部 occurrence，并使用同一 actionRunId 公式。RPC 调用前的确定路由失败由 CP 逐项合成；Worker 在 Node accepted 前的确定失败逐项合成 Fleet 终态。accepted 后正常只由 Node 最终 result 产生终态；仅 disposition 有界收敛表规定的 operation deadline、release/cancel 失败路径允许 CP/Worker 合成。传输超时但接受状态不确定时不得提前合成，最终仍按 actionRunId 首终态胜出。

## 4. 模板变量

命令模板只允许以下变量：

`botName`、`botOrdinal`、`cohortKey`、`runId`、`actionRunId`、`correlationToken`

`runId` 固定展开为 `bot_stress_sessions.id` 的十进制数字；运行 UUID 通过 IPC/API 的独立 `runUuid` 字段传递，不与模板变量混用。`roomKey`、`areaId` 以及任何其他业务变量均不允许出现；未知变量必须在计划提交时拒绝，不能通过嵌套字段或动态表达式绕过白名单。

## 5. 验收接口边界

自动化测试必须覆盖 500 个 Bot 命令顺序、屏障窗口、调度延迟 p95、取消和无 timer 泄漏。真机测试只验证普通 Minecraft 命令发送及 `bot.chat` 异常语义，不验证房间、区域、塔防或其他业务效果。
