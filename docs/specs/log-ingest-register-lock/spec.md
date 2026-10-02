# 采集登记路径与长临界区解耦（FR-499）

> 状态：🔨 开发中　·　关联 PRD：FR-499　·　分支：feat/log-platform-hardening
> 关联 ADR：ADR-094（Worker 日志本地数据面）、ADR-095（磁盘权威事件存储）、ADR-093（进程生命周期韧性）
> 依赖：FR-473~484（采集/投递/投影契约）、FR-496（采集索引 SQLite）、FR-497（启动增量对账）
> 被依赖：无（本项为事故根因修复，不改任何对外契约字段）

## 1. 背景与目标

### 1.1 事故（2026-10-01）

CP 下发实例规格时，Worker 的 `CreateInstance` 中「登记实例日志采集」这一步**持续 DeadlineExceeded**，同日 07:39 / 07:46 / 07:49 / 08:44 四次实测全超时（不是偶发）；同期实例**进程侧已能对账**（状态从 CRASHED 收敛为 STOPPED），**只有这一步挂住** → 11 台实例无法启动，且该步失败使 CP 的启动预检直接终止。

调用链（固定截止时间在 CP 侧）：

```
CP  InstanceService.registerOnWorkerLocked   ctx = 10s（instance.go）
 └─ Worker grpc.Server.CreateInstance         （server.go）
     └─ registerInstanceFromProto             （server.go）
         └─ registerInstanceLogs              （log_acquisition.go）
             └─ ingest.Manager.RegisterInstance   ← 这一步挂住
     └─ manager.Create(...)                   ← 因上一步失败而根本不执行
```

### 1.2 根因

`RegisterInstance` 以 `m.cycleMu` 作为入口互斥，而 `cycleMu` 的持有者是**整轮采集**：

```
pollOnce()  { cycleMu.Lock(); … for 每个源 { pollSource } … cycleMu.Unlock() }
             pollSource → Pipeline.Poll()  → WAL.Append/Commit
                        → m.persist()       （m.mu 内整段索引写入）
                        → m.deliver()       （VL 插入 + 投影校验，校验退避窗口默认 5 分钟）
                        → releaseRecovery   （段覆盖/回收责任推进）
```

采集侧处于**吞吐饱和**（实测上限 ~1150–1250 行/s、每源 ~20 行/s，`tailer` 单源单次 poll 上限 2000 行）时，单轮 `pollOnce` 在秒级到分钟级，`ticker` 250ms 使两轮之间几乎没有空档 → `cycleMu` 近乎连续被持有。于是**任何**取 `cycleMu` 的调用（登记、预备切换、解缺口、停止）都会被整轮采集拖住；登记的 10 秒 RPC 截止必然到点。

同一根因的另一条生产证据：`PrepareCutoverReadiness`（取 `cycleMu`，做全管道 `Flush` + `persist`）在忙期同样需**分钟级**才返回（2026-09-30 多次 PUT 挂起实测）。

### 1.3 目标

**让「实例登记 / 采集绑定」不再被采集轮的长临界区阻塞**：登记的等待面收敛到「短临界区」（建管道、写索引），与单轮采集时长**解耦**。

- **范围内**：`ingest.Manager` 的锁纪律（新增 `registerMu`、登记路径改锁、`Stop` 串行化）、锁审计工具与忙期量化、登记路径回归测试、本文档。
- **不做**：
  - **不改登记语义**——登记仍是**同步**的（返回即已建管道、已落库）；不做「快速受理 + 异步落地」，理由见 §3.5。
  - 不改采集轮内部结构（`pollOnce` 仍持 `cycleMu` 跑完一轮）——把长操作拆小会引入「轮与轮交错 / 与 Stop 交错」的新语义，收益不及风险。
  - 不改索引表结构、不改任何 proto / API 字段、不改 CP 侧代码。
  - 不解决 §3.4 列出的**残留等待面**（`m.mu` 与 `pendingMu` 的短临界区），但给出实测上界。

## 2. 锁审计（改动前的现状真貌）

审计方法：AST 级扫描（`.tmp/fix-register-lock/audit`，按 `Lock/RLock` 调用点定位函数、持有区间与区间内的长操作调用）→ 人工判定长临界区 → 饱和负载下用 `TryLock` 采样量化持有时间。

### 2.1 三把锁的职责

