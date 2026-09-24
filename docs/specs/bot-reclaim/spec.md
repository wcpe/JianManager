# 失效 Bot 自动回收与容量补足（FR-460）

> 状态：📋 计划　·　关联 PRD：FR-460（增强 FR-398）　·　依赖：FR-365（世代归真）、FR-326（无主运行时处置范式）、FR-351（Bot 分布式压测执行）　·　关联 ADR：ADR-013（时序）、ADR-074（执行节点解析）

## 1. 背景与目标

**现状痛点**（来自 FR-438 收尾实测）：

- bot-worker 分片重启后，`workerEpoch` 换代；旧世代 Bot 的事件被 `bot_fleet_runtime.go` 的 `classifyRuntimeEpoch` 判为 `BotFleetRuntimeIgnoredStaleEpoch` 而**丢弃**，CP 永远不会再推进它们的 `status`。
- 这些 Bot 停在 `desiredState=running` / `status=error`，但 Worker `Manager` 的 desired 集合仍把它们算作在用（`worker/bot/manager.go` 的 `m.capacity.ActiveBots`），于是平台侧「`loadtest_node_capacity` 报 `activeBots=500`，真实连接只有 482」——**18 个僵死 Bot 占容量记账却不产生连接**。
- 平台**无回收能力**：`Grep ReclaimBots|PurgeBots|ReapBots|StaleBots` 零命中；`bot_freshness.go` 的 `MarkStale/MarkRuntimeMissing/MarkExecutorOffline` 只**收敛状态**（→`disconnected`/`error`），从不删除/停用；删除只有单条 `DELETE /bots/:id`（`service/bot.go` → `delegateDeleteBot` → `Worker.DeleteBot`）。

**目标**

- **自动回收**：按 `workerEpoch` 不匹配 + 状态判据周期识别失效 Bot，宽限后对其下发停用/删除，使 Worker desired 集合与 CP 账本同时去账。
- **容量自动补足**：回收后按 `BotLoadBatch.planned_count` 目标把实际在线补回目标数。
- 回收 / 补足动作**写审计**（复用 `AuditService`）。

**不做**

- 不删除用户手动创建的单条 V1 Bot（无 `loadBatchId`/`stressSessionId`）——只回收 **Fleet 归属** Bot。
- 不重做 FR-365 的状态归真语义（继续由 `BotFreshnessSweeper` 负责收敛，本 FR 只在其之上叠加「回收」）。
- 不引入新的 Worker↔bot-worker 协议；回收复用既有 `Worker.DeleteBot` / `StopBotBatch`。

## 2. 设计

### 2.1 失效判定（reclaim predicate）

一条 Bot 判为**失效**当且仅当：

1. `deleted_at IS NULL AND desired_state = running`；且
2. `status IN (error, disconnected, connecting)`（排除 `connected` 与 `stopped`）；且
3. **世代不匹配**：`worker_epoch_generation < 该执行节点最近报告的 epoch_generation`，或 `worker_epoch = ''`（分片重启前残留），或 `executor_node_id` 为空/对应节点已不存在；且
4. `last_seen_at` 早于新鲜度窗口（复用 `bot_freshness.go` 的 `botFreshnessMissingWindow = 90s` 口径）。

节点**当前 epoch 真源**取自心跳/容量快照：`BotLoadCapacityDirectory.applyWorkerCapacity`（`bot_load_capacity.go`）已把 `WorkerEpoch`/`WorkerEpochGeneration` 落到 `BotLoadNodeCapacity`，直接复用，不新增 RPC。

> **多分片世代口径（FR-460 修正）**：节点级 `WorkerEpochGeneration` 是各分片世代号的 **max**（`worker/bot/sharded.go` 聚合），而每分片 `Manager` 独立自增、Bot 记录的是其所属分片的世代号。多分片下任一分片独立崩溃自愈都会让节点 max 领先于健康 Bot 的分片世代，故「世代落后」只是**嫌疑**而非充分判据；判定还需与 **Worker Fleet 实存快照**（`GetBotFleetSnapshot`）对账：仍在实存中的 Bot 一律视为健康，只有实存中确实缺失才判失效。快照不可用时退回保守兜底（多分片不判失效、单分片比对 epoch 字符串）。

