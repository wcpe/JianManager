# 超级规格：500+ Bot 分布式命令压测平台

> 状态：已审核（2026-07-20）　·　关联 PRD：FR-351/352/354/358～361　·　协调分支：feature/bot-load-testing
> 子规格：FR-351 分布式调度 / FR-352 场景引擎兼容 / FR-358 命令编排 / FR-354 恢复归真 / FR-359 运行判定 / FR-360 创建向导 / FR-361 观测报告
> 架构决策：`ADR-074`（分布式拓扑）+ `ADR-075`（命令动作成功边界）

## 1. 背景与目标

现有 Bot 平台已具备 Mineflayer 子进程、单 Bot 管理、压测会话、FR-274 YAML 编排，以及 FR-351/352 的分布式 Fleet 与 Scenario V2 地基。但 500+ Bot 运行仍缺少通用命令计划、长稳恢复、负载状态机、默认判定、创建向导和会话级观测闭环。

ADR-075 冻结通用命令动作成功边界：Bot Worker 调用 `bot.chat` 且未同步抛错即为命令发送成功。该结果不证明服务器接受或执行命令，不验证权限，也不证明产生预期业务效果。ServerProbe、塔防插件、聊天回显和业务事件均不得成为通用命令链路的必需依赖。

本批目标：

1. 多个 Worker 作为发压节点，500+ Bot 可共同连接同一个目标服。
2. 通过 `command_schedule` 配置相对时间命令、重复、持续时间和受限抖动。
3. 支持固定长稳、阶梯升压和突发洪峰三种负载模式。
4. 默认聚合连接、命令发送、调度完成、屏障和 Worker 健康，形成 verdict 与报告。
5. 建立重连、子进程恢复、Worker reconcile 和命令检查点恢复。
6. 前端完成模板、预检、启动、实时观测、失败诊断和报告闭环。

FR-352 已实现的移动、攻击、外部信号和塔防预设作为历史或可选场景兼容能力保留，但不再是新模板、默认运行、预检或通用交付验收的主路径。

## 2. 已确认决策

| 维度 | 决策 |
|---|---|
| 发压拓扑 | 多 Worker 分布式；目标实例与执行节点解耦 |
| 登录认证 | 离线测试环境使用唯一 Bot 昵称；不建设 Microsoft 账号池 |
| 默认编排 | `command_schedule`；内置预设 `command-orchestration-v1` |
| 命令成功 | `bot.chat` 未同步抛错即发送成功；不验证服务端结果 |
| 负载模式 | stable / step / spike |
| 默认判定 | 连接、命令发送、调度完成、屏障、schedule lag、Worker 健康与 crash |
| 可选观测 | TPS、MSPT、房间、区域、伤害等 nullable optional legacy 指标 |
| 运行入口 | 手动运行 + 可复用模板 |
| 范围外 | 服务端权限和业务效果通用验证、技能、放塔、商店、复杂背包、定时压测、CI 门禁、云资源自动扩缩容 |

默认严格验收：500+ Bot 连续 60 分钟；在线率、命令发送率、调度完成率、Worker 健康率及已配置屏障的到达率均不低于 99%；`command_schedule` lag p95 不高于 1 秒；Worker/bot-worker 非预期 crash 为 0。未配置屏障时该指标为 `not_applicable`，不进入分母。

## 3. 不可违反的架构约束

1. 不新增第四类常驻进程。
2. Control Plane 是唯一浏览器 HTTP/SSE 入口和唯一数据库读写方。
3. Control Plane 只经 gRPC 委托 Worker，不直接管理 Bot Worker 子进程。
4. Worker 不访问数据库；Bot Worker 不访问数据库、HTTP 或 gRPC。
5. Worker 与 Bot Worker 继续使用 stdin/stdout JSON 行协议。
6. 目标实例与 Bot 执行节点可以不同；权限和可选目标实例观测仍归目标实例。
7. 单 Worker 默认容量为 50；通过多节点扩容，不把常量直接放大为 500。
8. 浏览器只订阅会话级聚合 SSE；不得为 500 个 Bot 常驻 500 条 SSE。
9. 既有单 Bot API、50 Bot 会话、Scenario V2 和旧 YAML 会话保持向后兼容。
10. ServerProbe 仍可服务实例监控和独立业务适配，但不是 Bot 命令压测必经链路。

## 4. 总体链路

