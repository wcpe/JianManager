# 功能规格：Bot 发压节点池与分布式调度底座

> 状态：已审核（2026-07-18）　·　关联 PRD：FR-362（增强 FR-038/042/274）　·　计划分支：feature/fr-362-bot-distributed-load
> 超级规格：`../bot-load-platform/super-spec.md`　·　HTTP API：`../bot-load-platform/api.md`
> 架构决策：`ADR-074`（accepted，部分修订 ADR-006 的单 Worker 归属假设）

## 1. 背景与目标

当前 Bot 压测会话绑定目标实例，`BotService.Create` 总是按实例所属节点路由到一个 Worker；Bot Worker 单进程硬上限 50。虽然 HTTP count 允许到 5000，但第 51 个 Bot 起会被单 Worker 容量拒绝，系统没有容量预检、跨节点分片、批次回执和真实连接完成语义。

本 FR 建立分布式发压地基：目标实例继续负责权限和被测指标，Bot 可以由其他具备 Bot 运行时的 Worker 执行；Control Plane 在启动前发现容量、生成确定性分片计划，并通过批量 gRPC/IPC 幂等下发。

## 2. 需求（要什么）

- 目标实例与执行节点解耦，普通单 Bot 默认行为保持不变。
- Worker 上报 Bot Worker 是否就绪、版本/能力、容量、活跃/连接中 Bot、RSS、事件循环延迟和进程世代。
- CP 提供目标实例作用域的发压节点容量列表。
- CP 对运行做预检：当前校验已冻结 Scenario 与执行节点容量，生成 allocation plan 和短期软预留；`probe` 仅保留 `required=false` 的兼容响应字段，不参与校验，load profile/thresholds 由后续 FR-370 扩展。
- 分片算法必须确定性、可测试，单批不超过执行节点容量和 50 个 assignment。
- 启动改为后台化，HTTP 立即返回 202；`accepted` 不得等同于 `connected`。
- gRPC 批量应用 assignment，IPC 一次发送数组，禁止 500 次逐 Bot unary 创建。
- 每批有 idempotencyKey；HTTP 重试、gRPC 重试和 Worker 重连不重复建 Bot。
- 部分节点失败时运行进入可诊断的降级态，成功批次保留，不做无条件全回滚。
- 停止按批次发送，DB desired-state 与 Worker 回执可对账。
- 旧 Worker/bot-worker 仍可执行既有单 Bot操作，但不进入分布式节点池。

**范围内**：Bot/批次模型增量、容量 RPC、批量 Apply RPC、Worker/IPC 批量下发、容量目录、预检与分片、启动/停止后台化、权限/审计、API/文档/测试。

**不做**：V2 场景动作（FR-363）、ServerProbe/ProbeEvent 数据源（未来独立可选适配能力，不归当前 FR-369～372）、重连与进程恢复（FR-365）、三类负载状态机和 verdict（FR-370）、前端向导和观测（FR-371/372）、提高单 Worker 默认容量、自动创建云节点。

## 3. 设计（怎么做）

### 3.1 ADR 决策边界

新 ADR 必须记录：

1. 保持 Worker spawn Bot Worker + IPC 的三进程模型。
2. `InstanceID` 是目标/权限归属，`ExecutorNodeID` 是运行位置。
3. CP 是全局调度和 desired-state 真源；Worker 是节点容量和 runtime 真源。
4. 首版默认 50 Bot/Worker，通过多 Worker 扩容。
5. 批量 RPC + 幂等 key，拒绝逐 Bot 500 次 RPC。
6. 分布式节点池只接受声明 fleet 特性的新版 Worker/bot-worker。

### 3.2 模型

严格按超级规格 §6 实现本 FR 所属字段：

- `Bot.ExecutorNodeID *uint`、`Bot.LoadBatchID *uint`，关系指向 `Node`/`BotLoadBatch`。
- `BotLoadBatch` 新表及唯一索引。
- `BotStressSession.AllocationPlan`。

普通单 Bot：若 ExecutorNodeID 为空，路由回 `Instance.NodeID`；成功委托后可将实际执行节点写入 WorkerID 兼容字段。

### 3.3 Worker 容量

新增 `BotCapacityProvider`，不把容量逻辑塞入 gRPC Server：

