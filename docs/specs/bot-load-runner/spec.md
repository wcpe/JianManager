# 功能规格：压测运行状态机、三类负载曲线与自动判定

> 状态：已审核（2026-07-18）　·　关联 PRD：FR-355　·　计划分支：feature/fr-355-bot-load-runner
> 超级规格：`../bot-load-platform/super-spec.md`　·　HTTP API：`../bot-load-platform/api.md`
> 依赖：FR-351～354 已落协调分支

## 1. 背景与目标

FR-351～354 提供分布式 Bot、确定性场景和恢复能力，但尚缺少可复用模板、完整运行状态机、固定/阶梯/洪峰调度、时序指标、阈值判定、安全停止和正式报告。当前 BotStressSession 只有 pending/running/stopped/error 和查询时聚合，不能回答“最大稳定并发是多少、哪一层先过载、是否达到严格标准”。

本 FR 将 BotStressSession 增强为压测运行账本，新增模板和聚合指标，驱动三类负载计划并自动形成 verdict/report。前端由 FR-356/357 消费，本 FR 先完成全部后端契约。

## 2. 需求（要什么）

- 场景模板与运行快照分离；运行不受后续模板编辑影响。
- 支持 stable、step、spike 三类 load profile。
- 运行状态机覆盖预检、就绪、启动、运行、降级、停止、取消、完成和失败。
- 启动/停止/取消为后台任务，不阻塞 HTTP。
- 每 5 秒聚合一次连接、命令发送、调度完成、屏障和 Worker 健康指标，不逐 Bot 写时序。
- 阈值 evaluator 持续判断，区分 verdict 阈值和安全停止阈值。
- step 模式输出 maxStableBots；spike 模式输出连接/屏障/攻击峰值延迟。
- 失败分类必须区分 target/executor/network/scenario/internal；Probe 不属于本 FR 的硬依赖。
- 终态生成 JSON 报告和 CSV 导出，并明确报告仅反映 Bot 运行时与调度观测，不等同于目标游戏性能结论。
- 提供会话级 SSE 聚合流、运行内 Bot/失败/指标分页 API。
- 支持 stop（有序完成）和 cancel（尽快中止），运行证据均保留。
- 旧 BotStressSession API 和字段保持兼容。

**范围内**：模板/运行/指标模型、状态机、profile runner、指标采集、阈值与安全停止、失败分类、报告、会话级 SSE、HTTP API/审计、自动化与真机验收。

**不做**：定时任务、CI 门禁、云扩缩容、前端页面（FR-356/357）、CP HA、多租户、按每次普通攻击持久化原始时序。

## 3. 设计（怎么做）

### 3.1 模型

实现超级规格分配给 FR-355 的字段/表：

- BotStressSession：TemplateID、LoadProfile、Thresholds、RunState、CurrentStage、Verdict、MaxStableBots、FailureSummary、ReportSummary，以及 ConnectedAt。
- BotLoadTemplate。
- BotLoadMetricSample。
- BotLoadRunEvent（append-only 关键运行事件，供历史事件链和 SSE 快照补偿）。

模型 JSON 列在 service 边界用强类型 DTO 序列化；handler 不直接拼 map。AutoMigrate 加性，不改旧列。

### 3.2 模板服务

- CRUD 和分页搜索按共享 API。
- Create/Update 同时校验 Scenario V2、LoadProfile、Thresholds。
- 名称软删活跃唯一；若现有数据库不适合部分唯一索引，则 service 预检 + DB 普通复合索引，保持 SQLite/MySQL 兼容。
- 从模板创建运行时深拷贝快照；不保存 ExecutorNodeIds 到模板。
- 模板不硬编码 Probe、塔防或其他具体游戏玩法；领域特定 preset 只能作为可选普通模板，不参与默认验收。

### 3.3 运行状态机

实现纯状态机包，转换严格按超级规格 §7：

