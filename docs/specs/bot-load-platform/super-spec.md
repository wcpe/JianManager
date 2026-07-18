# 超级规格：500+ Bot 分布式游戏压测平台

> 状态：已审核（2026-07-18）　·　关联 PRD：FR-351～FR-357　·　协调分支：feature/bot-load-testing
> 子规格：FR-351 分布式调度 / FR-352 场景引擎 / FR-353 探针断言 / FR-354 恢复归真 / FR-355 运行判定 / FR-356 创建向导 / FR-357 观测报告
> 架构决策：`ADR-074`（proposed，用户审核通过后转 accepted）

## 1. 背景与目标

现有 Bot 平台已经具备 Mineflayer 子进程、单 Bot 管理、分页聚合、压测会话和 FR-274 YAML 阶段编排，并完成 50 Bot 长稳验收。但当前会话仍绑定目标实例所属的单个 Worker，Bot Worker 单进程容量固定 50；命令、移动和攻击只证明客户端发起动作，缺少服务端可信结果；断线/进程重启恢复和会话级实时观测也不足。

本批目标是在保持三进程模型和现有通信边界的前提下，把 Bot 平台扩展为：

1. 多个 Worker 作为发压节点，500+ Bot 可共同连接同一个目标服或房间。
2. Bot 按结构化场景执行主城漫游、命令进房、同步开局、抵达区域、锁敌追击和持续普通攻击。
3. ServerProbe 与可修改的塔防插件提供可信房间/战斗事件，动作结果不靠聊天文本猜测。
4. 支持固定长稳、阶梯升压和突发洪峰三种负载模式。
5. 自动采集 Bot、发压端、目标服务端指标并给出通过/失败和最大稳定容量。
6. 前端完成模板、预检、启动、实时观测、失败诊断和报告闭环。

## 2. 用户已确认的决策

| 维度 | 决策 |
|---|---|
| 发压拓扑 | 多 Worker 分布式；多个发压节点共同连接同一目标服/房间 |
| 登录认证 | 离线测试环境，使用唯一 Bot 昵称；不建设 Microsoft 账号池 |
| 场景组织 | 可配置 cohort 比例；允许 100% 同一流程和同步屏障 |
| 成功真源 | ServerProbe + 塔防插件测试事件；正式判定不解析聊天文本 |
| 负载模式 | 固定 500+ 长稳、阶梯升压、突发洪峰全部支持 |
| 默认通过阈值 | 60 分钟；在线/进房/抵达/攻击成功率均 ≥99%；TPS≥19；MSPT p95≤50ms；无进程崩溃 |
| 产品边界 | 通用场景引擎 + 塔防预设；兼容代理跨服和单服多房间 |
| 首版动作 | 登录、主城行走、命令进房、同步开局、抵达、锁敌持续平A、死亡重生/重进 |
| 范围外 | 技能、放塔、商店、复杂背包、定时压测、CI 门禁、云资源自动扩缩容 |
| 运行入口 | 手动运行 + 可复用模板 |

## 3. 不可违反的架构约束

1. 不新增第四类常驻进程。
2. Control Plane 是唯一浏览器 HTTP/SSE 入口和唯一数据库读写方。
3. Control Plane 只经 gRPC 委托 Worker，不直接管理 Bot Worker 子进程。
4. Worker 不访问数据库；Bot Worker 不访问数据库、HTTP 或 gRPC。
5. Worker ↔ Bot Worker 继续使用 stdin/stdout JSON 行协议。
6. ServerProbe 只经本机 `/ws/plugin-bridge` 连接 Worker，不直连 Control Plane。
7. 目标游戏实例与 Bot 执行节点可以不同，但资源权限仍以目标实例为准。
8. 单 Worker 默认容量仍为 50；首版以分布式扩容，不把常量直接放大为 500。
9. 浏览器只订阅会话级聚合流；不得为 500 个 Bot 常驻 500 条 SSE。
10. 既有单 Bot API、50 Bot 会话和无编排模式保持向后兼容。

## 4. 总体链路

```text
浏览器
  │ HTTP + 会话级 SSE
  ▼
Control Plane
  ├─ 模板与运行账本
  ├─ 发压节点容量目录
  ├─ 分片/批次/负载状态机
  ├─ 场景外部事件关联器
  ├─ 指标聚合与 verdict
  └─ 目标实例权限与审计
       │ gRPC（隧道优先/直拨回退）
       ├──────────────┬──────────────┬──────────────┐
       ▼              ▼              ▼              ▼
   Worker A       Worker B       Worker C       Worker N
       │ IPC          │ IPC           │ IPC          │ IPC
       ▼              ▼               ▼              ▼
  Bot Worker A   Bot Worker B    Bot Worker C   Bot Worker N
       └────────────── Mineflayer 连接目标服 ──────────────┘

目标实例/代理
  └─ ServerProbe ← 塔防插件 LoadTestEventPublisher
       │ 反向 WS
       ▼
   目标实例所在 Worker → gRPC PluginEvent → Control Plane
                                             │ 关联成功
                                             ▼
                        gRPC SignalBotActions → 执行节点 Worker → IPC
```