```text
浏览器
  │ HTTP + 单运行 SSE
  ▼
Control Plane
  ├─ 模板与运行账本
  ├─ 发压节点容量目录
  ├─ 分片/批次/负载状态机
  ├─ command_schedule 冻结、分发与全局协调
  ├─ 指标聚合、阈值与报告
  └─ 目标实例权限与审计
       │ gRPC（隧道优先/直拨回退）
       ├────────────┬────────────┬────────────┐
       ▼            ▼            ▼            ▼
   Worker A     Worker B     Worker C     Worker N
       │ IPC        │ IPC        │ IPC        │ IPC
       ▼            ▼            ▼            ▼
 Bot Worker A Bot Worker B Bot Worker C Bot Worker N
       └──────────── Mineflayer 连接目标服 ────────────┘
```

Control Plane 冻结绝对命令计划、分配执行节点并协调运行/取消/deadline；Worker 负责可靠透传与前置失败收敛；每个 Bot Worker 以冻结绝对时间在本地集中 scheduler 中排队并调用 `bot.chat`，避免跨节点网络抖动形成 CP 定时器风暴。系统只有这一层实际定时队列，不做 CP 与 Bot Worker 双重调度；Worker 与 Control Plane 只透传、聚合结果，不将缺少服务端确认解释为失败。

可选 ServerProbe 或业务适配器只产生独立 legacy 观测，不推动通用命令动作成功，不改变默认 verdict，也不影响 preflight `ready`。

## 5. 领域标识

| 名称 | 含义 | 格式/来源 |
|---|---|---|
| templateId | 可复用命令压测模板 | DB 数字 ID + UUID |
| runId | 运行数据库主键，跨 API/IPC 使用十进制数字 | `bot_stress_sessions.id` |
| runUuid | 一次运行的不可变公开标识 | `bot_stress_sessions.uuid` |
| cohortKey | 兼容的运行内 Bot 分组 | 模板内唯一小写 key |
| batchId | 某执行 Worker 上的一批 Bot | UUID |
| botId | Bot 运行身份 | `bots.uuid` |
| generation | Bot desired-state 版本 | int64，期望配置或状态变化时递增 |
| stepId | 命令或兼容场景步骤标识 | 模板内唯一字符串 |
| actionRunId | 某 Bot 某个 commandId+occurrence 的稳定执行身份；该 occurrence 内最多 3 次发送尝试均复用 | 确定性 UUIDv5 |
| commandId | 命令计划内稳定命令标识 | 计划内唯一字符串 |
| correlationToken | 整个 Bot 命令计划的链路诊断关联标识；同一 schedule 的 occurrence 原样复用 | UUID，不是凭据或业务成功证明 |
| attempt | 某 occurrence 内 `bot.chat` 发送尝试序号 | 从 1 开始，最大 3 |
| eventSeq | Bot Worker 事件序号 | 子进程世代内单调 int64 |
| workerEpoch | Bot Worker 启动标识 | UUID，仅诊断 |
| workerEpochGeneration | Worker 内可比较的 Bot Worker 世代 | int64 |

禁止仅以显示名称、列表顺序或时间戳作为幂等键。

## 6. 最终数据模型与 FR 所有权

### 6.1 `bots` 增量字段

| 字段 | 所属 FR | 语义 |
|---|---|---|
| executor_node_id / load_batch_id | FR-351 | 执行节点与分片批次 |
| cohort_key | FR-352 | 兼容场景分组 |
| desired_state / desired_state_generation | FR-354 | CP 期望状态及版本；协议字段 `generation` 映射现有 desired_state_generation，不新增 generation 列 |
| worker_epoch / worker_epoch_generation / last_event_seq | FR-354 | 运行事件世代与顺序 |
| config_hash / last_seen_at / reconnect_count | FR-354 | reconcile、新鲜度与恢复统计 |
| connected_at | FR-359 | 最近连接完成时间 |

`InstanceID` 始终表示目标实例和权限归属；`ExecutorNodeID` 是运行 Mineflayer 的 Worker 路由真源。

### 6.2 `bot_stress_sessions` 增量字段

保持现有表名和 API 路径：