- handler 只调用 service intent；状态写入和副作用由 runner 管理。
- 同一 run 只有一个 active runner；进程内锁 + DB conditional update 防重复 start。
- CP 重启扫描非终态 run：
  - ready/pending 保持；
  - starting/running/degraded/stopping/cancelling 恢复 runner，并先执行 FR-354 reconcile；
  - 无法恢复场景 snapshot 时 failed。
- 状态变更写审计/结构化日志并发布不可丢 SSE 事件。

### 3.4 Profile Planner

#### stable

- targetBots 固定；rampUpSeconds 生成各 batch/Bot connectNotBefore。
- 达到 ready condition（默认 onlineRate≥阈值或 ramp 结束+宽限 60s）开始 duration 计时。
- duration 内持续判定，结束后有序 stop。

#### step

- stages targetBots 必须严格递增、1..12800；holdSeconds 10..86400。
- 每级只增加差量 Bot，不重新创建已有 Bot。
- 进入 stage 后等待连接宽限，再开始 hold。
- stage hold 满足 verdict 阈值则更新 MaxStableBots 并进入下一级。
- 阈值失败且 stopOnThresholdFailure=true：停止升压，记录失败级，进入有序停止；MaxStableBots 保留上一通过级。

#### spike

- targetBots 在 connectWindowSeconds 内均匀/分片生成 connectNotBefore。
- 若配置 barrierKey，达到屏障条件后生成同一 releaseAt，要求 releaseWindowMs 内动作开始。
- 分阶段记录 connect、barrier release、attack start 延迟 p50/p95/p99。
- holdSeconds 后有序停止。

### 3.5 Runner 调度

RunCoordinator 每运行一个 goroutine，但不为每 Bot 建 goroutine：

- 事件输入：Bot 状态、命令发送与调度结果、屏障事件、Worker capacity/heartbeat、人工 intent、tick。
- tick 默认 1 秒，只推进阶段/阈值；指标采样 5 秒。
- 事件 channel 有界；高频 count/metric 合并，state/stop 不丢。
- context cancel 时释放 timer、订阅和锁。
- 所有 RPC 带 deadline；单节点失败不阻塞其他节点。

### 3.6 指标采集

#### 默认指标

从 DB 增量/内存聚合读取连接成功、命令发送成功、调度完成、屏障到达和 `command_schedule` lag，并按冻结 expectedBotSet 计算比率。避免每 5 秒扫描全 bots 表：维护 run 级聚合器，CP 重启时可从 DB 与 checkpoint 重建。

#### Worker 健康

复用 GetBotCapacity/Worker 心跳；按 ExecutorNodeID 聚合 active/connecting/RSS/eventLoop/dropped/crashes。CPU 可从节点进程指标取得时填，否则 null，不造假。

#### 可选 legacy 指标

TPS、MSPT、房间、伤害等目标游戏指标仅在环境提供相应适配器时采集；字段缺失时为 null，不用 0 代替，不影响默认 verdict，也不阻断 acceptance harness。模板可显式配置独立的 legacy 判定，但报告必须将其与默认验收结论分开。

#### 写入

每 5 秒写一条 BotLoadMetricSample；同 timestamp upsert 幂等。终态后停止采样。后台清理 30 天前样本，ReportSummary 永久保留；清理任务复用现有后台调度框架，不引入定时任务产品入口。

### 3.7 阈值 evaluator

输入当前阶段窗口内样本和累计动作结果，输出：

```go
type Evaluation struct {
    Passed bool
    Pending bool
    Reasons []Reason
    SafetyStop *Reason
}
```