关键点：产生游戏事实的 Worker 与执行 Bot 的 Worker 可能不同。Control Plane 负责将目标实例探针事件关联到 Bot 当前等待步骤，再把成功/失败信号路由到 Bot 的执行 Worker。

## 5. 领域术语与稳定标识

| 名称 | 含义 | 格式/来源 |
|---|---|---|
| templateId | 可复用压测模板 | DB 数字 ID + UUID |
| runId | 一次不可变压测运行；沿用 BotStressSession 记录 | `bot_stress_sessions.uuid` |
| cohortKey | 运行内 Bot 分组 | 用户定义小写 key，运行内唯一 |
| batchId | 某执行 Worker 上的一批 Bot | UUID |
| botId | Bot 运行身份 | 既有 `bots.uuid` |
| generation | Bot desired-state 版本 | int64，任何期望配置/状态变更递增 |
| stepId | 场景步骤稳定标识 | 模板内唯一字符串 |
| actionRunId | 某 Bot 某次步骤尝试 | UUID |
| correlationToken | 目标服事件与 Bot 动作关联令牌 | 每次动作随机 UUID，不含凭据 |
| eventId | 探针/内部标准事件幂等键 | 不透明 canonical key，≤128；塔防插件使用 UUID，cross_server 标准化使用固定 namespace UUIDv5 |
| eventSeq | Bot Worker 事件单调序号 | 每子进程启动 epoch 内 int64 |
| workerEpoch | Bot Worker 进程启动标识 | 每次子进程启动 UUID，仅诊断 |
| workerEpochGeneration | 当前 Worker 进程内可比较的 Bot Worker 子进程世代 | int64，每次拉起子进程递增；Worker 进程重启可重置，CP 通过每节点本地 fleetSubscriptionGeneration 丢弃旧 gRPC 流，并用新 snapshot 建立基线 |

禁止仅以显示名称、列表顺序或时间戳作为幂等键。

## 6. 最终数据模型与 FR 所有权

为避免并行 FR 重复迁移，本超级规格冻结最终模型。各 FR 只能实现分配给自己的字段/表。

### 6.1 `bots` 增量字段

| 字段 | 类型 | 所属 FR | 语义 |
|---|---|---|---|
| executor_node_id | uint nullable + index | FR-351 | 实际执行 Bot 的 Worker 节点；空时兼容为目标实例节点 |
| load_batch_id | uint nullable + index | FR-351 | 所属分片批次 |
| cohort_key | varchar(64) | FR-352 | 所属场景分组 |
| desired_state | varchar(16) default stopped | FR-354 | running/stopped |
| generation | bigint default 1 | FR-354 | desired-state 版本 |
| worker_epoch | varchar(36) | FR-354 | 最近状态来源的子进程启动 UUID，仅用于诊断 |
| worker_epoch_generation | bigint default 0 | FR-354 | Worker 维护的可比较子进程世代；只接受更高世代 |
| config_hash | char(64) | FR-354 | assignment 规范 JSON 的 sha256，用于 reconcile |
| last_event_seq | bigint default 0 | FR-354 | 当前 epoch generation 内最近接收事件序号 |
| last_seen_at | datetime nullable | FR-354 | 最近可信状态时间 |
| connected_at | datetime nullable | FR-355 | 最近一次连接完成时间 |
| reconnect_count | int default 0 | FR-354 | 本次运行累计重连次数 |

`InstanceID` 继续表示目标游戏实例和权限归属，不改语义。既有 `WorkerID` 保留兼容展示，写入执行节点 UUID；新代码以 `ExecutorNodeID` 关系作为路由真源。

### 6.2 `bot_stress_sessions` 增量字段

保持现有表名与 API 路径，外部 UI 可显示为“压测运行”，不做破坏性改名。

| 字段 | 类型 | 所属 FR | 语义 |
|---|---|---|---|
| template_id | uint nullable + index | FR-355 | 来源模板；临时运行为空 |
| scenario_snapshot | longtext JSON | FR-352 | 启动时冻结的 V2 场景 |
| load_profile | longtext JSON | FR-355 | stable/step/spike 配置 |
| thresholds | longtext JSON | FR-355 | verdict 与安全停止阈值 |
| allocation_plan | longtext JSON | FR-351 | 预检通过后冻结的节点分片计划 |
| run_state | varchar(32) + index | FR-355 | 细粒度状态机；旧 status 作为兼容映射 |
| current_stage | int default 0 | FR-355 | 当前负载阶段 |
| verdict | varchar(16) | FR-355 | pending/passed/failed/aborted |
| max_stable_bots | int default 0 | FR-355 | 阶梯模式结果 |
| failure_summary | longtext JSON | FR-355 | 聚合错误分类 |
| report_summary | longtext JSON | FR-355 | 最终报告摘要 |

旧 `Status` 保留并由 `RunState` 映射：pending/preflighting/ready→pending；starting/running/degraded/stopping/cancelling→running；completed/cancelled→stopped；failed→error。旧客户端继续可读。`RunState` 与兼容 `Status` 必须在同一事务更新，升级中断后由启动修复逻辑重新映射。