```go
type BotCapacitySnapshot struct {
    Ready             bool
    Legacy            bool
    MaxBots           int
    ActiveBots        int
    ConnectingBots    int
    CapacityGeneration int64
    WorkerEpoch       string
    WorkerEpochGeneration int64
    BotWorkerVersion  string
    RSSBytes          int64
    EventLoopP95Ms    float64
    ObservedAt        time.Time
    UnavailableReason string
}
```

来源：Bot Manager 的 worker-ready/heartbeat；bot-worker 未启动时允许惰性探测运行时和 dist，但预检不能为了展示容量创建 Bot。若依赖未安装或启动失败，Ready=false 并返回可操作原因。

CP 容量目录：

- 每次 HTTP 查询并发请求候选 Worker 的 `GetBotCapacity`，并发上限 16、单节点超时 3 秒。
- 同时维护最多 15 秒短缓存，避免向导频繁刷新打满节点。
- available = max(0, max-active-reserved)。
- 节点离线、maintenance、无 fleet feature、容量过期均不可选。
- CapacityGeneration 只表示“容量语义版本”：bot-worker/Worker epoch 变化、maxBots/features 变化、admission ready/unavailable 变化时递增；active/connecting 即时利用率变化不递增，另随快照返回。GetBotCapacity/HTTP/planToken/ApplyBotBatch expected generation 全链携带。
- preflight 按 runMaxTargetBots 一次性为整个运行软预留最大容量；step 后续增量 batch 复用同一 plan/lease，不因本运行自身连接数变化失效。其他运行或旧单 Bot绕过软预留占用容量时，Worker 最终 admission 可逐项拒绝并触发重新预检。

### 3.4 分片算法

输入：targetBots、候选节点容量、可选用户节点顺序、每节点连接速率。

规则：

1. 用户指定节点时保留其顺序；自动选择时按 available 降序、nodeId 升序稳定排序。
2. 采用轮转填充：每轮给各节点分配最多 `min(50, availableRemaining)`，优先避免把全部连接集中到一台。
3. 每个 batch plannedCount 1..50；一个节点若未来容量可配置 >50，可生成多个 batch。
4. 总 available < targetBots：返回 ready=false，不写 Bot。
5. 生成 `connectStartAt/connectIntervalMs/idempotencyKey`；同输入和同容量 generation 下计划稳定。
6. planToken 包含 runId、allocation hash、节点 capacity generation、expiresAt；服务端签名/哈希验证，不信任客户端回传 plan 内容。

### 3.5 软预留

- 预检 ready 时在 CP 内存建立 runId→节点 reserved count，TTL 60 秒；同一 run 重预检替换旧预留。
- 容量列表合并已持久化运行中的批次 + 内存预留。
- CP 重启丢失软预留可接受；start 必须再次调用 GetBotCapacity 快检。
- planToken 过期或 capacity generation 变化返回 409 CAPACITY_CHANGED。

### 3.6 gRPC

按超级规格冻结新增：

- `GetBotCapacity`
- `ApplyBotBatch`
- `GetBotFleetSnapshot`（本 FR 提供当前快照，FR-365 用于 reconcile）
- `StreamBotFleetEvents`（本 FR 接真持续状态流，CP 活动运行按执行节点订阅）
- `SignalBotActions`（本 FR 完整实现 gRPC→IPC 通用投递和逐项回执；FR-363 提供/消费 Scenario 信号语义。旧 FR-364 已废弃；FR-369 command_schedule 使用独立命令 IPC，不复用本 RPC）

FR-362 还在 proto 一次铺齐 assignment/runtime 的 sessionId、generation、configHash、workerEpochGeneration、eventSeq。若保留实例指标 `mspt_p95_millis` additive 字段，其所有权属于既有实例监控/未来 optional legacy 观测，不归 FR-369，也不参与通用命令 preflight、成功或默认 verdict；预铺字段不得被解释为本批次的数据源承诺。

`ApplyBotBatchRequest`：batchId、idempotencyKey、assignments。响应逐 assignment：accepted/status/errorCode/error。

Worker 幂等缓存：