- runMaxTargetBots 在运行开始冻结；每个 stage 冻结 stageTargetBots/stageExpectedBotSet。step 首级100只以100为当前分母，后续250/500分别冻结新集合；失败/停止/重分配不得缩小当前级分母。
- 每个可信断言步骤从当前 stageExpectedBotSet 冻结 expectedBotSet；未进入步骤、断线、超时者在窗口结束时计失败，不从分母移除。
- stable 预热：连接成功率≥99%，且每个 cohort 进入 `observationStep:true` 的 Bot/该 cohort stageExpectedBotSet≥99%，两者连续60秒；ramp结束后10分钟内必须达到。step 每级差量派发后同样预热。
- 观察窗每5秒采样；连接成功、命令发送成功、调度完成和屏障到达均按冻结 expectedBotSet 计算，窗口内最小值必须分别≥99%，不是累计平均。
- `command_schedule` lag 以计划发送时间至实际发送时间计算，p95≤1秒；非预期 Worker/bot-worker crash 必须为0。
- 样本覆盖率≥99%，连续缺样>30秒失败。
- step 每级按同一公式；spike 额外计算连接、屏障释放和命令开始延迟。
- percentile 纯函数用于连接、命令和调度延迟。
- TPS、MSPT、房间、伤害等 legacy 指标不进入默认 Evaluation；缺失或未配置不得令默认 verdict 失败或 Pending。
- safety 条件需连续 sustainSeconds，不因单点毛刺停止。
- 非预期 WorkerEpochGeneration 增长/目标实例 crash snapshot 计 crash；正常 stop/计划内重启不计。

### 3.8 失败分类

固定 category：

- target：目标服崩溃或可选 legacy 适配器报告的目标异常。
- executor：Worker/bot-worker 不可用、RSS/eventLoop admission、子进程崩溃。
- network：连接超时、ECONNREFUSED、kicked、跨服失败。
- scenario：命令发送、调度、barrier、动作超时。
- internal：DB/gRPC/状态机异常。

为兼容历史报告可读取 legacy `probe` category，但新运行的默认判定不依赖或产生 Probe 专属失败。

错误码→category 用纯映射表；未知为 internal/UNKNOWN。FailureSummary 存计数，不存完整堆栈。

### 3.9 安全停止与终态

- safety stop：runState→stopping，verdict=aborted，reason 写报告。
- stop：完成当前 DB 事务后取消场景、批量停止 Bot，等待最多 60 秒；剩余差距入报告，不无限等待。
- cancel：立即发取消/停止，等待最多 15 秒；终态 cancelled/aborted。
- runner 内部不可恢复错误：failed/failed，但仍尽力停止 Bot并生成部分报告。
- 终态生成 ReportSummary，发布 complete SSE。

### 3.10 报告

JSON 报告按共享 API。CSV 至少包含两部分，以 `section` 列区分：summary/stage/failure/executor/action；UTF-8 BOM 便于 Excel。导出不临时重新扫描高频原始事件，只读 report summary + samples/action results。

JSON、CSV 与 SSE complete 摘要必须携带免责声明：默认 verdict 只证明当前环境下 Bot 连接、命令发送、调度、屏障与 Worker 健康达到阈值，不代表目标游戏服容量、TPS/MSPT 或具体玩法正确性；可选 legacy 指标仅作附加观测。

### 3.11 会话级 SSE

实现 run event broker：

- 每 run ring buffer 最近 1000 个聚合事件；eventId 在单次 CP 进程 epoch 内单调。
- Last-Event-ID 命中当前 buffer 时补发；太旧或跨进程 epoch 时发送 init 最新持久化快照。
- 所有 SSE 是可丢增量，DB run/metric/event 才是真源；run-state/stage/complete 必须先持久化再发布。
- 每连接队列上限256；慢消费者满队列时主动断开，禁止无界“可靠队列”。重连后用 init/历史 API补齐。
- 单用户同 run 最多 5 条连接，超出返回429。
- 终态 complete 后关闭。

### 3.12 API/审计/兼容

严格实现共享 `api.md`：模板 CRUD、从模板创建 run、扩展 session list/detail/create、preflight/start/stop/cancel/retry、metrics/bots/failures/report/events。

旧 V1 start 未传 planToken，仅在单节点兼容条件下内部预检；V2/500+ 必须显式预检。

## 4. 任务拆分