### 6.3 新表 `bot_load_batches`（FR-351）

```text
id, uuid(unique), stress_session_id(index), executor_node_id(index), ordinal,
planned_count, accepted_count, connected_count, failed_count,
state(planned/dispatching/running/stopped/failed),
idempotency_key(unique), connect_start_at, connect_interval_ms,
last_error, started_at, ended_at, created_at, updated_at
```

唯一约束：`(stress_session_id, ordinal)`；`idempotency_key` 全局唯一。删除会话时级联删除批次；删除节点前已有节点守卫处理引用。

### 6.4 新表 `bot_load_templates`（FR-355）

```text
id, uuid(unique), name(unique active), description, scenario_json,
load_profile_json, thresholds_json, tags_json, created_by,
created_at, updated_at, deleted_at
```

模板不保存目标实例、发压节点选择或账号凭据。运行创建时选择环境并冻结快照。

### 6.5 新表 `bot_load_action_results`（FR-352）

```text
id, stress_session_id(index), bot_id(index), cohort_key, step_id,
action_run_id(unique), attempt, status(running/succeeded/failed/timed_out/cancelled),
error_code, message, duration_ms, correlation_token(index),
started_at, ended_at, result_json
```

只记录步骤开始和终态，不逐条保存每次挥击。高频伤害/击杀归聚合计数与指标样本。

### 6.6 新表 `bot_load_probe_events`（FR-353）

```text
id, event_id(unique), stress_session_id(index), bot_id(index nullable),
instance_id(index), event_type(index), correlation_token(index),
player_name, player_uuid, room_id, server_name, area_id,
occurred_at, received_at, payload_json,
match_state(unmatched/matched/consumed/late/invalid), unmatched_reason,
consumed_action_run_id(unique nullable), consumed_at, late,
created_at
```

仅持久化 `domain=bot_load` 事件；按 event_id 去重。事件消费使用条件更新：仅 `consumed_action_run_id IS NULL` 的匹配行可绑定一个 actionRunId；唯一索引阻止一条事件被多个动作消费。默认保留 7 天，运行报告生成后允许后台清理；本 FR 不做配置化保留策略。

### 6.7 新表 `bot_load_metric_samples`（FR-355）

```text
id, stress_session_id(index), sampled_at(index), stage_index,
bot_counts_json, action_counts_json, executor_metrics_json,
target_metrics_json, latency_json, error_counts_json
```

每个运行默认 5 秒一行，不按 Bot 逐行写时序。唯一约束 `(stress_session_id, sampled_at)`。默认保留 30 天；报告摘要长期保存在会话记录。

### 6.8 新表 `bot_load_run_events`（FR-355）

```text
id, event_id(unique), stress_session_id(index), event_type(index),
bot_id(index nullable), executor_node_id(index nullable), action_run_id(index nullable),
step_id, stage_index, severity, occurred_at(index), payload_json, created_at
```

append-only 持久化关键 run-state/stage/barrier/executor crash/safety stop/report-ready 事件；不写每次普通攻击。默认保留 30 天，ReportSummary 长期保存。`GET .../events` 将 RunEvent、ActionResult、ProbeEvent 投影成统一历史响应。

## 7. 共享状态机

### 7.1 运行状态机

```text
pending
  → preflighting → ready
  → starting → running ↔ degraded
  → stopping → completed
  → cancelling → cancelled
  → failed
```

规则：

- 只有 `pending/ready/completed/failed/cancelled` 可重新预检。
- 只有 `ready` 可启动；启动时再次快速校验节点在线和容量 generation。
- `running/degraded` 可 stop 或 cancel。
- stop：有序停止并生成报告，终态 completed。
- cancel：尽快停止，终态 cancelled，报告标记未完整。
- 安全阈值命中走 stopping，verdict=aborted，不直接 failed。
- 未处理异常导致无法收束才进入 failed。

### 7.2 批次状态机

```text
planned → dispatching → running → stopped
                    ↘ failed
```

批次 `accepted_count` 表示 Worker 接受 desired assignment，不等于 connected；连接成功只计 `connected_count`。

### 7.3 Bot desired/runtime 状态

- desired_state：running/stopped，由 CP 持久化；runtime status 由 Worker/Bot Worker 上报。
- CP 每节点维护本地 `fleetSubscriptionGeneration`；旧 gRPC 流 handler 的迟到事件先丢弃。
- 新订阅必须先用 FleetSnapshot 建 baseline；只有 baseline 可在 Worker 进程重启后重置 workerEpochGeneration。
- desired generation 小于 DB 值丢弃；大于 DB 值触发 snapshot/reconcile，不直接覆盖 desired。
- baseline 后，workerEpochGeneration 较小丢弃；相同世代 eventSeq 必须严格递增；较大世代表示同 Worker 进程内新 Bot Worker 子进程。
- workerEpoch UUID 仅用于诊断，不参与排序。
- 同 desired generation 下 configHash 不一致时不接受为健康状态，触发 reconcile。
- 超过状态新鲜度窗口仍未见 Bot，CP 收敛为 disconnected，而不是保留 connected。