| 锁 | 保护对象 | 持有者 |
|---|---|---|
| `mu` | `sources` / `pipes` / `state`（含 `state.Instances`）/ `persistedRev` / `persistedCover` / `index` / 各报告字段 | 几乎所有方法（**每处都只做 map/字段读写**），例外见下表最后一行的 `persist` |
| `cycleMu` | 「采集轮 / 预备切换 / 解缺口 / 停止」之间的互斥（**同一时刻只允许一个长流程**） | `pollOnce`、`PrepareCutoverReadiness`、`ResolveCoveredGaps`、`ResolveCoveredGapsForSource`、`Stop`、**（改动前）`RegisterInstance`** |
| `pendingMu` | 未绑定实例的暂存写入与「绑定接管」之间的互斥（保证回放期间没有输出插进中间） | `AppendInstanceOutput`、`RegisterInstance`、`recoverPendingSpools`、`pendingSpoolState`、（改动后）`SetPendingSpoolLimits` |

> **2026-10-02 修订（压测现场 E1，本表随之收窄）**：`ResolveCoveredGaps`（整节点解算）**不再整段持有 `cycleMu`**——它改为「离锁投影查询 + 每源一段短临界区」，故上表「一次只允许一个长流程」对解算路径已不成立：解算与采集轮只在**单源变更段**（毫秒级）互斥，采集轮在解算进行中照常推进。`ResolveCoveredGapsForSource`（放弃裁定）仍是单源短流程、整段持锁（无投影查询，代价与单源同阶）。判据（`STDIO_RAW_WRITE_FAILED` 拒绝、投影完整性、覆盖水位）与 `ResolveGapsThroughExcept` / `VerifiedRuns` 语义逐字未改。
> 量化口径（合并门禁「长临界区审计」要求）与 FR-499 同：直接测「被它挡住的那件事」的时长，不用 `TryLock` 探锁。回归 `TestResolveCoveredGapsLockHoldIsBoundedPerSource`（`internal/worker/logs/ingest/resolve_gaps_lock_test.go`）实测：解算总时长 **372ms** 的同一时段内，采集轮**最坏单轮 0.89–1.37ms** ⇒ 争用上界毫秒级，与解算总时长彻底解耦；旧形态（变异复刻整段持锁）下同一断言实测转红（采集轮单轮 **364.8ms ≈ 解算总时长**）。同批另有「解算进行中采集不停」「ctx 取消即停且不落变更」「预算有界且可续跑」三条可转红回归。

### 2.2 持有跨越长操作的临界区（改动前；解算两行的现状见 §2.1 修订注）

行号为改动前的位置（改动后同一逻辑 +19 行，见 §3）。

| 文件:行 | 锁 | 临界区内的长操作 | 判定 | 忙期实测持有 |
|---|---|---|---|---|
| `runtime.go:765` `pollOnce` | `cycleMu` | 整轮逐源 `pollSource`：`Poll`（单源 ≤2000 行）→ `persist`（索引写入）→ `deliver`（VL 插入 + 投影校验，退避窗口默认 5 分钟）→ `releaseRecovery` | **长**（秒级~分钟级，饱和期几乎连续持有） | **3.15s**（6 源 × 600 行积压、VL 插入 200ms/批）；4 源同参数夹具 **2.06s** |
| `runtime.go:272` `PrepareCutoverReadiness` | `cycleMu` | 全管道 `pipe.Flush()` + `m.persist()` | **长**（源数线性） | 生产实证分钟级（2026-09-30 PUT 挂起） |
| `runtime.go:712` `Stop` | `cycleMu` | 全管道 `Flush` + `persist` + `CloseIndex`（关库并归并 WAL，实测 WAL 归并可达数百 MB） | **长** | 与 Flush 规模同阶 |
| `runtime.go:301` `ResolveCoveredGaps` | `cycleMu` | 逐源 `Ledger` 读写 + `BindRecoverySegment`/`TryReclaim` + 末尾 `persist` | **长** | 未量化（运维显式动作，非事故路径） |
| `runtime.go:382` `ResolveCoveredGapsForSource` | `cycleMu` | 同上（单源）+ `persist` | **长** | 未量化 |
| `instances.go:114` `RegisterInstance` | `cycleMu` | 建管道（`m.Register` 逐流）+ `flushPendingForBinding`（spool 可能达单流 64MB 的 `io.Copy` 主体）+ `persist` | **判定为「被长临界区阻塞的受害者」**（自身不快，但**致命的是取锁即排队到整轮结束**） | 改动前登记耗时 = **1.00s 到点判超时**（真实需要等完整轮 **3.1s**，见 §5） |
| `runtime.go:2283` `persist` | `mu` | **整段** `stateToIndexStateScoped` + `index.ApplyScoped`（SQLite 事务提交）都在锁内 | **中**（按变更源收敛；无变更轮只比对根表） | **48–51ms**（饱和期单次最长，采样含 13 次释放观测） |
| `runtime.go:611` `Register` | `mu` | 建管道；**旧状态存在时**在锁内做 `led.Restore` / `wal.Restore(Mixed)`（含 `eventBodyLookup` 段存储磁盘读） | **中**（新源无恢复，为空操作） | 未量化（受该源 WAL 条数约束） |
| `runtime.go:124`→`instances.go` `RegisterInstance` | `pendingMu` | `flushPendingForBinding` 的 `io.Copy`+`fsync`+`os.Remove`，**并跨 `persist()`** | **中**（自持，非等锁；挡的是未绑定实例的输出写入） | 未量化 |
| `runtime.go:546` `pendingSpoolState` / `recoverPendingSpools` | `pendingMu` | `os.ReadDir` 递归 pending 目录 + `flushPendingForBinding` | 中（同上） | 未量化 |

