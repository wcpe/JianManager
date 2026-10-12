# 重启增量对账（源 × UTC 天，条数级）（FR-497）

> 状态：🚧 开发中　·　关联 PRD：FR-497　·　分支：feat/log-platform-hardening
> 关联 ADR：ADR-094（Worker 日志本地数据面）、ADR-095（磁盘权威事件存储）、ADR-093（进程生命周期韧性）
> 依赖：FR-496（采集索引 SQLite 与 `projection` 记账面）　·　被依赖：FR-498（规模压测的「60 源重启 ≤60s」判据）

## 1. 背景与目标

**现状（整窗重发）**：Worker 启动时 `ingest.New()` 的恢复段对**每个源**调用 `canonicalRecoveryEvents`（事件段 + durable WAL 的权威全量），只要该集合非空且 VL 客户端可用，就无条件生成新 generation 并 `writeProjection(..., replace=true)`：

1. 把该源**全部事件**按 UTC 天重新插入 VL（`insertInBatches`，500 条/批）；
2. 对写下去的每一条做**逐条内容校验**（`verifyProjection`：event_id + canonical_content_hash + `_time`/`_msg`/`level`/`stream`）；
3. 发布新代（catalog 该天白名单只留新代）、补段存储、推进投递责任与回收。

这是「安全但昂贵」的：它同时承担了「VL 数据根丢失后的重建」与「日常重启」两种场景。生产实测口径：**13 源重启重发 ≈5 分钟**；按目标形态 **60 实例**外推将达十几至几十分钟，重启窗口内查询侧看到的是旧数据、采集侧被启动阻塞。

**目标**：启动时按「**源 × UTC 天**」与 VL 做**条数级对账**——账本侧应发条数 vs VL 侧实有条数（VL `stats count()` 查询，只读）：

- 两侧一致 → **零重发**（不生成新代、不写 VL、不改 catalog）；
- 某天 VL 条数不足 → **只重发该天**；
- VL 不可达 / 对账失败 → **回退现有整窗重发**（安全兜底：绝不因对账失败而少发）。

**口径**：**绝不丢、允许极少重复**（重复由查询侧去重与游标吸收，见 `query/vlrange` 的 `event_id` 去重与 `record_*` 游标语义）。

**范围内**：启动恢复路径（`ingest.New` 恢复段）+ 对账查询 + 增量重发（只写缺失天）+ 配置项 + 回归测试。
**不做**：

- 不改投递/发布语义（新代仍 `replace` 取代、写入后仍逐条内容校验）、不改运行期 `pollOnce` 投递路径；
- **不做内容级（逐条）对账**——条数相同而内容缺失/被替换**无法**由本 FR 检出，见 §2.4 口径与检出方式；
- 不改 FR-496 的索引表结构（复用 `projection` 记账面：`generation` / `events_stored_through` / `pending`，**不新增表、不改 DDL**）；
- 不改 `apps/worker/main.go` 与 `internal/worker/config.go`（本次改动面限制）→ `log_reconcile.*` 的 YAML 接线已于 2026-10-01 完成，见 §5「接线状态」。

## 2. 设计

### 2.1 对账口径（源 × UTC 天）

对每个「权威集合非空」的源：

| 侧 | 取值 |
|---|---|
| 账本侧「应发」`N(day)` | `canonicalRecoveryEvents(key, saved)`（**与现状同一函数**：事件段存储 + durable WAL，去重、按登账顺序）按 `eventUTCDay` 分组的条数 |
| VL 侧「实有」`M(day)` | `/select/logsql/stats_query`（只读）执行 `<selector> \| stats count()`，时间窗 = 该 UTC 日 |

**selector 与查询侧同口径**（`query/vlrange.selectorQuery`），即「对账测的是查询侧能不能看到」：

```
log_source_id:="<id>" AND source_generation:="<gen>"
  [ AND (projection_generation:="g1" OR projection_generation:="g2" ...) ]
  [ AND record_end:<=<ClosedVisibleSeq> ]
```

- 代次白名单与封闭水位取自 **catalog 该天记录**的该源 scope（`PublishedProjection.SourceProjections[].ProjectionGenerations` / `ClosedVisibleSeq`）。**白名单不可得**（该天无记录 / 该源 scope 缺失 / `ProjectionGenerations` 为空）→ 不查询、直接判该天缺失（保守重发）：无法证明「查询侧看得到」，就不接受它。
- 时间窗用 `utcDayBounds`（`[日首, 日首+24h-1ns]` 闭区间）。若 VL 视 `end` 为开区间，最坏漏掉末纳秒的 1 条 → 判「不足」→ 重发该天，**方向安全**（重复可被吸收，丢失不可接受）。