### 7.4 动作状态机

```text
pending → running → succeeded
                  ↘ failed
                  ↘ timed_out
                  ↘ cancelled
```

每个动作定义 `timeoutMs`、`maxAttempts`、`retryBackoffMs`、`resumePolicy`。默认 `resumePolicy=restart_step`。

## 8. 场景 V2 契约

场景以 JSON 为内部规范格式，YAML 只作为等价编辑/导入格式；Control Plane 单点解析和校验，Worker/Bot Worker 不解析 YAML。

```yaml
version: 2
seed: 20260718
cohorts:
  - key: lobby
    percent: 20
    steps:
      - id: spawn
        type: wait_spawn
        timeoutMs: 30000
      - id: roam
        type: roam_in_area
        observationStep: true
        durationMs: 3600000
        area:
          type: radius
          center: { x: 0, y: 64, z: 0 }
          radius: 30
        pauseMs: { min: 500, max: 3000 }
  - key: combat
    percent: 80
    steps:
      - id: spawn
        type: wait_spawn
        timeoutMs: 30000
      - id: join
        type: send_command
        command: "/tower join test {{correlationToken}}"
      - id: joined
        type: wait_probe_event
        event: room_joined
        timeoutMs: 30000
      - id: ready
        type: barrier
        key: combat-ready
        release:
          type: percent
          value: 99
        timeoutMs: 60000
      - id: game-start
        type: wait_probe_event
        event: game_started
        timeoutMs: 60000
      - id: move
        type: move_to_and_wait
        pos: { x: 100, y: 65, z: 100 }
        radius: 2
        areaId: combat-zone-a
        requireProbeEvent: area_arrived
        timeoutMs: 45000
      - id: attack
        type: attack_until
        observationStep: true
        selector:
          kind: hostile
          types: [zombie, skeleton]
          radius: 16
        stop:
          durationMs: 3600000
          successPolicy: all
          evidenceWindowMs: 30000
          minDamageEventsPerWindow: 1
        attackIntervalMs: 600
        chase: true
```

### 8.1 支持动作

| type | 执行方 | 终态条件 |
|---|---|---|
| wait_spawn | Bot Worker | Mineflayer spawn 完成 |
| roam_in_area | Bot Worker | 持续到 duration；路径失败超阈值则失败 |
| send_command | Bot Worker | chat 调用成功；业务成功由后续探针步骤确认 |
| wait_probe_event | CP 关联器 + Bot Worker 等待态 | 收到匹配 token/run/bot/event 的信号 |
| barrier | CP 协调器 | 到达数量/比例/全部条件并统一释放 |
| move_to_and_wait | Bot Worker + 可选探针确认 | 本地进入目标半径且稳定 500ms；配置 requireProbeEvent 时还必须收到同 token/areaId 的可信事件 |
| find_entity | Bot Worker | 按 selector 锁定实体 |
| attack_until | Bot Worker + 探针可信计数 | 满足可信伤害/击杀/探针条件；仅 duration 到时不得计正式攻击成功 |
| wait | Bot Worker | 时间到 |
| respawn_and_rejoin | Bot Worker | 重生并执行引用的入口步骤成功 |

### 8.2 模板变量

仅支持白名单变量：

- `{{botName}}`
- `{{botUuid}}`
- `{{runId}}`
- `{{cohortKey}}`
- `{{correlationToken}}`
- `{{roomKey}}`

禁止任意表达式、文件读取、环境变量和脚本执行。

### 8.3 分组分配

- 按 Bot 稳定序号和 seed 计算 cohort，不用随机运行时抽签。
- 百分比总和必须等于 100。
- 余数按 cohort 声明顺序分配，确保目标总数精确。
- 运行开始后 cohort 不变；失败重试仍回原 cohort。

## 9. 跨进程协议冻结

完整 HTTP 契约见同目录 `api.md`。

### 9.1 gRPC 增量

在 `WorkerService` 加性新增：

```proto
rpc GetBotCapacity(GetBotCapacityRequest) returns (GetBotCapacityResponse);
rpc ApplyBotBatch(ApplyBotBatchRequest) returns (ApplyBotBatchResponse);
rpc GetBotFleetSnapshot(GetBotFleetSnapshotRequest) returns (GetBotFleetSnapshotResponse);
rpc StreamBotFleetEvents(StreamBotFleetEventsRequest) returns (stream BotFleetEvent);
rpc SignalBotActions(SignalBotActionsRequest) returns (SignalBotActionsResponse);
```

约束：

- `ApplyBotBatch` 单请求最大 50 个 assignment；超出 `InvalidArgument`。
- `idempotency_key` 重放返回第一次结果，不重复创建连接。
- `SignalBotActions` 可一次投递最多 100 个外部事件/屏障信号，并返回逐信号 accepted/skipped/error。
- `StreamBotFleetEvents` 是 CP 持续状态真源：活动运行按执行节点建立一条流，断流后先拉 FleetSnapshot 再重连。
- 所有 RPC 沿用池的隧道优先/直拨回退，不绕开连接池。
- 既有 CreateBot/DeleteBot/SetBotBehavior/SendBotCommand 保留；所有既有 Bot 操作统一按 `ExecutorNodeID != nil ? ExecutorNodeID : Instance.NodeID` 路由。