- 以 idempotencyKey 保存最近结果，至少保留 1 小时/最多 1000 项，进程内即可。
- 同 key 不同 payload 返回 FailedPrecondition。
- assignment generation 小于 Worker 已见 generation 时 skipped/stale。

### 3.7 Worker → Bot Worker IPC

- 所有同步确认命令携 requestId；Manager 建立有界 pending request map 和超时清理。
- 一批 assignment 组装为一次 `create-bots(requestId,batchId,idempotencyKey)`；停止组装为一次 `stop-bots(requestId)`。
- Node 返回 `batch-result`，逐 Bot 给 accepted/skipped/errorCode/error；ApplyBotBatch 只能据该回执响应，不能以“成功写 stdin”冒充 Node 已接受。
- `signal-actions/get-fleet-snapshot` 同样有 requestId 和 `signal-result/fleet-snapshot-result`。
- BotConfig 扩展字段必须可选，旧单 Bot路径可继续构造旧字段。
- Bot Worker `createBots` 容量判断改为按“新增 ID 数”计算；替换已有 Bot 不应在满容量时被错误拒绝。
- 创建连接遵守 connectNotBefore；本 FR 只实现时间门控，复杂 stable/step/spike 由 FR-370 生成计划。
- 连接终态经 `bot-state`→Worker `StreamBotFleetEvents` 异步上报 CP；accepted 与 connected 严格分离。

### 3.8 统一执行节点路由

所有既有 Bot 操作统一经 `BotExecutorResolver`：`ExecutorNodeID != nil ? ExecutorNodeID : Instance.NodeID`。覆盖 Create/Delete/Batch stop/start/SetBehavior/SendCommand/List/详情状态刷新/StreamBotEvents/压测 stop/retry；WorkerID 仅作兼容展示，不作路由真源。每条路径必须有“目标实例节点≠执行节点”的回归测试。

### 3.9 CP 启动流程

1. 校验 run=ready、planToken、权限。
2. 事务创建/更新 BotLoadBatch 和 Bot 记录，Bot 状态 pending、ExecutorNodeID/BatchID 已定。
3. 提交事务后启动后台 dispatch；HTTP 202。
4. 每批调用 ApplyBotBatch；按逐 Bot 回执写 accepted/error。
5. dispatch 失败只标该 batch failed 和 Bot error；其他批次继续。
6. 所有批次派发结束后，兼容 Status 映射为 running；FR-370 接管细粒度状态。
7. DB 写失败不发 RPC；RPC 成功后 DB 回写失败由 fleet snapshot/FR-365 reconcile 收束。

### 3.10 停止流程

- 将目标 Bot desired intent 视为 stopped（FR-365 字段尚未落前，本 FR用 batch state + Bot status 表达）。
- 按 executor node 聚合 bot UUID，一次 batch stop。
- Worker 不可用时记录 batch lastError，Bot 不伪装 stopped；后续 FR-365 reconcile。
- 旧 stop endpoint 保持幂等。

### 3.11 权限与审计

- 容量列表/预检/启动/停止都先校验目标实例权限。
- 非平台管理员不获得节点 host/secret，只返回节点名、容量和健康摘要。
- 审计 action 按共享 API 文档。

### 3.12 兼容

- 既有单 Bot Create/Delete/List 不删除。
- 旧 V1 会话 count≤目标节点 available 时可内部生成单节点 plan。
- 旧 Worker 容量显示 legacy，不参与 V2 分片。
- 数据库只加列/表。

## 4. 任务拆分

- [ ] 测试先行：Bot/Batch model AutoMigrate、关系、唯一索引与兼容默认路由。
- [ ] 测试先行：容量快照、legacy 判定、缓存和不可用原因。
- [ ] 测试先行：确定性分片、容量不足、用户节点顺序、50 上限、计划 token/过期/变化。
- [ ] 测试先行：软预留并发与 TTL。
- [ ] Proto：一次铺齐 capacity/batch/snapshot/fleet-event/signal RPC/message 与 runtime/configHash/epoch generation 字段，重新生成 workerpb，不手改生成文件。
- [ ] Worker：GetBotCapacity/ApplyBotBatch/GetBotFleetSnapshot/StreamBotFleetEvents/SignalBotActions、幂等缓存、逐项回执。
- [ ] Worker IPC：requestId pending map、batch/signal/snapshot result、超时和迟到回执清理。
- [ ] Bot Worker：扩展 IPC 类型、连接时间门控、满容量替换已有 Bot 修正、批量/信号/snapshot ack 测试。
- [ ] CP：容量目录、preflight、后台 dispatch/stop、批次与 Bot 回写。
- [ ] Router：load-nodes/preflight/start/stop 契约和权限/审计。
- [ ] 兼容测试：旧单 Bot、旧 V1 会话、legacy Worker。
- [ ] 文档同步：ADR-074、ARCHITECTURE、API、PRD 本 FR 状态、CHANGELOG、adr/README。