**判定**：

| 条件 | 处置 |
|---|---|
| `N(day) == 0` | 跳过该天（无可对账数据） |
| `M(day) >= N(day)` | 一致（含「VL 里有多代重复」的合法情形） |
| `M(day) < N(day)` | **缺失** → 该天进入重发集合 |
| catalog 无该天记录 / 该源 scope 缺失 | 视为**缺失**（无法证明 VL 有数据 → 保守重发该天） |
| 查询失败 / 超时 / 响应不可解析 | **回退整窗重发**（该源整体） |

**回退（整窗重发）触发条件**（任一命中即回退）：

1. `log_reconcile.enabled=false`（显式关闭）；
2. `saved.PublicationPending == true`（上次发布未完成 → 发布状态不确定，不做增量裁剪）；
3. `m.vl == nil` 且 `m.vlRoute == nil`（无 VL 客户端——此时现状本就跳过恢复重发，行为不变）；
4. 取不到 VL 客户端（`clientForSource` 报错）；
5. 权威集合按 UTC 天分组失败；
6. 任一天的 count 查询失败/超时/解析失败。

### 2.2 增量重发（只写缺失天）

新增内部写入计划 `projectionWritePlan`，把现状 `writeProjection(..., replace)` 的「写入面」与「发布语义」解耦：

| 字段 | 语义 |
|---|---|
| `Write` | 本次实际写入 VL 的事件集合 |
| `Archive` | 同时落归档的权威事件集合（现状恢复路径为 nil） |
| `Replace` | 发布语义：`true` = 新代**取代**旧代白名单（该天）；`false` = 累积微代次 |
| `Days` | **非空 = 限定写入面**为这些 UTC 天；此时 `Replace` 不再把写入面扩为权威全量（即「整窗」）。集合必须与 `Write` 的天集合一致，否则硬失败（防「标为已发布却没写数据」） |

现状语义完全保留：`Days` 为空 + `Replace=true` → 写入面扩为权威全量（整窗重发）；`Days` 为空 + `Replace=false` → 按 `Write` 的天写入。

增量重发 = `projectionWritePlan{Write: 缺失天事件, Replace: true, Days: 缺失天}`：

- 只对缺失天执行「写 VL → 逐条内容校验 → publish（该天白名单 = 新代）」；
- **未缺失天完全不触碰**（catalog 记录与 VL 数据原样保留 → 查询侧仍可见）；
- 权威全量仍传入 `events` 参数：用于 `appendEvents`（段存储幂等补齐）与 persist。

**零重发**（无缺失天且无回退）：不生成新 generation、不写 VL、不改 catalog、不推进回收责任；仅当该源投递状态为 `UNKNOWN` 时用既有 `ResolveUnknownThroughProjection` 解算（对账已证明数据在 VL，与该函数既有语义「投影已持久即可解算」一致）。

### 2.3 并发与超时

- 对账按**源**并发（`log_reconcile.concurrency`，默认 4，上限 32）；源内按天串行（每天一次 count 查询，天数量通常 1–3）。
- 单源对账总超时 `log_reconcile.timeout`（默认 30s）；单次 count 查询超时 `log_reconcile.query_timeout`（默认 10s，经 `context.WithTimeout` 注入，不受 vlsup 查询客户端 60s 默认超时支配）。
- **整批总预算 `log_reconcile.budget`（默认 60s，2026-10-02 复审 P2-10）**：对账在 `ingest.New` 里**同步**执行，只有「单源 timeout + 并发」两道约束时，最坏情况（60 源全部卡在网络查询上）≈ `ceil(60/4)×30s = 7.5 分钟`，启动恢复被拖成分钟级。预算到期即取消 ctx（在途查询立刻收手），**未完成判定的源按最保守方向回退整窗重发**（`FallbackReason=reconcile_budget_exhausted`）——预算只影响「重发多少」，绝不影响「要不要重发」。回归：`ingest.TestStartupReconcileHonorsTotalBudget`（去掉预算即红：实测 4.0s vs 200ms）、`ingest.TestCloseReconcileBudgetNeverSkipsReplay`。
- 任一源对账失败只影响该源（回退整窗重发），**不阻塞**其它源与启动主流程；整体上界 = 总预算。
- 对账结论逐源落 `slog`，并保留在 `Manager.LastReconcileReports()`（只读，供运维与回归取证）。