核心 message 语义：

```text
BotAssignment:
  bot_uuid, instance_uuid, session_uuid, generation, desired_state,
  config_hash, name, host, port, username, version, auth,
  cohort_key, scenario_json, resume_step_id,
  connect_not_before_unix_ms, correlation_seed

BotRuntimeSnapshot:
  bot_uuid, session_uuid, generation, config_hash,
  worker_epoch, worker_epoch_generation, event_seq,
  status, current_step_id, health, food, pos,
  reconnect_count, error_code, last_error, observed_at_unix_ms

BotActionEvent:
  bot_uuid, session_uuid, generation,
  action_run_id, step_id, attempt,
  status(running/succeeded/failed/timed_out/cancelled),
  error_code, message, correlation_token,
  result_json, duration_ms, observed_at_unix_ms

BotFleetEvent:
  oneof runtime_snapshot | action_event
```

`StreamBotFleetEvents` 对同一执行节点使用单条类型化流承载 runtime/action；禁止把新动作结果塞回旧 BotEvent 自由 JSON 作为正式契约。

### 9.2 IPC 增量

所有需要同步确认的 IPC 命令都必须携带唯一 `requestId`；Worker Manager 维护 requestId→等待者，超时后移除，Node 的迟到结果只记录不复活等待者。

Go → Node.js：

- 扩展 `create-bots`：命令增 `requestId/batchId/idempotencyKey`；每 Bot 增 `sessionId/generation/configHash/cohortKey/scenario/resumeStepId/connectNotBefore`。
- 扩展 `stop-bots`：增 `requestId/generation/reason`，旧形态兼容。
- 新增 `signal-actions`：增 `requestId`，批量投递 probe/barrier/cancel 信号。
- 新增 `get-fleet-snapshot`：增 `requestId`，Worker 主动拉当前 Bot 快照。

Node.js → Go：

- `worker-ready` 增 `workerEpoch/workerEpochGeneration/maxBots/features`。
- `heartbeat` 增 `activeBots/connectingBots/rssBytes/eventLoopP95Ms/droppedEvents/capacityGeneration`。
- `bot-state` 增 `sessionId/generation/configHash/workerEpoch/workerEpochGeneration/eventSeq/currentStepId/reconnectCount`。
- 新增 `batch-result`：关联 requestId/batchId/idempotencyKey，逐 Bot 返回 accepted/skipped/errorCode/error。
- 新增 `signal-result`：关联 requestId，逐信号返回 accepted/skipped/errorCode/error。
- 新增 `action-event`：动作开始/终态和结构化结果。
- 新增 `fleet-snapshot-result`：关联 requestId，返回当前全部 Bot 运行快照。

所有消息必须向后兼容未知字段；Worker 遇到老 bot-worker 不支持 fleet 特性时容量标记 `legacy=true`，分布式运行预检不选择该节点。

### 9.3 ServerProbe 事件

复用既有 `PluginEvent`，不新增 proto 字段：

- `domain = "bot_load"`
- `type = 具体事件类型`
- `dedup_key = eventId`
- `request_id = correlationToken`
- `player_name/player_uuid/server/instance_uuid` 使用既有字段
- `raw_json` 承载版本化载荷

`raw_json` 公共信封：

```json
{
  "schemaVersion": 1,
  "source": "tower_plugin",
  "eventId": "uuid-or-canonical-key",
  "runId": "uuid",
  "correlationToken": "uuid",
  "occurredAtUnixMs": 1784366400000,
  "roomId": "room-1",
  "areaId": "combat-zone-a",
  "gameId": "game-1",
  "wave": 3,
  "targetType": "zombie",
  "targetId": "entity-uuid",
  "damage": 6.0,
  "result": "win",
  "data": {}
}
```

## 10. 探针事件与动作关联

1. `send_command` 创建 correlationToken，并按模板变量放入塔防测试命令。
2. 塔防插件接收 token，关联玩家/房间/运行并发布事件。
3. ServerProbe 将事件作为 `domain=bot_load` 上报。
4. CP 以 eventId 去重，以 runId+correlationToken+player 映射 Bot 和等待动作。
5. 匹配成功后持久化 probe event/action result，并按 executorNodeID 调用 `SignalBotActions`。
6. Bot Worker 收到信号后完成 `wait_probe_event` 或为 `attack_until` 累加可信伤害/击杀计数。
7. 未匹配事件记录为诊断事件，但不推动动作。

### 10.1 关键事件可靠投递