- `schema_version`：FR-358/共享地基负责加性迁移、历史默认 1 和 schemaVersion=1 判别联合序列化，含 FR-358 commandSchedule 兼容运行；FR-359 新运行写 2。
- `template_id`、`load_profile`、`thresholds`、`run_state`、`current_stage`、`verdict`、`max_stable_bots`、`failure_summary`、`report_summary`：FR-359。V2 专属列允许 schemaVersion=1 历史行 null，service 只对 schemaVersion=2 强制完整。
- `allocation_plan`：FR-351。
- `scenario_snapshot`：FR-352 历史兼容快照。
- `command_schedule_snapshot`：FR-358 通用命令计划快照。

旧 `status` 保留并与 `run_state` 同事务映射：pending/preflighting/ready/starting→pending，running/degraded/stopping/cancelling→running，completed/cancelled→stopped，failed→error。数据库 ended_at 是唯一终止时间；V1 只返回 stoppedAt 别名，V2 终态同时返回值相等的 endedAt/stoppedAt，非终态均省略。

### 6.3 运行表

- `bot_load_batches`（FR-351）：分片、幂等键、计划数、接受数、连接数、失败数及批次状态。
- `bot_load_templates`（FR-359）：名称、描述、命令计划、profile、thresholds、标签和创建者；不保存目标实例、执行节点或凭据。
- `bot_load_action_results`（FR-352/358）：`run/bot/cohort/step/actionRunId/attempt/status/error/duration/result`；表内状态继续使用 `running/succeeded/failed/timed_out/cancelled`，通用命令的 IPC `sent` 映射为 `succeeded`，deadline 终态映射为 `timed_out`，并在 result 中保留 `status=sent/commandId/occurrence/scheduleRunId`。`sent` 固定表示 `bot.chat` 未同步抛错。
- `bot_load_command_checkpoints`（FR-358/354）：稳定唯一键为 `runUuid/botUuid/stepId/commandId/occurrence`，在 Apply 前即物化并记录最近 `generation/scheduleRunId/actionRunId/plannedAt(nullable)/sentAt(nullable)/attempt/status/errorCode/endedAt`；correlationToken 与 jitterSeed 按 FR-358 API 从持久 scheduleRunId/botUuid/stepId 确定性复算，不另存随机值；恢复时默认跳过已 `sent` 执行项，新的 actionRunId 不改变 checkpoint 身份。
- `bot_load_metric_samples`（FR-359）：每运行默认 5 秒一行，保存 Bot 计数、命令计数、调度 lag、屏障、执行节点健康及 nullable legacy 指标。
- `bot_load_run_events`（FR-359）：append-only 保存 run-state、stage、barrier、scenario-action、command-schedule、command-send、worker-health、executor-crash、safety-stop、report-ready；不逐条持久化高频普通命令事件。

#### 6.3.1 可迁移数据库契约

跨 SQLite/MySQL 使用以下逻辑类型：`ID`=MySQL BIGINT UNSIGNED / SQLite INTEGER；`JSON_TEXT`=MySQL LONGTEXT / SQLite TEXT，写入前后由强类型 DTO 校验；`UTC_TIME_MS`=MySQL DATETIME(3) / SQLite RFC3339 UTC TEXT；`UNIX_MS`=BIGINT/INTEGER。所有外键列与目标主键同宽，时间精度固定毫秒。

**`bot_stress_sessions` 加性列**

| 列 | 逻辑类型 | null/default | 索引/约束 |
|---|---|---|---|
| schema_version | SMALLINT | NOT NULL DEFAULT 1 | INDEX；仅 1/2 |
| template_id | ID | NULL | FK bot_load_templates(id) ON DELETE SET NULL；INDEX |
| command_schedule_snapshot | JSON_TEXT | NULL | ≤256KiB |
| load_profile / thresholds | JSON_TEXT | NULL | 各≤64KiB；schema=2 必填 |
| run_state | VARCHAR(32) | NULL | INDEX；schema=2 冻结枚举 |
| current_stage | INT | NULL | schema=2 非负 |
| verdict | VARCHAR(16) | NULL | INDEX；pending/passed/failed/aborted |
| max_stable_bots | INT | NULL | 非负 |
| failure_summary | JSON_TEXT | NULL | ≤64KiB |
| report_summary | JSON_TEXT | NULL | ≤4MiB |

旧列 status/ended_at 等保持不变；schemaVersion=1 允许上述 V2 列 null，schemaVersion=2 由 service 在事务提交前强制完整。

**`bot_load_templates`**