> **V1 手动 Bot（FR-460 修正）**：`load_batch_id`/`stress_session_id` 为空的 V1 Bot **不纳入回收判定**——它既不能自动处置，也无法经回收端点处置，纳入只会产生无法收敛的死条目。V1 Bot 的清理由既有 `DELETE /bots/:id` 承担。

> 与 FR-365 的关系：`classifyRuntimeEpoch` 只「丢弃过期事件」（第 322 行）；本 FR 增加「过期即判失效 → 回收」的**动作**，二者互补。

### 2.2 宽限与状态机（复用 FR-326 范式）

镜像 `model/orphan_runtime.go` + `service/orphan_runtime.go`（FR-326 无主运行时处置）：

新增表 `model.FleetBotReclaim`（`bot_reclaims`），字段与状态机对齐 FR-326：

```go
type FleetBotReclaimStatus string // pending | confirmed | disposed | cancelled
type FleetBotReclaim struct {
    ID, UUID            // 主键 / 幂等键
    BotID   uint        // 关联 model.Bot
    BotUUID string
    NodeID  uint        // 执行节点
    SessionID, BatchID  // 归属（Fleet）
    StaleReason         // 判据命中说明（epoch 差 / 空 epoch / 节点消失）
    ObservedEpoch, CurrentEpoch int64
    Status              // 见上
    FirstSeenAt, LastSeenAt time.Time
    DisposedAt *time.Time
    DisposeMode string  // auto | manual
    LastError   string
}
```

**宽限**：`SettingKeyBotReclaimGracePeriod = "bot_reclaim.grace_period"`（默认 `2m`，短于 FR-326 的 10m——Bot 生命周期短，且容忍一次世代迁移窗口即可）。
**自动化开关**：`SettingKeyBotReclaimAuto = "bot_reclaim.auto_reclaim"`，**默认 `true` 但仅对 Fleet 归属 Bot 生效**；`pending` 超宽限 → `confirmed` → 自动处置。V1 手动 Bot 不进入回收判定（见 §2.1 修正：纳入只会产生无法处置的死条目）。

> 决策理由：Fleet Bot 由编排器幂等重建、可安全回收；V1 Bot 是用户资源，误删不可逆——因此「自动」严格限定在 `loadBatchId != nil || stressSessionId != nil`。

### 2.3 回收动作

对 `confirmed` 记录下发停用，复用既有链路：

- 执行节点在线：调 `Worker.DeleteBot`（`service/bot.go` 的 `delegateDeleteBot` 已封装）或 `Worker.StopBotBatch`（`worker/bot/manager.go:1179`，按 UUID 列表，带 `generation`/`reason`）。**优先 `StopBotBatch`**：批量、幂等、附带 `reason=bot_reclaim`，便于 Worker 侧日志归因。
- 执行节点离线：`DeleteBot` 会失败；此时只改 CP 账本（`disposed`，`LastError` 记录），待节点回归后由巡检重试——与 FR-326「处置失败可重试」一致。
- **幂等**：Worker 返回「Bot 不存在」视为成功（分片已把它清掉）。

回收成功后 CP 侧同一事务内：

- `Bot.desired_state = stopped`、`status = stopped`、`worker_epoch = ''`；
- 修正批次计数（口径同 `bot_load_execution.go` / `bot_fleet_runtime.go` 的 `adjustConnectedCount`），使 `connected_count` 与实际一致；
- **不物理删除** `Bot` 行（保留取证与审计），仅置 stopped。

### 2.4 容量自动补足（target reconciler）

对每个**存活**的 `status = running` 的 `BotStressSession`（存活判定见 §2.4.1）：