### 2.4 条数级口径的边界（内容缺失不可检出）

条数级对账**不比较内容**：若 VL 中某天条数相同而内容缺失/被替换（同一事件被替换为另一 `event_id`，或消息/级别被改写），对账判「一致」→ 零重发。这是本 FR 的**已知边界**，不是缺陷：

- **口径**：本 FR 只承诺「不丢**条数**」；内容一致性由**写入路径的逐条校验**（`verifyProjection`，写入后立即执行）承接。
- **检出方式**（本 FR 不自动做、可人工/工具触发）：① 既有 `verifyProjectionOnceWithClient` 可对指定事件集做内容级校验（回归 ④ 用它证明「内容缺失能被内容级路径检出，而条数级对账不检出」）；② 人工全量重建（重启前把 `log_reconcile.enabled=false` → 回退整窗重发）。
- 对账结论中 `Basis` 恒为 `count_only`，显式表达「本次判定只做了条数级比较」，避免被误读为内容级确认。

### 2.5 与 FR-496 记账面的关系（不新增表）

- 对账**读** `projection` 行的 `generation`（当前代次 → 派生新代 `projection-N+1`）与 `pending`（发布待定 → 直接回退）；`events_stored_through`（段覆盖水位）用于确认权威集合已落段（现状 `canonicalRecoveryEvents` 已依赖段存储判据）。
- 不新增持久表、不改 DDL：对账结论是**启动期的瞬态判定**（内存 + 日志），重发后的结果仍由既有 `writeProjection`/`publish` 落账。

## 3. 验收标准

| # | 验收项 | 方式 | 回归测试名（已跑通 + 已做变异验证） |
|---|---|---|---|
| 1 | 人为删掉 VL 某天数据 → 启动对账检出，且**只补该天**（其它天零投递） | 回归 ① | `TestStartupReconcileReplaysOnlyDeletedUTCDay` |
| 2 | 两侧一致 → **零重发**（重启后无任何多余投递） | 回归 ② | `TestStartupReconcileSkipsReplayWhenCountsMatch` |
| 3 | VL 不可达 / 对账查询挂起 → 回退**整窗重发**且不卡死（受 `timeout`/`query_timeout` 约束） | 回归 ③ | `TestStartupReconcileFallsBackToFullReplayWhenVLUnreliable`（两子用例：对账查询挂起 / VL 完全不可达） |
| 4 | 条数相同、内容缺失 → 对账判「一致」（`Basis=count_only`）且内容级路径可检出该缺失（口径显式） | 回归 ④ | `TestStartupReconcileCountsOnlyAcceptsEqualCountWithMissingContent` |
| 5 | 零行为变更：现有 ingest/ledger/stateindex 全套测试绿；`gofmt`/`go vet` 干净 | 命令 | `go test ./internal/worker/logs/... -count=1` |

**变异验证（「为什么能转红」的实测口径）**：把实现临时改回旧行为后，上述回归必须失败；本批实测过三条变异：

| 变异 | 期望转红 | 实测 |
|---|---|---|
| `reconcileSource` 恒回退（= 无条件整窗重发，旧行为） | ① ② ④ 失败 | ✅ 三者 FAIL |
| `applyStartupRecovery` 在回退时直接返回（= 对账失败即不重发） | ③ 两子用例失败 | ✅ 两子用例 FAIL |
| `countVLDay` 忽略 `query_timeout`（= 无单查询超时） | ③「对账查询挂起」耗时断言失败 | ✅ 5.01s ≫ 3s 界，FAIL |

**配置归一化 / 逃生口 / 解析兼容**另有独立用例：`TestStartupReconcileDisabledFallsBackToFullReplay`（关闭对账 = 现状整窗重发、零查询）、`TestParseStatsCountShapes`、`TestSameUTCDaysRejectsMismatch`、`TestReconcileConfigNormalization`。

## 4. 任务拆分

- [x] T1 `projectionWritePlan` 拆分（写入面 / 发布语义解耦），现状语义零变更
- [x] T2 对账器：按源×UTC 天 count 对账 + 双超时 + 并发 + 报告
- [x] T3 `New()` 恢复段改造：三阶段（登记 → 并发对账 → 按源重发/零重发）
- [x] T4 配置面 `ReconcileConfig` + 默认值 + 归一化（越界回退默认）
- [x] T5 四条回归（删天补回 / 一致零重发 / 不可达回退 / 条数相同内容缺失）
- [ ] T6 真机验证（60 源重启 ≤60s）——属 FR-498 批次，本 FR 只交付可回归判据

