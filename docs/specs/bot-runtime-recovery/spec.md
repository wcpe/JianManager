# 功能规格：Bot 长稳重连、进程恢复与状态归真

> 状态：已审核（2026-07-18）　·　关联 PRD：FR-365　·　计划分支：feature/fr-365-bot-runtime-recovery
> 超级规格：`../bot-load-platform/super-spec.md`　·　HTTP API：`../bot-load-platform/api.md`
> 依赖：FR-362/363 开发中；按批次计划须等待其所需契约可用　·　可与 FR-369 的非依赖部分并行

## 1. 背景与目标

现有 Bot 被 kicked/end 后只标 disconnected，不自动重连；bot-worker 子进程崩溃后 Manager 可重新启动，但已有 Bot 配置丢失；Worker 重启后 CP 不会重放 Bot；CP 查询不到 Worker snapshot 中的 Bot 时还可能保留旧 connected。500+ 60 分钟严格验收不能依赖人工重连或读取时懒回填。

本 FR 建立 desired-state/generation/workerEpoch/eventSeq 四层归真机制，覆盖单 Bot断线、bot-worker 子进程崩溃、Worker 重启和短时目标服离线，同时限制重连并发，避免恢复风暴。

## 2. 需求（要什么）

- CP 数据库是 Bot desired-state 真源；Worker/Bot Worker 是 runtime 真源。
- 每个 Bot 有 generation，旧配置/旧进程状态不得覆盖新状态。
- Bot Worker 每次启动生成 workerEpoch，事件带单调 eventSeq。
- Bot 断线自动重连，指数退避+抖动+最大并发，目标服不可用时不形成风暴。
- Worker 内存保留本节点 desired assignment，bot-worker 崩溃后自动拉起并重放。
- Worker 进程重启后由 CP 通过 fleet snapshot 做 reconcile。
- CP 状态有新鲜度窗口；运行时消失/事件断流后 connected 必须归真 disconnected。
- 恢复按动作 resumePolicy 处理；默认重启当前步骤，不盲续半个动作路径。
- 以 command_schedule checkpoint 记录命令发送与结果；默认不重发已成功命令。
- 支持只重试失败 Bot 子集，幂等且不改变 cohort。
- 所有 goroutine/timer/listener 在停止后释放。

**范围内**：Bot 恢复字段、generation/epoch/seq、Node 侧自动重连、Worker desired cache/child restart、CP reconcile/freshness、retry-failed 后端、故障指标和测试。

**不做**：跨 CP 高可用、精确恢复毫秒级阶段时间、永久离线节点的自动迁移（首版只在运行中可选择重新分配失败 Bot）、负载 verdict（FR-370）、恢复 UI（FR-372）。

## 3. 设计（怎么做）

### 3.1 数据与顺序

实现超级规格分配给 FR-365 的 Bot 字段：新增 DesiredState、ReconnectCount，并复用现有 WorkerEpoch、WorkerEpochGeneration、LastEventSeq、LastSeenAt、ConfigHash 与 `DesiredStateGeneration`。协议/IPC/proto 中的 `generation` 唯一映射数据库 `bots.desired_state_generation`，不得再新增 `generation` 列或第二套模型字段。

状态接受规则：

1. 每节点 Fleet 订阅在 CP 内有单调 `fleetSubscriptionGeneration`；旧订阅 handler 即使收到迟到事件也先丢弃。
2. 新订阅建立后必须先用 GetBotFleetSnapshot 建 baseline；只有 baseline 可以在 Worker 进程重启时重置 workerEpochGeneration。
3. incoming protocol generation < DB DesiredStateGeneration：丢弃。
4. incoming protocol generation > DB DesiredStateGeneration：协议异常，记录 WARN 并触发 snapshot，不直接覆盖 desired。
5. baseline 后，incoming workerEpochGeneration < DB epoch generation：旧子进程迟到事件，丢弃。
6. incoming workerEpochGeneration == DB epoch generation 且 eventSeq ≤ lastEventSeq：重复/乱序丢弃。
7. incoming workerEpochGeneration > DB epoch generation：同 Worker 进程内新子进程世代，允许 eventSeq 从 1 开始。
8. 同 desired generation 下 configHash 不一致：不接受为健康状态，触发 reconcile。
9. observedAt 与 CP 时间偏差 >5 分钟只记录警告；新鲜度以 CP receive time 为准。

### 3.2 Desired state 变更