| 列 | 逻辑类型 | null/default | 索引/约束 |
|---|---|---|---|
| id / uuid | ID / CHAR(36) | PK / NOT NULL | uuid UNIQUE |
| created_by | ID | NOT NULL | FK users(id) ON DELETE RESTRICT；INDEX |
| active_name_key | CHAR(64) | NULL | UNIQUE(created_by,active_name_key) |
| name / description | VARCHAR(128) / TEXT | NOT NULL / NOT NULL | name 为 trim 后展示值 |
| command_schedule / load_profile / thresholds / tags | JSON_TEXT | NOT NULL | 分别≤256KiB/64KiB/64KiB/64KiB |
| created_at / updated_at / deleted_at | UTC_TIME_MS | NOT NULL/NOT NULL/NULL | INDEX(created_by,updated_at)、INDEX(deleted_at) |

软删必须在同事务设置 deleted_at 与 active_name_key=NULL；创建/改名先计算 SHA-256 hex，唯一冲突映射 409。

**`bot_load_command_checkpoints`**

| 列 | 逻辑类型 | null/default | 索引/约束 |
|---|---|---|---|
| id | ID | PK | — |
| stress_session_id | ID | NOT NULL | FK bot_stress_sessions(id) ON DELETE CASCADE；INDEX |
| bot_id / run_uuid / bot_uuid | ID / CHAR(36) / CHAR(36) | NULL / NOT NULL / NOT NULL | bot_id FK bots(id) ON DELETE SET NULL |
| step_id / command_id / occurrence | VARCHAR(64) / VARCHAR(64) / INT | NOT NULL | UNIQUE(run_uuid,bot_uuid,step_id,command_id,occurrence) |
| generation | BIGINT | NOT NULL | >0 |
| schedule_run_id / action_run_id | CHAR(36) / CHAR(36) | NOT NULL | INDEX(schedule_run_id)、INDEX(action_run_id) |
| planned_at_unix_ms / sent_at_unix_ms | UNIX_MS | NULL | prepared-before-release 可 null |
| attempt / status / error_code | INT / VARCHAR(32) / VARCHAR(64) | NOT NULL / NOT NULL / NULL | status=prepared/scheduled/sent/failed/timed_out/cancelled；INDEX(session,status) |
| ended_at / created_at / updated_at | UTC_TIME_MS | NULL/NOT NULL/NOT NULL | INDEX(session,updated_at) |

**`bot_load_metric_samples`**

| 列 | 逻辑类型 | null/default | 索引/约束 |
|---|---|---|---|
| id / stress_session_id | ID / ID | PK / NOT NULL | FK session ON DELETE CASCADE |
| sampled_at / stage_index | UTC_TIME_MS / INT | NOT NULL / NOT NULL | UNIQUE(session,sampled_at)；INDEX(session,stage_index,sampled_at) |
| counts_json / command_json / barrier_json / executor_json / latency_json / errors_json | JSON_TEXT | NOT NULL | 每列≤1MiB |
| target_legacy_json | JSON_TEXT | NULL | ≤1MiB；nullable 不写 0 |

样本终态后停止写入，30 天清理；report_summary 与 run events 不随样本 TTL 删除。

**`bot_load_run_events`**

| 列 | 逻辑类型 | null/default | 索引/约束 |
|---|---|---|---|
| id / stress_session_id / run_uuid | ID / ID / CHAR(36) | PK / NOT NULL / NOT NULL | FK session ON DELETE CASCADE；INDEX(session,id) |
| type / occurred_at / stage_index | VARCHAR(32) / UTC_TIME_MS / INT | NOT NULL / NOT NULL / NULL | INDEX(session,type,id)、INDEX(session,occurred_at,id) |
| action_run_id / bot_uuid / executor_node_id / step_id | CHAR(36) / CHAR(36) / ID / VARCHAR(64) | NULL | 各建 `(session,<field>,id)` 复合索引 |
| payload_json / legacy_json | JSON_TEXT | NOT NULL / NULL | payload≤64KiB；legacy≤64KiB |

事件 id 自增且永不复用；同 actionRunId 终态投影用 UNIQUE(session,type,action_run_id)（action_run_id NULL 时不参与），barrier 分块/聚合事件依赖普通自增 id。run events 保留到会话删除，不使用 30 天 metric TTL。

`bot_load_batches` 与 `bot_load_action_results` 沿 FR-351/352 现有列型和索引原地复用；FR-358 只扩充 ActionResult error allowlist/result JSON，不新建竞争动作表。