### 2.3 短临界区（无长操作，登记路径会与之竞争的全部对象）

`instances.go` 38/116/159/166/215/391/446/450/454；`reconcile.go` 169/449/478/491/509/513/530；`runtime.go` 140/151/205/250/274/303/322/384/494/527/534/714/749/767/940/966/1065/1112/1153/1185/1201/1820；`index_store.go` 690 —— 均为 map/字段读写，单次微秒级。

**结论**：登记路径的**唯一**致命等待是 `cycleMu`；`mu` 与 `pendingMu` 的最长持有分别实测 48–51ms 与（无量化）同阶，均远小于 10 秒截止。

### 2.4 量化方法（以及一处方法学纠正）

- `cycleMu` 的持有时间用**直接测量**：显式驱动一轮 `pollOnce`（该函数全程持 `cycleMu`），测本轮墙钟时长 = 该锁的单次持有时长。
- **不要用 `TryLock` 探锁来量化 `cycleMu`**：Go 的 `sync.Mutex` 在有等待者排队超过 1ms 后进入**饥饿模式**，所有权由 `Unlock` 直接交给队首等待者且 `mutexStarving` 位保持置位，而 `TryLock` 一见该位即返回 false——**即便锁在那一瞬是空的**。持续有排队者的场景（后台采集轮 + 250ms ticker 永远排着下一个 `pollOnce`）下，探针会连续上万次全部失败，读出「锁从未释放」的假象。本项第一版回归正是据此误判（一次 `-race` 运行中采样器 15 361 次失败、0 次释放），已改为直接测轮时长。
- `mu` 的持有时间仍用 `TryLock` 采样（它在忙期没有持续排队者——除采集自身外只有登记偶发取它，队列瞬时清空、不进入饥饿模式），并同时上报「观测到的释放次数」以证读数有效（零释放的读数一律视为无效）。

## 3. 设计

### 3.1 新增 `registerMu`：登记自成一锁，永不等待 `cycleMu`

```go
type Manager struct {
    mu       sync.Mutex
    cycleMu  sync.Mutex   // 采集轮 / 预备切换 / 解缺口 / 停止
    registerMu sync.Mutex // 登记与采集绑定（新增）
    pendingMu  sync.Mutex
    …
}
```

- `RegisterInstance` 把入口互斥由 `cycleMu` 换成 `registerMu`（**唯一的行为改动点**）。
- `Stop` 在原 `cycleMu` 之内补取 `registerMu`，把「关闭采集索引」与登记串行开——否则登记可能在 `CloseIndex` 之后进来 `persist`，把索引句柄重新打开却无人关闭（WAL 残留）。
- **锁序（任何路径不得逆序）**：`cycleMu → registerMu → pendingMu → mu`。`registerMu` 永远不等待 `cycleMu`（否则本锁失去意义）；`Stop` 是唯一同时持有两者的路径。

### 3.2 为什么登记不需要 `cycleMu` 的互斥（不变量逐条）

改动前 `cycleMu` 在登记路径上只提供「与采集轮互斥」，而该互斥对登记**并非必要**：