- AutoMigrate 只新增 desired_state/reconnect_count；现有 desired_state_generation 已默认 1，不新建 generation 列。幂等 backfill：活动会话中 status=pending/connecting/connected/disconnected/error 的 Bot 回填 desired=running；stopped/无活动会话回填 desired=stopped，仅当既有 DesiredStateGeneration≤0 时修正为1。backfill 可重复运行且不中断升级。
- 创建/启动：DesiredState=running，DesiredStateGeneration 从 1 开始；下发协议字段名仍为 generation。
- stop/delete/配置或场景恢复入口变化：事务内 DesiredStateGeneration+1。
- RunState 与旧 Status、Bot DesiredState 在同一事务演进；兼容映射严格使用超级规格定义。
- ApplyBotBatch assignment 必须带 generation。
- Worker 为每 Bot 保存最高 generation；stale assignment 返回 skipped。
- 删除 Bot 前先 desired=stopped 并完成/记录停止委托，再软删；旧事件因 generation/不存在被忽略。

### 3.3 Bot Worker 自动重连

每 Bot 维护 ReconnectController：

- 触发：kicked/end/error 且 desiredState=running、非人工 stop。
- delay：`min(base*2^attempt,max)` + 0..jitter；默认 base=1s、max=60s、jitter=20%。
- 单 Bot 连续失败默认不设总次数上限，但受 run deadline；达到 10 次进入 degraded 并降低到 max delay。
- 进程级 semaphore 限制同时 connecting，默认 min(10,maxBots/5)，至少 1。
- 服务端恢复后成功 spawn 清连续失败计数，但累计 ReconnectCount 不清。
- stop/cancel 清 timeout、取消 pending connect，禁止 stop 后复活。
- 重连成功重新构造 ScenarioRunner：按 resumePolicy 和 CP 下发 resumeStepId。

### 3.4 Worker 子进程崩溃恢复

Worker Bot Manager 增 `desired map[botUUID]Assignment`，只在内存：

- ApplyBotBatch 先按 generation 更新 desired，再写 IPC。
- bot-worker 退出：标所有 desired running Bot runtime=disconnected，发布 worker-crashed 事件。
- 按进程级退避重启 bot-worker；连续崩溃 5 次/5分钟进入 circuit open 60秒并上报 unavailable。
- worker-ready 后把 desired running assignments 按 batch≤50 重放；desired stopped 不重放。
- 重放 idempotencyKey 使用 `replay:<workerEpoch>:<batch>`，Bot Worker 按 bot id/generation 幂等。
- 子进程 stderr 尾部沿现有机制保留，错误日志中文且不泄露配置凭据。

### 3.5 Worker 重启后的 CP Reconcile

CP `BotFleetReconciler` 由节点重连/心跳 generation 变化触发，另每 30 秒巡检活动运行：

1. 查询节点上 DesiredState=running/stopped 的 Bot。
2. 调 GetBotFleetSnapshot。
3. desired running + snapshot 缺失：ApplyBotBatch 创建。
4. desired stopped + snapshot 存在：ApplyBotBatch stop。
5. snapshot Bot 不在 CP/已删除：先记录 orphan，默认停止；只处理 `sessionId` 属于 JianManager 的 Bot，未知外部进程不碰。
6. generation/配置 hash 不一致：应用较新 desired。
7. 单节点 reconcile 加互斥锁和 30 秒 deadline；同节点不并发。

节点重连首拍需等 bot-worker capacity ready，再 reconcile；失败按指数退避，不阻塞普通心跳。

### 3.6 Fleet 事件订阅与快照补偿

- CP 对每个含活动运行 Bot 的 ExecutorNode 建立一条 FR-362 `StreamBotFleetEvents`，按 node UUID 管理生命周期；目标实例节点与执行节点不同不影响订阅。
- 事件携 sessionId/generation/configHash/workerEpochGeneration/eventSeq/observedAt/currentStep/error。
- 流断开：立即把该节点活动 Bot 标记为状态待确认，先调用 GetBotFleetSnapshot 补偿，再按退避重连流。
- CP 重启：先从 DB 找活动 ExecutorNode，逐节点 snapshot，再开流；不依赖用户打开 Bot 页面触发。
- 运行结束且节点无其他活动 Bot 时释放订阅。

### 3.7 状态新鲜度归真

- 活动运行中 Bot 状态新鲜度默认 10 秒（状态快照周期 3 秒）。
- LastSeenAt 超 10 秒且 desired running：connected/connecting→disconnected，并记录 STATUS_STALE；不立即算 error。
- 超 90 秒且节点在线但 snapshot 缺失：error RUNTIME_MISSING，reconcile。
- 节点离线：统一 category EXECUTOR_OFFLINE，不逐 Bot 刷 500 条日志；聚合一条节点故障并批量更新。
- Worker 重新出现后状态可恢复，历史 failure 仍保留供报告。

### 3.8 动作恢复与命令检查点

CP 从最新未终态 action result 和 `command_schedule` checkpoint 得到 current step：