FR-352 的公开 Scenario/action result 与屏障信号继续作为当前能力；旧 ProbeEvent 持久化不属于 FR-352 当前数据契约。新通用命令运行不得依赖 ProbeEvent 建表、写入或消费。

## 7. 共享状态机

### 7.1 运行

```text
pending → preflighting → ready → starting → running ↔ degraded
                                              ↓
                                         stopping → completed
                                              ↓
                                       cancelling → cancelled
任一不可恢复路径 → failed
```

只有 `ready` 可启动。stop 在 `starting|running|degraded` 接受并进入 stopping，在 `stopping` 幂等返回当前运行；cancel 在任一非终态接受：`pending|preflighting|ready` 无命令计划可取消时直接经 cancelling 收束，`starting|running|degraded|stopping` 持久化 cancel intent 后取消命令计划与 Bot，`cancelling` 幂等返回当前运行；终态请求返回 409。安全阈值命中走 stopping，终态 `completed + verdict=aborted`；无法收束的内部错误才进入 failed。

### 7.2 Bot desired/runtime

- CP 数据库是 desired-state 真源；Worker/Bot Worker 是 runtime 真源。
- 新订阅先拉 FleetSnapshot 建 baseline，再接 stream。
- generation、workerEpochGeneration、eventSeq 和 configHash 共同隔离旧事件。
- 状态超过新鲜度窗口必须从 connected 收敛为 disconnected/error。
- stop/cancel 后任何重连、恢复或迟到事件都不得继续发送命令。

### 7.3 命令计划

```text
pending → scheduled → sent
                    ↘ failed
                    ↘ timed_out
                    ↘ cancelled
```

命令动作在调用前路由失败、进程通信失败、参数处理失败或 `bot.chat` 同步抛错时失败；`bot.chat` 调用未同步抛错即记录 `sent`。服务端拒绝、权限不足或未产生业务效果不回写通用命令状态；需要此类结论时必须读取独立的可选业务观测。

## 8. 通用 `command_schedule`

```json
{
  "commands": [
    {"id":"ready","atMs":0,"command":"/say ready"},
    {"id":"list","atMs":500,"command":"/list","repeat":{"intervalMs":1000,"count":3}}
  ],
  "durationMs":4000,
  "jitterMs":25
}
```

规则：

- `commands` 必须为 1..100 项，每项 `id` 在计划内唯一且稳定；时间必须为非负有限整数；命令非空；未知字段、越界重复和超出 duration 的命令必须拒绝。
- 同计划按 `atMs`、声明顺序和重复序号稳定执行；checkpoint 使用 `commandId + occurrence`，不得使用数组下标。
- 集中 scheduler 管理排队、取消、有限重试、结果聚合和资源释放；不得为每条重复命令永久保留独立 timer。V1 仅对 `bot.chat` 同步抛错最多尝试 3 次，固定退避 250ms、500ms，且不越过 duration/运行 deadline；其他错误不重试。
- 取消幂等；取消后未发送命令不得执行。
- 聚合结果至少包含 `total/sent/failed/timedOut/cancelled/retries/startedAt/endedAt`。
- 计划结束、取消、失败或调度器关闭后必须释放 timer 和状态。

模板变量白名单仅为：

- `{{botName}}`
- `{{botOrdinal}}`
- `{{cohortKey}}`
- `{{runId}}`
- `{{actionRunId}}`
- `{{correlationToken}}`

`runId` 固定展开为 `bot_stress_sessions.id` 的十进制数字；运行 UUID 使用独立 `runUuid` 字段。`roomKey`、`areaId` 和其他业务变量必须在提交时拒绝；禁止表达式、文件读取、环境变量和脚本执行。

内置 `command-orchestration-v1` 只提供有序命令、发送间隔和执行顺序示例，不绑定任何玩法字段。

### 8.1 Scenario V2 兼容

- 既有公开 Scenario V2 JSON/YAML 创建、冻结、执行和详情契约保持可用；cohort、屏障、移动、攻击和旧塔防预设继续支持显式场景。
- 新通用命令模板和向导不得要求 room/area/monster/tower 字段，也不得默认创建 `wait_probe_event` 或业务断言。
- `send_command` 的成功语义统一服从 ADR-075；后续 legacy 观测只能独立展示。
- 旧 `orchestrationYaml` 与 `behavior` 保留兼容；`scenario`、`commandSchedule` 与 `orchestrationYaml` 三者最多提供一个，均不得改变历史数据。