- ServerProbe 端保留最近 30 秒/最多 4096 条 `domain=bot_load` 事件；重连本机 Worker 后按原 eventId 重发。
- Worker 为 bot_load 建独立 30 秒/4096 条重放缓冲。新的 `StreamPluginEvents` 订阅建立时，先重放缓冲再接实时流；重复由 CP eventId 去重，无需新增 proto checkpoint 字段。
- bot_load 订阅队列满时不得静默丢关键事件：关闭该慢流并记录中文 WARN，CP 重连后从缓冲重放。普通玩家/遥测事件仍沿既有 best-effort 语义。
- `damage_dealt` 可由 ServerProbe 按 player+actionRunId 在 250ms 内合并为累计增量，降低洪峰；room/game/kill/death/respawn/end 不采样。
- 事件事实时间取 raw_json.occurredAtUnixMs，并同步写 PluginEvent.timestamp；Worker/CP 接收时间仅用于诊断。

## 11. 负载模式

### 11.1 stable

```json
{"type":"stable","targetBots":500,"rampUpSeconds":120,"durationSeconds":3600}
```

### 11.2 step

```json
{
  "type":"step",
  "stages":[
    {"targetBots":100,"holdSeconds":600},
    {"targetBots":250,"holdSeconds":600},
    {"targetBots":500,"holdSeconds":900},
    {"targetBots":750,"holdSeconds":900}
  ],
  "stopOnThresholdFailure":true
}
```

### 11.3 spike

```json
{
  "type":"spike",
  "targetBots":500,
  "connectWindowSeconds":10,
  "barrierKey":"combat-ready",
  "releaseWindowMs":1000,
  "holdSeconds":600
}
```

连接限流由负载计划生成 `connectNotBefore`；场景 `staggerMs` 不再承担连接限流语义。

## 12. 指标与判定

### 12.1 Bot/动作

- planned/accepted/connecting/connected/disconnected/error/stopped
- connect latency p50/p95/p99
- spawn latency p50/p95/p99
- reconnect count/rate
- 各 step 成功/失败/超时/取消
- room_joined / game_started / arrived / damage / kill 成功率

### 12.2 发压端

- 每 Worker planned/active/connecting Bot 数
- bot-worker RSS、CPU（可取得时）、事件循环延迟 p95、掉事件数
- Worker 节点 CPU/内存/网络
- gRPC/IPC 批次延迟和失败数

### 12.3 目标服

- TPS、MSPT p95、在线玩家、CPU、内存、网络。
- `MSPT p95` 的唯一真源是 ServerProbe 直接基于最近 60 秒原始 Tick 时长计算并暴露 `serverprobe_mspt_seconds{quantile="p95",window="60s"}`；Worker 增 `MSPTP95Millis` 解析和 additive proto/心跳字段，CP 原样入样本。禁止对现有 MSPT avg 的 5 秒样本再次取 p95 冒充 Tick p95。
- 老探针没有 p95 时字段为 null；要求 maxMsptP95 的严格运行进入 degraded，连续缺失超过 60 秒判 PROBE_METRICS_MISSING，不得用 0 或 avg 代替。
- 房间/区域/波次/伤害/击杀等探针聚合。

### 12.4 默认严格阈值

```json
{
  "minOnlineRate":0.99,
  "minRoomJoinRate":0.99,
  "minArrivalRate":0.99,
  "minAttackSuccessRate":0.99,
  "minTps":19.0,
  "maxMsptP95":50.0,
  "maxProcessCrashes":0
}
```

安全停止阈值与 verdict 阈值分开，默认：TPS<10 持续 30 秒、MSPT p95>100 持续 30 秒、目标服不可达 30 秒、任一发压节点 RSS 超节点内存 85% 或事件循环 p95>500ms 持续 30 秒。安全停止终态 completed + verdict=aborted。

### 12.5 严格 60 分钟判定数学定义

- `runMaxTargetBots` 在运行开始时冻结，等于 profile 最大目标数，用于容量预检和完整报告。
- 每个 stage 进入时冻结 `stageTargetBots` 与 `stageExpectedBotSet`；stable/spike 通常等于 runMaxTargetBots，step 等于当前档目标（100/250/500…）。失败、停止、重分配不得缩小当前 stage 分母。
- 每个断言步骤在 stage 进入时从 stageExpectedBotSet 冻结自己的 `expectedBotSet`：只包含场景中声明该可信断言的 Bot；未抵达步骤、断线、超时者在窗口结束时均计失败，不从分母移除。
- 场景中每个 cohort 必须恰有一个 `observationStep:true` 的持续动作（塔防预设为 lobby.roam 与 combat.attack）。
- stable 预热完成条件：connected/stageTargetBots ≥99%，且每个 cohort 中进入 observationStep 的 Bot/该 cohort stageExpectedBotSet ≥99%，两者连续 60 秒；必须在 ramp 结束后 10 分钟内达到，否则失败且不进入观察窗口。step 每级差量 Bot 派发后按当前 stageTargetBots 做同样预热。
- stable 观察窗口从预热完成时刻起连续 3600 秒；step 观察窗口等于该级 hold；每 5 秒一个有效样本。
- 在线通过：观察窗口内每个有效样本 `connected/stageTargetBots ≥ minOnlineRate`；使用最小样本率，不使用累计平均掩盖短时掉线。
- 房间/抵达/攻击通过：窗口结束时 `可信 succeeded / 冻结 expectedBotSet ≥ 对应阈值`；攻击成功只能由 damageAtLeast/killsAtLeast/probeEvent 满足，clientAttackAttempts 不计成功。
- TPS 通过：观察窗口内所有有效 1m TPS 样本的最小值 ≥ minTps。
- MSPT 通过：观察窗口内 ServerProbe 直接报告的 60s Tick MSPT p95 最大值 ≤ maxMsptP95。
- 指标样本覆盖率必须 ≥99%，任一连续缺样 >30 秒失败；探针 p95 连续缺失 >60 秒失败。
- 任一 Bot Worker/Worker/目标服非预期 crash 计 1 次；人工 stop、计划内重启不计 crash。
- step 每一级使用相同公式，但观察窗口为该级 hold；spike 另评连接/屏障/攻击开始延迟，不放宽可信动作成功分母。
- 所有公式用固定测试向量锁定，前端只展示后端结果，不自行重算。