- checkpoint 稳定唯一键至少为 `runUuid + botUuid + stepId + commandId + occurrence`，记录最近 generation/scheduleRunId/actionRunId、nullable 计划/实际发送时间、最终 attempt、发送状态与终态结果；prepared 未 release 即取消时 plannedAt/sentAt 均为 null；已成功执行项默认不重发。
- restart_step：resumeStepId=当前 step，attempt+1，并生成新的 scheduleRunId；每个未完成 occurrence 据此复算新的 actionRunId。旧 actionRunId 的迟到结果不能完成新动作，但 checkpoint 身份不随 actionRunId 改变。
- restart_scenario：从 cohort 第一步开始；V1 不暴露 replay policy，固定按稳定 checkpoint 键跳过全部已 `sent` 的 commandId+occurrence，仅重放未终态项。
- fail：不重连场景，Bot runtime 可 connected，但动作标失败。
- barrier 重启后重新 arrived，同一个 Bot 只计一次当前 generation。
- stop/cancel 后冻结调度并取消未发送命令；任何恢复、重连或迟到事件都不得继续发送命令。
- run 总 deadline 不重置。

### 3.9 Retry failed

`retry-failed`：

- 选择失败 Bot 后事务 DesiredStateGeneration+1、DesiredState=running，协议继续下发为 generation，清当前 LastError 但保留历史 ActionResult。
- 可选 fromStepId 必须属于该 cohort；未传按 resumePolicy。
- 按 executor node 批量 Apply；原节点不可用时，首版可由 FR-362 allocator 在当前可用节点重新分配，更新 ExecutorNodeID/BatchID，但不改变 Bot UUID/Name/Cohort。
- 同请求通过审计 requestId 幂等，重复调用不重复 generation+1。

### 3.10 资源与观测

Bot Worker heartbeat 需真实上报：active/connecting、RSS、eventLoopP95、droppedEvents、workerEpoch。Node built-in `perf_hooks.monitorEventLoopDelay` 可用，不新增依赖。

保护：

- RSS > 节点内存 85% 或 eventLoop p95 >500ms 持续30秒时 capacity available=0，拒新 Bot；不自动杀现有 Bot。
- reconnect semaphore 与批次 connectNotBefore 共同生效。
- droppedEvents 单调计数，CP 报警但状态快照仍作为补偿。

## 4. 任务拆分

- [ ] 测试先行：generation/epochGeneration/seq/configHash 接受矩阵和状态新鲜度。
- [ ] Model/AutoMigrate：FR-365 所属字段、索引和历史 desired-state 幂等 backfill。
- [ ] Bot Worker：ReconnectController、并发 semaphore、stop 取消、ScenarioRunner resume。
- [ ] Worker：desired cache、子进程 crash circuit/restart/replay、fleet snapshot。
- [ ] CP：每执行节点 FleetEvent 订阅、snapshot 补偿、BotFleetReconciler、节点锁、orphan 保护、stale 状态归真。
- [ ] CP：retry-failed service/router/审计和重新分配。
- [ ] 指标：workerEpoch、eventLoop、RSS、dropped/reconnect。
- [ ] 故障注入集成测试：kick/TCP断开/kill child/restart Worker/offline target。
- [ ] 文档同步：ARCHITECTURE、API、PRD 本 FR 状态、CHANGELOG、运维故障说明。

## 5. 验收标准

### 自动化

- [ ] stale generation、同 epoch 乱序/重复 seq、新 epoch seq 重置全部正确。
- [ ] stop 后 pending reconnect 被取消，30 秒观察无 Bot 复活。
- [ ] 100 个 Bot 同时断线时 connecting 并发不超过配置，退避含抖动且无忙循环。
- [ ] bot-worker 崩溃后 Worker 自动重启并重放 desired，Bot UUID/generation/cohort 不变。
- [ ] Worker 重启后 CP reconcile 创建缺失、停止多余、修正配置；同节点无并发 reconcile。
- [ ] 状态超过窗口从 connected 收敛到 disconnected/error，不保留幽灵在线。
- [ ] retry-failed 幂等、可换执行节点、不改变 Bot 身份和 cohort。
- [ ] 所有 fault test 结束后 goroutine/timer/listener 数不持续增长；相关 Go race 全绿。

### 真机

- [ ] 随机踢出 10% Bot，系统按退避恢复，在线率回到阈值且无登录风暴。
- [ ] 杀掉一个 bot-worker 子进程，该节点 Bot 先降级后自动恢复。
- [ ] 重启一个 Worker，CP desired-state reconcile 完成，幽灵状态归零。
- [ ] 目标服离线 2 分钟再恢复，重连速率受控且最终恢复场景。
- [ ] 500 Bot 60 分钟期间 RSS 不持续单调增长，事件循环/掉事件可观测。

## 6. 风险 / 待定

- 精确恢复动作内部瞬时状态不可行，按步骤与命令检查点恢复是明确取舍。
- Worker 内存 desired cache 在 Worker 重启后丢失，由 CP DB reconcile 恢复。
- CP 单实例前提下 reconcile 锁为进程内；未来 CP HA 需分布式租约，范围外。
- 目标服长期不可用时 Bot 会低频重试直到运行截止或人工停止，不永久停止 desired。
- 不新增第三方依赖。