## 9. 跨进程协议

保留 FR-351/352 已新增的：

- `GetBotCapacity`
- `ApplyBotBatch`
- `GetBotFleetSnapshot`
- `StreamBotFleetEvents`
- `SignalBotActions`

约束：

- `ApplyBotBatch` 每批最多 50 个 assignment，幂等重放不重复连接。
- FR-358 在 CP↔Worker 加性新增 `ApplyBotCommandSchedules`、`ReleaseBotCommandSchedules` 与 `CancelBotCommandSchedules` 三个批量 RPC，每批最多 100 个 Bot 项。Apply 下发 CP 已完成变量展开/actionRunId/jitter/文本复验的 occurrence plan、absolute/barrier 启动模式和已完成 occurrence 跳过集；Release 以共同绝对 releaseAt 启动 prepared 命令计划；Cancel 下发 CP 已持久化的 cancel intent 与未终态 occurrence 集。同步逐项结果固定为 `accepted|rejected|unknown`，异步 occurrence 终态仍只经 Fleet stream `action_event` 回传。
- Worker 不访问 CP 数据库。重启/reconcile 所需 checkpoint、skip/unresolved occurrence 必须由 CP 放入上述 RPC；不得扩展 `ApplyBotBatch` 偷渡命令计划，也不得令 Worker 自行猜测持久状态。
- `SignalBotActions` 每次最多 100 项，仅用于 FR-352 屏障与兼容场景外部信号，不承载 FR-358 命令计划、命令取消或通用业务成功证明。
- Fleet stream 按执行节点建立；断流先拉 snapshot 再重连。
- 所有既有 Bot 操作统一按 `ExecutorNodeID`，为空才回退目标实例节点。
- Worker/bot-worker 不解析 YAML；只接收 CP 冻结的规范快照和已调度命令。

IPC 继续使用带 `requestId` 的 `create-bots`、`stop-bots`、`signal-actions`、`get-fleet-snapshot` 及对应 result。FR-358 在 CP↔Worker gRPC 加性定义 `ApplyBotCommandSchedules`/`ReleaseBotCommandSchedules`/`CancelBotCommandSchedules`，并在同一 stdin/stdout JSON 协议上加性定义 `command-schedule`、`command-schedule-accepted`、`command-schedule-release`、`command-schedule-release-result`、`command-schedule-result`、`command-schedule-cancel`、`command-schedule-cancel-result`；字段、幂等键、accepted 未知态、reconcile payload 和 `StreamBotFleetEvents.action_event` 映射以 `../bot-load-probe-events/api.md` 为准。不得新增进程边界或让 Bot Worker 访问 HTTP/gRPC。

## 10. 负载模式

- stable：固定目标数、ramp 和观察持续时间。
- step：目标数严格递增，每级只增加差量 Bot；首次阈值失败停止升压并保留上一通过级为 `maxStableBots`。
- spike：在连接窗口内分片发起连接，可选配置 `barrier:{key,releaseWindowMs}`；记录连接 latency、命令 schedule lag 和已配置屏障 release lag 的 p50/p95/p99。schedule lag 是原始计划时间至最终成功调用 `bot.chat` 的时差；barrier release lag 在通用命令路径为 `releaseAt` 至首个基础 atMs=0 occurrence sent 的时差，在 Scenario 兼容路径为 releaseAt 至屏障后首个动作 running 的时差。

连接限流由 `connectNotBefore` 生成，命令计划 jitter 不承担连接限流语义。三类 latency 的样本集合和 nearest-rank 公式以 `api.md` 为权威：connect 仅纳入成功 connected，schedule lag 仅纳入 sent，barrier release lag 仅纳入已释放且后续动作实际开始；failed/timedOut/cancelled 不进入 percentile，零样本固定返回 null。

## 11. 指标与判定

### 11.1 默认指标

- planned / accepted / connecting / connected / disconnected / failed / stopped。
- connect latency p50/p95/p99，reconnect count/rate。
- command planned/sent/failed/timedOut/cancelled 与发送率。
- schedule completion rate、schedule lag p50/p95/p99。
- barrier waiting/arrived/released/timed-out 与到达率。
- 每 Worker active/connecting、RSS、可取得时的 CPU、eventLoop p95、dropped events 和 crash。
- gRPC/IPC 批次延迟与失败数。