| 不变量 | 保障者 | 登记与采集轮并发时是否被破坏 |
|---|---|---|
| `pipes`/`sources`/`state` 读写原子 | `mu` | 否——读写都在 `mu` 内，且 `pollOnce` 对本轮管道集合是「`mu` 下快照、快照外轮询」 |
| 新登记的源**何时**参与采集 | `pollOnce` 的快照对象 | 否——最坏延到下一轮（新源首轮无数据），**不丢采集**（首轮 `Poll` 会读到从 0 起的全部内容） |
| 索引一致性（同一时刻只有一个写者） | `persist` 全程 `mu` | 否——两个 `persist` 串行 |
| 绑定变更与暂存回放的顺序 | `pendingMu` | 否——登记路径仍持 `pendingMu` 跨「写 `state.Instances` + 回放暂存」 |
| 账本/索引基线（`persistedRev`/`persistedCover`） | `mu`（`Register` 全程持锁） | 否 |

即：登记与采集轮之间的**唯一**共享状态都在 `mu`/`pendingMu` 保护之下；`cycleMu` 在登记路径上只是「顺带」被拿的入口锁，去掉它不损失任何正确性。

### 3.3 与「预备切换 / 解缺口」的并发（行为变化，如实声明）

改后登记可与 `PrepareCutoverReadiness` / `ResolveCoveredGaps*` 并发（此前互斥）：

- 二者都会 `persist`，由 `mu` 串行；二者对 `pipes` 的取用都是 `mu` 下快照。
- 极窄窗口内「新登记的源」可能不在该次预备切换的 `Flush` 集合里。该源此时**必然没有已读未投数据**（管道刚建、首轮 poll 尚未跑或正在跑），故「切换就绪」判据不受影响；`CutoverReadiness` 每次 PUT 都会重算（CP 侧是 `PUT` + 轮询语义），下一拍即覆盖。
- 取此权衡的理由：反向的旧行为（登记必须等分钟级切换完成）正是本事故的形态——**用一个假想的窄窗口正确性换取确定性的可用性损失**，不划算。

### 3.4 残留等待面（如实标注）

改后登记的等待面 = `registerMu`（登记之间自排）+ `mu`（建管道、`persist` 整段）+ `pendingMu`（仅暂存回放的 `stat`/`copy`）：

| 残留 | 实测上界（饱和夹具） | 是否可能触及 10s 截止 |
|---|---|---|
| `mu`（`persist` 整段） | 48–51ms | 否（两个数量级余量；`persist` 成本已按「变更源」收敛，见 FR-496） |
| `mu`（`Register` 新源） | 微秒级（新源无 `Restore`） | 否 |
| `pendingMu`（`flushPendingForBinding`） | 取决于 spool 体积（单流上限 64MiB） | 极端情况（64MiB `fsync`）会到秒级，但仍 < 10s |

上表由 `TestRegisterInstanceLatencyBudgetUnderSaturation` 持续观测：它打印忙期单轮时长（= `cycleMu` 持有）、`m.mu` 单次最长持有与登记耗时，作为「忙期画像」的常驻取证。

### 3.5 未采纳：登记改为「快速受理 + 异步落地」

评估过（父任务允许的方向 (c)），**不采纳**：

- 语义代价：CP 现有契约是「`CreateInstance` 返回 200 ⇒ 采集绑定已生效」。改成「受理即返回」后，绑定失败只能异步告警，而 CP 的启动预检、`MissingInstanceBindings` 的 `acquisition_not_bound` 判据都会失去同步保证。
- 收益为零：本事故的阻塞源是**等锁**而非登记本身的开销（实测登记本体 0.1–4.6ms）。去掉 `cycleMu` 后同步登记已在毫秒级返回，异步化不带来额外收益。
- 若将来「登记本体」变贵（例如必须同步等待外部系统），再按 (c) 改造，届时需一并给 CP 配套（见 §6）。

### 3.6 对 CP `CreateInstance` 响应语义的影响（明确回答）

**无影响**（这是本项刻意的取舍）：

- Worker 侧 `RegisterInstance` **仍是同步**调用，`CreateInstance` 仍是「成功返回 ⇒ 实例已登记 + 日志采集已绑定并落库」。
- **不变**：proto / RPC 字段、返回结构、错误语义（失败仍是 `Success=false` + `Error`，CP 仍作为启动预检的终止条件）。
- **变**：同一份成功语义下的**响应时间**——由「等整轮采集结束（秒级~分钟级）」变为「等短临界区（毫秒级）」。CP 侧**无需任何配套改动**；10 秒截止从「必然到点」回到「仅在与自身持久化竞争时才可能接近」。
- 反向约束（写进验收）：若日后有人把登记改成异步，**必须**同步改 CP：`CreateInstance` 需改为「受理 + 轮询/回调」，并在启动预检里把 `MissingInstanceBindings` 的语义从「尚未登记」改为「尚未落地」。