- [ ] 测试先行：Template/Metric model、JSON DTO、CRUD/软删/快照。
- [ ] 测试先行：运行状态机所有合法/非法转换、CP 重启恢复。
- [ ] 测试先行：stable/step/spike planner 和阶段推进。
- [ ] 测试先行：percentile、rate 分母、Pending、连续 safety 窗口、失败分类。
- [ ] Model/AutoMigrate（Template/Metric/RunEvent）+ TemplateService/Router。
- [ ] RunCoordinator、runner registry、重启扫描、状态事件。
- [ ] 指标聚合器/5秒样本/30天清理。
- [ ] ThresholdEvaluator、安全停止、MaxStableBots。
- [ ] ReportService JSON/CSV。
- [ ] Run SSE broker + metrics/bots/failures/events历史投影/report APIs。
- [ ] 新增固定 acceptance harness：`JM_BOT_LOAD_ACCEPTANCE=1 JM_BOT_LOAD_ENV=.tmp/bot-load-acceptance/environment.json go test -tags=botloadacceptance ./internal/e2e -run '^TestBotLoadAcceptance$' -count=1 -timeout=4h`；运行 60 分钟、500+ Bot，校验连接/命令发送/调度完成/屏障/Worker 健康各≥99%、schedule lag p95≤1s、crash=0，输出 passed/failed/blocked 机器可读证据；不要求 Probe 或塔防适配。
- [ ] 扩展 devmock 后端契约，为 FR-356/357 提供动态运行模拟。
- [ ] 文档同步：ARCHITECTURE、API、PRD 本 FR 状态、CHANGELOG、运维说明。

## 5. 验收标准

### 自动化

- [ ] 状态机每条合法/非法转换均有测试；重复 start/stop/cancel 幂等。
- [ ] CP 重启后非终态 run 恢复且不重复创建 Bot/runner。
- [ ] stable 计时从连接宽限完成后开始；step 差量升压并准确输出上一通过级；spike 窗口和 barrier 延迟统计正确。
- [ ] 5秒采样不对每 Bot逐行写 DB；60分钟运行约 720 个 sample。
- [ ] 固定测试向量锁定：预热起点、720样本窗口、固定分母、expectedBotSet、连接/命令发送/调度完成/屏障/Worker 健康各≥99%、schedule lag p95≤1s、缺样覆盖率和 crash=0。
- [ ] 阈值缺样本为 Pending，连续缺样/安全窗口触发失败或 safety，单点毛刺不触发。
- [ ] p50/p95/p99 仅用于连接/动作延迟，固定向量结果准确。
- [ ] 报告能区分 target/executor/network/scenario/internal，并携带免责声明；legacy 指标缺失不阻断默认 verdict。
- [ ] SSE Last-Event-ID、慢消费者、快照补偿、终态关闭全覆盖。
- [ ] JSON/CSV 报告字段一致，运行未终态返回 409。
- [ ] Go tests/race、router/service 集成测试全绿；旧 BotStressSession 测试回归。

### 真机

- [ ] stable：500+ 连续60分钟，严格阈值判 passed；人工制造阈值失败时判 failed/aborted 原因正确。
- [ ] step：按 100→250→500→更高档推进，首次失败停止升压，maxStableBots 正确。
- [ ] spike：500 Bot 在配置连接窗口内发起，屏障/攻击峰值 p50/p95/p99 可见。
- [ ] 发压节点过载与目标服过载分别制造一次，分类正确。
- [ ] 安全停止在危险持续窗口后有序收束，报告保留。
- [ ] JSON/CSV 默认指标与真机运行证据一致；配置 legacy 适配器时，其附加指标独立核对且不改变默认 verdict。

## 6. 风险 / 待定

- 500+ 真机指标需要足够 Worker 和目标测试服；没有环境不得把 mock 判为完成。
- 现有 SQLite 在高并发下需避免每 Bot 高频事务，本设计用内存聚合+5秒单行采样；race/压力测试必须验证。
- CP 重启恢复依赖 FR-354 desired-state；若 FR-354 仍 partial，本 FR 不得标 done。
- 不新增统计库/消息队列/图表依赖。