### 11.2 默认阈值

```json
{
  "minOnlineRate": 0.99,
  "minCommandSentRate": 0.99,
  "minScheduleCompletionRate": 0.99,
  "minWorkerHealthRate": 0.99,
  "minBarrierArrivalRate": 0.99,
  "maxScheduleLagP95Ms": 1000,
  "maxProcessCrashes": 0,
  "safety": {
    "maxExecutorMemoryRate": 0.85,
    "maxEventLoopP95Ms": 500,
    "sustainSeconds": 30
  }
}
```

- `runMaxTargetBots` 和每级 `stageExpectedBotSet` 冻结；失败、停止或重分配不得缩小分母。
- stable 预热要求在线率连续达到阈值；观察窗口默认 3600 秒，每 5 秒采样。
- 在线、命令发送、调度完成和 Worker 健康始终适用；屏障到达只在 profile 或兼容 Scenario 配置屏障时适用。不适用指标记为 `not_applicable` 并从 verdict 分母与样本覆盖率排除；适用指标使用窗口内最小样本率，不用累计平均掩盖短时失败。
- Worker 健康率默认阈值为 `minWorkerHealthRate=0.99`；每个 5 秒采样点以冻结 `executorNodeIds` 为分母，节点同时满足 online、botWorkerReady、心跳新鲜度不超过 15 秒，且 CP 可经反向 tunnel 或 direct 任一路径取得可用 Worker RPC client 时才计健康；tunnelConnected 仅作诊断，直拨可达时不得判不健康。无样本先为 pending，连续缺样超过 30 秒失败。
- schedule lag p95 不高于阈值；样本覆盖率至少 99%，连续缺样超过 30 秒失败。
- Worker/bot-worker 非预期 crash 计入；人工 stop 和计划内重启不计。
- 前端只展示后端 verdict/reasons，不自行重算。

### 11.3 optional legacy

TPS、MSPT、在线玩家、房间、区域、波次、伤害、击杀和其他游戏指标：

- 仅在适配器提供时采集，缺失为 null，不以 0 代替。
- 不阻断 preflight，不使默认 verdict 失败或 pending。
- 可显式配置独立 legacy 判定，但必须与默认 verdict 分开展示和报告。
- 不得改变命令 `sent` 状态。

## 12. 预检、报告与免责声明

preflight 只验证：

- 目标实例权限。
- 执行节点在线、bot-worker feature、容量与分片。
- 当前运行快照（Scenario V2 或 `command_schedule`）、load profile 和 thresholds 的结构与语义。
- planToken 的生成与过期。

结构或语义非法返回对应 422；只有容量不足或节点不可用返回 200 且 `ready=false`。preflight 不验证连接配置、服务端接受、命令权限、业务效果或 ServerProbe 可用性。

页面、SSE complete、JSON 和 CSV 报告必须包含免责声明：默认结果只证明当前环境下 Bot 连接、命令发送、调度、已配置屏障和 Worker 健康达到阈值；命令发送成功仅表示 `bot.chat` 未同步抛错；结果不代表目标游戏服容量、TPS/MSPT、权限或玩法正确性。

## 13. 前端冻结边界

路由：

- `/bots?tab=fleet|sessions|templates`
- `/bots/sessions/:id?tab=overview|bots|metrics|failures|events|config`

创建向导固定五步：目标节点 → 连接 → 命令编排 → 负载曲线 → 阈值预检。默认预设为 `command-orchestration-v1`。

实时观测：

- 每页每运行只建立一条会话级 SSE。
- Bot、失败、事件分页，历史指标按区间查询，pageSize 不超过 100。
- SSE 支持 Last-Event-ID、快照补偿、慢消费者断开和终态关闭。
- 5000 Mock Bot 下 DOM 与请求数量受控，图表单序列默认不超过 1200 点。

## 14. 权限、安全与审计

- 读取使用 `bot:read`；模板写和运行操作使用 `bot:manage`。
- V1 模板是 createdBy 个人所有权资源，不绑定目标实例：非管理员只读写自己的模板，平台管理员可管理全部；活跃名称由 `(created_by, active_name_key)` 数据库唯一索引保证，active_name_key 为 trim 后名称 UTF-8 的 SHA-256 hex、软删时置 null；团队共享留待未来显式 scope 契约。
- 运行、节点容量、指标和报告仍按目标实例授权收敛；由模板创建运行必须同时校验模板所有权与目标实例管理权。
- V2 只允许 offline 连接配置，不保存账号 token 或密码。
- correlationToken 不是凭据。
- 审计 template create/update/delete、run create/preflight/start/stop/cancel/retry、report export。
- 日志和报告不得输出节点 secret、JWT、账号 token 或完整内部堆栈。