## 4. 任务拆分

- [x] 锁审计工具与行级审计表（`.tmp/fix-register-lock/audit`、本文 §2）
- [x] 饱和负载下量化忙期持有时间（`cycleMu` 3.15s / `m.mu` 48–51ms，见 §2.4 与 §5）
- [x] 新增 `registerMu`、登记路径改锁、`Stop` 串行化
- [x] 可转红回归：`TestRegisterInstanceNotBlockedBySaturatedPollRound`（两个子用例）+ `TestRegisterInstanceLatencyBudgetUnderSaturation`；**逐字还原旧实现实测转红**后再还原修复
- [x] 门禁：`go build ./...`、`CGO_ENABLED=0 go build ./apps/worker`、`gofmt`、`go vet`、`go test ./internal/worker/logs/... -count=1`、`go test -race ./internal/worker/logs/ingest/... -count=1`
- [x] 文档同步：本文 + PRD FR-499 登记

## 5. 验收标准

1. **登记与整轮采集解耦**（可转红）：在**显式驱动**的一轮忙期采集（单轮时长 > 登记截止）进行中调用 `RegisterInstance`，必须满足两条：
   - 登记**在该轮结束之前**返回（旧实现取 `cycleMu`，必然等整轮跑完）；
   - 登记耗时 < 1s 截止，且 < 该轮时长的 1/4（`TestRegisterInstanceLatencyBudgetUnderSaturation`）。
   - 实测（`6 源 × 600 行`、VL 插入 200ms/批）：忙期单轮（= `cycleMu` 单次持有）**3.15s**；登记耗时 **新实现 0.12–4.6ms** / **旧实现 1.00s（到点判超时；真实需等满整轮 3.15s）**。
2. **覆盖两条真实调用形态**：`new_binding`（首次下发规格：建管道 + 落库）与 `idempotent_rebind`（CP 每次启动前的幂等重注册——事故当场的实际形态）都必须通过。
3. **无丢失绑定**：登记返回后，`MissingInstanceBindings` 为空、两条流源管道存在、**且绑定已落索引库**（用例直接读索引库校验 `instance_binding` 行，不只查内存）。
4. **无数据竞争**：`go test -race ./internal/worker/logs/ingest/... -count=1` 全绿。
5. **采集不受影响**：并发登记之后追加日志行仍能被采集并投递（用例断言 VL 插入计数增长）。
6. **一致性测试全绿**：`go test ./internal/worker/logs/... -count=1`（含既有全部账本/索引一致性用例）。
7. **门禁**：`go build ./...`、`CGO_ENABLED=0 go build ./apps/worker`、`gofmt -l`（改动面）、`go vet ./internal/worker/logs/...` 全绿。
8. **长临界区纪律（本次事故换来，评审必查）**：按 `.claude/rules/gate-merge.md`「长临界区审计」执行——长流程不得持共享锁跨越 `Poll` / 投递 / 持久化，锁获取点须标注锁序与最坏持有量，跨进程调用（gRPC/HTTP）不得在持锁区内，短路径与长流程分锁；本次改动后新增/修改的任何锁同样适用。

## 6. 风险 / 待定

- **`m.mu` 仍是登记路径上唯一可能显著增长的自持锁**（`persist` 整段在锁内）。当前实测 48ms；若 60 源规模下 `persist` 的单次耗时显著上行（FR-496 验收判据是 ≤50ms，FR-498 将给出 60 源实测），登记延迟会随之上行。**后续可选**：把 `persist` 的「快照 → 应用」拆开（`mu` 只护快照），或给登记一条不落全量根表的快路径。本项不做（改动面与风险都不小，且当前有 200 倍余量）。
- **登记路径自持 `pendingMu` 跨 `persist`**（改动前既有形态）：挡住的是「未绑定实例的输出写入」，不影响登记自身耗时；但对「大量未绑定实例持续输出」场景是写放大点。已在 §2.2 标出，未改。
- **`Stop` 之后仍可能有登记排队进来**（`registerMu` 只能串行、不能拒绝）：此时 `persist` 会在索引句柄关闭后重新打开它。属**改动前既存**形态（`recordRawWriteFailure` → `persist` 本来就不持 `cycleMu`），本轮未扩大；若要让 `Stop` 后登记明确失败，需引入 `stopping` 标志（本项不做）。
- **未量化项**：`ResolveCoveredGaps*` 与 `Stop` 的忙期持有时间（运维显式动作，不在事故路径上）。