1. 目标 `target = Σ BotLoadBatch.planned_count`；实际 `actual = count(bots where desired_state=running AND status=connected AND deleted_at IS NULL)`；
2. 若 `actual < target`，按缺失的稳定 ordinal（复用 `bot_load_execution.go:stableBotLoadOrdinals` / `botLoadOrdinalFromUUID`）重建缺失 Bot（`materializeBotLoadBots`）、经既有 `dispatchAllocation`/`ApplyBotBatch` 补发（复用 `rebuildRunningAssignment` 生成 assignment）；
3. **不重复派发**：仅对 Worker 快照中确实不存在的 ordinal 补发，避免与 `ReconcileBotFleetSnapshot` 打架。

触发：①回收事务提交后立即触发一次；②周期兜底（`botReclaimSweepInterval = 60s`，与 `evalInterval` 同量级）。

#### 2.4.1 僵尸会话收敛（FR-472）

**问题**：会话的生命周期终结依赖显式 `Stop` 调用。CP 重启或 bot-worker 死亡后该路径丢失，会话永久停留 `status = running`。原 §2.4 无条件捞取全部 running 会话补足，于是对早已失联的会话每拍重建期望行并失败——失败落在事务内，产生持续写盘。

进程内退避（`refillAttempts map`）**无法兜住**：它随 CP 进程结束而清零，重启即触发一轮全量重试。因此僵尸判定必须落在**数据库可见的事实**上。

**判定谓词**：一个 `status = running` 的会话是僵尸，当且仅当同时满足：

1. 该会话在 `botZombieSessionIdleThreshold`（默认 **10 分钟**）内**无任何进展**；
2. 其在线 Bot 数（`desired_state=running AND status=connected AND deleted_at IS NULL`）为 **0**；
3. 该会话**不属于本拍已判定为待补足的活跃集合**（避免与正常补足竞态）。

**进展**的定义（满足任一即视为有进展，重置计时）：会话 `updated_at` 被推进、或在窗口内曾出现过在线 Bot。取 `max(updated_at, 最近一次在线时刻)` 作为进展锚点。

> **阈值依据**：Bot 宽限期为 2 分钟（§2.2，容忍一次世代迁移窗口）。僵尸阈值取 10 分钟，即宽限期的 5 倍，确保「Worker 短暂重启 / 世代迁移」不会误判；同时远短于现场观察到的 3 天量级残留。

**动作**（`reapZombieSessions`，在 `refillRunningSessions` **之前**执行）：

1. 将会话置为 `model.BotStressSessionStopped`，写 `ended_at` 与 `last_error`（注明「僵尸会话自动收敛」及判定依据）；
2. 写一条强审计 `bot_reclaim.session_reaped`（含 sessionId / instanceId / 在线 Bot 数 0 / 停滞时长）；
3. 该会话随后**不再进入补足扫描**（状态已非 running），从根上终止写放大。

**不变量**：仍持有在线 Bot 的会话**永不**被收敛（反向由 `TestZombieSession_HealthySessionUntouched` 守护）。

### 2.5 巡检装配与 OSS

- 新增 `BotReclaimService`（`service/bot_reclaim.go`）+ `BotReclaimSweeper`（周期循环，镜像 `BotFreshnessSweeper` 的 `Start/Stop/loop`：`bot_freshness.go:150`）。
- 在 CP 启动处与 `BotFreshnessSweeper` 并列装配（同 DB/pool/clock 注入）。
- 审计动作：`bot_reclaim.dispose_auto` / `bot_reclaim.dispose_manual` / `bot_reclaim.refill`，经 `AuditService.RecordSafe`（`userID=0` 表示系统；镜像 `orphan_runtime.dispose_auto`）。
- HTTP（admin 组，镜像 `router/orphan_runtime.go` 的 `rg.Group` 风格）：
  - `GET  /api/v1/bot-reclaims?status=&activeOnly=1`
  - `GET  /api/v1/bot-reclaims/:uuid`
  - `POST /api/v1/bot-reclaims/:uuid/reclaim`（手动确认；auto 关闭时的主路径）