### 12.6 场景与阈值交叉校验

- thresholds.minArrivalRate>0 时，所有计入 arrival expectedBotSet 的 move_to_and_wait 必须配置 `requireProbeEvent=area_arrived` 和 areaId；否则 preflight 422。
- thresholds.minAttackSuccessRate>0 时，attack_until.stop 必须包含可信条件（damageAtLeast、killsAtLeast、probeEvent 或 minDamageEventsPerWindow）；durationMs 只是截止/覆盖时长，绝不单独成功。严格长稳预设必须 `successPolicy=all` 且 duration 覆盖完整观察窗，并要求每个 evidenceWindow 都有可信伤害/击杀证据。

## 13. 前端冻结边界

### 13.1 路由

- `/bots?tab=fleet`
- `/bots?tab=sessions`
- `/bots?tab=templates`
- `/bots/sessions/:id?tab=overview|bots|metrics|failures|events|config`

### 13.2 创建向导

固定五步：目标与节点池 → 连接 → cohort/场景 → 负载模式 → 阈值与预检。只有 preflight `ready=true` 才可启动。

### 13.3 实时更新

- 会话级 SSE 单连接。
- Bot 明细、失败明细和历史指标走分页/区间 HTTP。
- 前端隐藏/离开详情时断开 SSE；回到页面后先 GET 快照再续流。
- SSE 支持 Last-Event-ID；断线后丢失的聚合状态通过快照补齐。

### 13.4 大数据纪律

- Bot 列表不一次请求超过 100 条。
- 5000 Mock Bot 下使用分页或虚拟化，DOM 行数受控。
- 图表服务端下采样，前端单序列点数默认 ≤1200。

## 14. 权限、安全与审计

- 所有读取：`bot:read`，且目标实例必须可访问。
- 模板写、运行预检/启动/停止/重试：`bot:manage`，且目标实例必须可管理。
- 发压节点容量列表只返回用户有权用于目标实例压测的节点摘要；平台管理员全量，非平台管理员按既有节点/实例授权收敛。
- 离线认证仅允许在明确标记的压测模板/运行中使用；不保存 Microsoft token。
- correlationToken 是关联标识，不是认证凭据。
- 操作审计：template create/update/delete、run create/preflight/start/stop/cancel/retry、report export。
- 报告和日志不得输出节点 secret、JWT、账号 token、完整内部错误堆栈。

## 15. 向后兼容

1. 既有 `/bots/stress-sessions` 请求不带 V2 字段时继续生成单 cohort 兼容场景。
2. 旧无编排会话继续使用 `behavior`。
3. 旧 `orchestrationYaml` 作为 V1 导入源；创建时可转换为 V2 snapshot，原文仍保留。
4. 单 Bot 创建默认执行节点=目标实例节点。
5. 旧 Worker/bot-worker 可继续单 Bot 管理，但不得进入 500+ 分布式预检节点池。
6. 数据库只做加性 AutoMigrate，不删除或重命名旧列/表；FR-354 对历史 Bot 做幂等 desired-state/generation backfill，不能让新增默认 stopped 误停活动 Bot。
7. 前端旧 `/bots` 深链继续有效，默认 tab=fleet。

## 16. 跨 FR 文件所有权

| 文件/区域 | 首要所有者 | 其他 FR 规则 |
|---|---|---|
| `proto/worker.proto` fleet RPC/消息与 MSPT p95 additive 字段 | FR-351 | FR-351 一次铺齐；FR-353 只把预铺字段接真，FR-354 不改 proto |
| Worker Bot IPC 通用 request/result 层 | FR-351 | FR-352/353/354 只消费 requestId/result 契约 |
| `internal/controlplane/model/bot.go` | FR-351/354/355 按字段表分段 | 只加自己字段，不格式化他人段落 |
| 场景 V2 parser/schema | FR-352 | FR-356 只消费 API 类型 |
| `domain=bot_load` 事件 ingest/重放 | FR-353 | FR-355 只读聚合结果 |
| reconcile/generation | FR-354 | FR-351 不实现恢复策略 |
| run state/metrics/verdict/template、共享 `api/botLoad.ts` 类型、devmock 运行状态模型 | FR-355 | FR-356/357 只追加自己 UI 所需 hook/handler，不改共享语义 |
| 创建向导与模板 UI/devmock | FR-356 | FR-357 不修改向导流程；i18n 使用 `botLoad.wizard.*` namespace |
| 会话详情观测与 SSE devmock | FR-357 | FR-356 只提供启动后跳转；i18n 使用 `botLoad.session.*` namespace |
| 前端路由入口 | FR-356 | FR-357 只新增 session detail 子路由，避免重写 `/bots` 壳 |
| `docs/PRD.md`、`docs/ARCHITECTURE.md`、`docs/API.md`、ADR 索引 | 协调分支统一回写 | 并行 FR 只在本分支准备片段/证据，不并发改共享段；整合后主控统一追平 |
| `CHANGELOG.md` | 各 FR 末尾追加 | 不重排其他条目 |