## 5. 验收标准

### 自动化

- [ ] 分片器表驱动测试覆盖：500→10×50、容量不均、容量不足、节点失联、确定性、同节点多 batch。
- [ ] 并发预检不会超卖软预留；`go test -race` 相关包全绿。
- [ ] planToken 过期/容量世代变化时 start 返回 409，且 DB/Worker 零副作用。
- [ ] ApplyBotBatch 重放不重复连接；同 key 不同 payload 拒绝；Node IPC batch-result 与 gRPC 逐项结果一一对应。
- [ ] 一批 50 Bot 只产生一次 gRPC 和一次 create-bots IPC，不退化为逐 Bot 调用。
- [ ] accepted/connected 语义分离：只有 Node batch-result accepted 才计 accepted，只有 StreamBotFleetEvents 状态才计 connected。
- [ ] capacityGeneration 变化使旧 planToken/expected generation 失效。
- [ ] 目标实例权限与执行节点路由分离；Create/Delete/Command/Behavior/List/Event/Stop 全部命中 ExecutorNodeID，非授权用户不能读取/启动运行。
- [ ] 旧单 Bot和 50 Bot V1 会话回归全绿。
- [ ] `go test ./internal/controlplane/... ./internal/worker/...`、相关 `go test -race`、bot-worker tests、proto 生成检查全绿。

### 真机（当前交付门禁 · 缩比，用户 2026-07-21 确认）

> 与 FR-274 有界窗口同理：机制与跨节点语义必须真链路验证；满规模 10×50/500 改为可选扩容验收，不阻塞本 FR 完成。

- [x] ≥2 个新版 Worker 均显示 `botWorkerReady` / `maxBots=50` / available（真进程，非同进程 fake Worker）。
- [x] 对同一目标实例预检 **>50** Bot，计划跨 **≥2** 节点且每节点≤声明容量（例：51→50+1）。
- [x] 启动后目标服真实在线人数达到缩比目标（≥6；跨节点预检后至少再证一档真实连接），CP 库 Bot 行数、批次计数与 Worker 侧状态一致，**禁止**仅以 DB 行数冒充在线。
- [x] 容量不足时 preflight `ready=false`（例：仅 3 Worker 时 500 不误放行）。
- [x] stop 后可达 Worker 上 Bot 退出；会话/批次状态收敛。
- [x] 人为关闭一个发压 Worker 时，其他批次/节点不伪造「全员成功」；API 可区分失败/缺口（缩比规模即可）。

### 真机（可选满规模，不阻塞 FR-362 完成）

- [ ] ≥10 个新版 Worker botWorkerReady/max=50/available。
- [ ] 预检 500 Bot 为 10+ 节点且每节点≤50；启动后真实在线 500 且与批次/snapshot 一致。
- [ ] 满规模故障注入与 stop 全链路。

## 6. 风险 / 待定

- **满规模环境**：10 Worker / 500 真连为可选扩容验收；缩比门禁已用户确认，禁止用同进程 fake Worker 代替「≥2 真 Worker」门槛。
- **容量超卖**：软预留不是分布式锁；start 二次快检和 Worker 实际 admission 是最终防线。
- **节点选择公平性**：首版确定性轮转，不做成本/地域/网络质量智能调度。
- **Proto 冲突**：本 FR 必须一次铺齐 FR-369/365 所需 fleet/signal 字段；后续发现缺口先回协调分支改超级规格，不允许并行分支各改 proto。
- **无新增依赖**：实现使用现有 Go/Node 能力；如需新依赖必须先获得用户确认。