- 设置项注册复用 `settings.go` 的 `editableItem` / `defaultValue` / 白名单（对齐 `SettingKeyOrphanGracePeriod` 的四处登记：定义、`editable` 列表、校验 `switch`、默认值 `switch`）。

## 3. 任务拆分

| # | 步骤 | 依赖 |
|---|---|---|
| 1 | `model.FleetBotReclaim` + 迁移；设置项两键（`bot_reclaim.grace_period`/`auto_reclaim`）登记 | — |
| 2 | `service/bot_reclaim.go`：判定查询（§2.1，纯 SQL，注入 clock） | 1 |
| 3 | 宽限/状态机 + `BotReclaimSweeper` 周期巡检（`pending→confirmed`） | 2 |
| 4 | 回收动作：`StopBotBatch`/`DeleteBot` 幂等封装 + CP 账本事务 + 审计 | 3 |
| 5 | target reconciler：缺失 ordinal 补发（复用 `bot_load_execution.go`） | 4 |
| 6 | HTTP 路由 + 单元测试（判定边界、幂等、宽限、Fleet/V1 分流） | 4,5 |
| 7 | MCP 工具 `bot_reclaim_list` / `bot_reclaim_confirm`（可选） | 6 |
| 8 | 前端：回收列表页 + 一键补足按钮（复用 `bots-overview` 页面位） | 6 |

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | 构造 18 个 `workerEpoch` 不匹配的僵死 Bot（`desiredState=running`/`status=error`），巡检在**有限周期**（≤ 宽限 + 1 个 sweep）内将其转 `disposed` 并从 Worker desired 集合去除 | **要真机过**（真实分片重启复现） |
| 2 | 回收后 `loadtest_node_capacity` 的 `activeBots` 与实际 MC 连接数**一致**（不再 500 vs 482） | **要真机过** |
| 3 | 容量自动补足：回收 18 个后，`BotStressSession` 的 connected 数自动补回 `planned_count` 目标 | **要真机过** |
| 4 | 回收/补足各写一条审计（`bot_reclaim.*`，含 bot/node/session/计数） | 单测 + 真机查审计库 |
| 5 | 判据边界：`connected`/`stopped` Bot **不被**回收；epoch 匹配的 `error` Bot **不被**回收 | 单测 |
| 6 | V1 手动 Bot（无 batch/session）在 auto=true 下**不被自动回收**，仅入列表等人工确认 | 单测 |
| 7 | 执行节点离线时处置失败可重试，节点回归后被清（`LastError` → 成功清空） | 单测 + 真机断连注入 |
| 8 | 幂等：Worker 已无该 Bot 时回收仍成功、不报错 | 单测 |
| 9 | 补足路径与 `ReconcileBotFleetSnapshot` 不重复派发（同一 ordinal 只发一次） | 单测 |

## 5. 风险 / 待定

- **误回收风险**：宽限期默认 2m 可能短于一次慢速世代迁移。缓解：`pending` 观测窗内若 Bot 又被新世代事件认领（`worker_epoch_generation` 追平）则转 `cancelled`（镜像 FR-326 `OrphanRuntimeCancelled`）。宽限期是否随节点规模上调，待真机观察。
- **与 FR-437/438 的边界**：FR-437 已放宽容量快照陈旧阈值；本 FR 处理的是**账本层**僵死，不是快照陈旧。若真机再报 `activeBots` 偏差，需先确认来源是 Worker desired 集合还是 CP 账本（§2.3 两条路径都覆盖）。
- **补足阈值**：`actual < target` 的抖动（连接建立中的瞬时 `connecting`）可能触发过度补发。首版仅当 `status != connecting` 且持续一个窗口才补齐；比例阈值（如缺口 > 2%）留待调参。
- **多节点**：`planned_count` 按 `executor_node_id` 分桶（`BotLoadBatch`），补足必须发回**同一执行节点**，否则 assignment 与容量记账错位。