## 15. 向后兼容

1. 旧 `/bots/stress-sessions`、`/bots/stress-test`、start/stop 路径不改名。
2. 旧 `behavior`、`orchestrationYaml` 和公开兼容字段继续可读；Scenario V2 继续支持公开创建、冻结、执行和详情。
3. 旧 V1 且 `count<=50` 的单节点会话可受限使用空 body start；出现 commandSchedule/loadProfile/thresholds/executor pool 等新字段时必须显式 preflight。
4. 旧 Worker/bot-worker 可继续单 Bot 管理，但不得进入分布式预检池。
5. 数据库仅做加性迁移，不删除旧列或历史表。
6. 旧业务场景可显式继续使用，但不得重新成为新通用命令模板、默认 verdict 或通用命令验收的硬前提。

## 16. 跨 FR 所有权

| 区域 | 所有者 |
|---|---|
| Fleet gRPC、容量、批次与分片 | FR-351 |
| Scenario V2 与历史动作兼容 | FR-352 |
| command_schedule、Bot Worker 本地集中 scheduler、命令结果与变量白名单 | FR-358 |
| generation、reconcile、重连与命令 checkpoint 恢复 | FR-354 |
| run state、profile、metrics、thresholds、report、SSE 后端 | FR-359 |
| 模板与五步向导 UI | FR-360 |
| 会话详情、诊断、图表和报告 UI | FR-361 |
| PRD、ARCHITECTURE、API、CHANGELOG | 协调分支统一回写 |

## 17. 环境门与测试战略

`.tmp/bot-load-acceptance/environment.json` 不入库。

### 17.1 FR-351/352 当前交付门禁（缩比，2026-07-21 用户确认）

至少登记：

- ≥2 真 Worker 的 nodeId、bot-worker 版本、`maxBots`、隧道/直拨可达性。
- 目标服（或 e2e 同口径 fake-MC）地址、offline 配置、证据目录。
- 缩比真连规模（默认 ≥6 connected；跨节点分片预检 ≥51 planned 且 ≥2 batch）。

本地/CI 真链路入口（非 mock）：

```bash
python .tmp/bot-load-acceptance/run_local_realpath.py
```

证据：`.tmp/bot-load-acceptance/evidence/`（含 `local-realpath-report.*`、`REAL-ACCEPTANCE-SUMMARY.md`）。

### 17.2 满规模 / FR-359 harness（可选，不阻塞 FR-351/352 缩比完成）

满规模登记 10+ Worker、500 Bot、时钟偏差≤250ms。唯一满规模入口（实现后）：

```bash
JM_BOT_LOAD_ACCEPTANCE=1 JM_BOT_LOAD_ENV=.tmp/bot-load-acceptance/environment.json go test -tags=botloadacceptance ./internal/e2e -run '^TestBotLoadAcceptance$' -count=1 -timeout=4h
```

已启用但环境缺失时必须输出机器可读 `blocked`，不得静默 Skip。ServerProbe、塔防或 TPS/MSPT 只在显式启用 legacy 场景时附加验证。

## 18. 实施依赖

```text
FR-351 → FR-352 → (FR-358 ∥ FR-354) → FR-359 → (FR-360 ∥ FR-361)
```

FR-351/352：自动化 + **缩比真机门禁**（用户确认）后可完成本批；满规模 10×50/500 为可选扩容验收。FR-358～361 不因规格重定向而自动变为已实现或已交付。

## 19. 明确不做

- 不新增 Bot 微服务或消息队列。
- 不让 Control Plane 直接 spawn Node.js。
- 不简单把 `maxBots=50` 改成 500。
- 不以数据库 Bot 行数代替真实在线验收。
- 不把 `bot.chat` 发送成功描述为服务器接受、权限通过或业务成功。
- 不强制部署 ServerProbe 或业务插件才能运行通用命令压测。
- 不保存每条高频普通命令事件；只保存检查点、步骤结果和聚合指标。
- 不新增第三方依赖；确需新增时必须单独获得用户确认。