### 4.1 本批实现状态与未做项（2026-10-01）

**已落地**（改动面：`internal/worker/logs/ingest/`，未触碰 `ledger/`、`query/`）：

- `reconcile.go`（新增）：`ReconcileConfig` + 归一化、按源并发对账、`countVLDay`（只读 `stats_query`）、`parseStatsCount`（双形态兼容）、`publishedScope`、`applyStartupRecovery`、`LastReconcileReports`；
- `runtime.go`：`Options.Reconcile` + Manager 字段、`New()` 恢复段三阶段化、`writeProjection` → `writeProjectionPlan`（写入面/发布语义解耦，现状语义零变更）；
- `reconcile_test.go`（新增）：四条回归 + 逃生口/解析/错位/归一化用例。

**未做（如实登记）**：

1. **`log_reconcile.*` 的 YAML 接线**：生效路径目前只有 `Options.Reconcile`（默认值已在包内生效）。接线需改 `internal/worker/config.go` 与 `apps/worker/main.go`，超出本批改动面。
2. **`stats_query` 返回形态的真机验证**：本批无真机可验，解析器兼容两种已知形态并对歧义硬失败（回退整窗重发）；真机验证随 T6/FR-498 批次做。
3. **真机「60 源重启 ≤60s」**：T6，需真机（本批仅交付可回归判据）。
4. **FR-497 的另一半「全自动收养」**（存活 wrapper 自动重连、未纳管活进程自动收养为 RUNNING）：不在本批范围，仍待立项。

## 5. 配置表

| 键 | 默认 | 语义 | 接线状态 |
|---|---|---|---|
| `log_reconcile.enabled` | `true` | 启动增量对账开关；`false` = 回退整窗重发（现状） | `ingest.Options.Reconcile.Enabled`（包内生效）；**YAML 接线待做**（需改 `internal/worker/config.go` + `apps/worker/main.go`，超出本 FR 改动面） |
| `log_reconcile.concurrency` | `4` | 并发对账的源数上限；越界/非正 → 回退 4（上限 32） | 同上（`Options.Reconcile.Concurrency`） |
| `log_reconcile.timeout` | `30s` | 单源对账总超时；≤0 → 回退 30s | 同上 |
| `log_reconcile.query_timeout` | `10s` | 单天 count 查询超时；≤0 → 回退 10s | 同上 |
| `log_reconcile.budget` | `60s` | **整批**总预算（2026-10-02 复审 P2-10）；≤0 → 回退 60s。到期取消在途查询，未完成判定的源回退整窗重发 | 同上 |

**关键常量**：

| 常量 | 值 | 位置 | 语义 |
|---|---|---|---|
| `reconcileMaxConcurrency` | `32` | `reconcile.go` | 并发上限（防误配打爆 VL 查询面） |
| `reconcileBasisCountOnly` | `count_only` | `reconcile.go` | 判定依据标记（显式表达未做内容级校验） |
| `reconcileDefaultBudget` | `60s` | `reconcile.go` | 整批对账总预算默认值 |
| `reconcileBudgetExhausted` | `reconcile_budget_exhausted` | `reconcile.go` | 预算耗尽的回退原因标记 |
| VL 端点 | `/select/logsql/stats_query` | `reconcile.go` | 只读 stats 查询（`\| stats count()`） |
| 时间窗 | `utcDayBounds` 闭区间 | `runtime.go` | 与既有校验路径同日界口径 |

## 6. 风险与缓解

| 风险 | 缓解 |
|---|---|
| `stats_query` 返回形态在真机未验证 | 解析器同时兼容 Prometheus 向量形态（`data.result[].value = [ts, "n"]`）与 field/value 形态（`data.result[].values[].value`），并对空 `result` 判 0；**解析失败 → 回退整窗重发**（绝不把「看不懂」当成 0 而漏发） |
| 条数级漏检（内容缺失 / 同数替换） | 明确列入「不做」（§2.4），`Basis=count_only` 显式标注；写入路径仍逐条校验；人工全量重建兜底 |
| 误判缺失导致重复 | 「允许极少重复」是既定口径；重复量受「只补缺失天」限制，且重发仍走 `replace` 取代该天旧代 |
| 对账拖慢启动 | 并发 + 双超时 + 失败即回退；天数量通常 1–3；最坏 ≈ `ceil(源数/并发) × timeout` |
| catalog 无记录时误判 | 无记录/无 scope → 判缺失并重发该天（保守方向：宁可重发，不可漏发） |