## 17. 环境就绪门与统一测试战略

### 17.0 真机环境就绪门

开始声称任一 FR“真机完成”前，必须在 `.tmp/bot-load-acceptance/environment.json`（不入库）登记并探测：

- 10+ Worker 的 nodeId/版本/隧道状态/Node 运行时/bot-worker feature/maxBots/可用容量。
- 每节点 mineflayer 与 mineflayer-pathfinder 已安装且 bot-worker 自检通过。
- 目标服地址、离线认证、代理跨服与单服房间测试入口。
- ServerProbe commit/JAR/连接状态及 MSPT p95 metric 可用。
- 塔防插件仓路径、适配 commit SHA、测试命令、room/area/monster 配置。
- 所有主机 NTP 偏差≤250ms。
- 证据输出目录和测试负责人。

缺任一项，对应 FR 最多标 `partial/待真机`，不得标 done。代码自动化完成与真机完成分开记录。

### 17.1 自动化

- Go：service/state machine/protocol 表驱动测试；DB CRUD；bufconn gRPC；相关包 `go test -race`。
- Bot Worker：Node test 覆盖 schema、动作、重连、事件序号、信号关联、定时器清理。
- 前端：Vitest DOM + MSW 大数据；Playwright 场景；a11y/i18n/双主题。
- Fake MC：扩展最小测试服，覆盖登录、spawn、kick、断线；真实寻路和伤害不以 fake server 代替。
- ServerProbe：事件信封、去重键、断线重发、平台适配测试。

### 17.2 真机

- 唯一 acceptance 入口固定为：`JM_BOT_LOAD_ACCEPTANCE=1 JM_BOT_LOAD_ENV=.tmp/bot-load-acceptance/environment.json go test -tags=botloadacceptance ./internal/e2e -run '^TestBotLoadAcceptance$' -count=1 -timeout=4h`。
- 未设置 `JM_BOT_LOAD_ACCEPTANCE=1` 时测试显式 Skip；已启用但环境文件缺失/字段不全/探测失败时必须 Fail（机器可读状态 blocked），不得静默 Skip。
- `environment.json` 固定 `schemaVersion:1`，包含 controlPlaneBaseUrl、凭据环境变量名（不存明文）、targetInstanceId、workerNodeIds(≥10)、proxy/single-server 场景参数、tower adapter/area/command、预期 ServerProbe/Worker 版本和 evidenceDir。
- harness 自动做环境探测、依次运行 stable/step/spike、故障注入、轮询、下载报告，并写 `.tmp/bot-load-acceptance/<timestamp>/result.json`（status=passed|failed|blocked）、`environment-check.json`、`report-*.json/csv`、`timeline.ndjson`；禁止用 fake 代替。
- 至少 10 个可用 Worker、每个默认容量 50。
- 真实 CP + Worker + bot-worker + MC 目标服 + ServerProbe + 塔防插件适配。
- 500 Bot 固定 60 分钟严格阈值。
- 阶梯模式跑到首次阈值失败，验证最大稳定并发。
- 登录、开局、攻击三类突发洪峰。
- 批量踢 Bot、杀 bot-worker、重启 Worker、短时停目标服，验证恢复和无风暴。
- 代理跨服和单服房间各至少一套场景。
- 真浏览器完成模板→预检→启动→实时观测→诊断→报告。

## 18. 实施依赖

```text
FR-351 → FR-352 → (FR-353 ∥ FR-354) → FR-355 → (FR-356 ∥ FR-357)
```

所有子规格必须引用本超级规格，不得复制后修改共享契约。若实现发现共享契约必须变更，停止对应并行批次，由协调分支修改超级规格并重新审核受影响 FR。

## 19. 明确不做

- 不新增 Bot 微服务或消息队列。
- 不让 Control Plane 直接 spawn Node.js。
- 不让 ServerProbe 直连 CP。
- 不简单把 `maxBots=50` 改成 500。
- 不以 DB Bot 行数代替真实在线人数验收。
- 不以 command-sent、setGoal 或 bot.attack 调用代替服务端动作成功。
- 不保存每次普通攻击的高频原始事件；只保存步骤结果与聚合指标。
- 不引入新的第三方依赖；如实现确需依赖，必须另行向用户说明并获得确认。
