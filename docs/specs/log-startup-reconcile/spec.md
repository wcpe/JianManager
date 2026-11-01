# 重启增量对账（源 × UTC 天，条数级）（FR-497）

> 状态：开发中（日志恢复子项本轮证据分离、稳定窗口物化预算、热层窗口及重启修复与已覆盖回归完成；整 FR 的自动收养 T7、60 源重启 ≤60s/随机强杀 100 次仍未完成验收）
> 本次提交/功能修复验收不代表整个 FR 交付；性能、长稳及追加故障覆盖延期，统一见 [收口范围与验收表](../worker-log-platform-contract/spec.md#2026-10-25-功能修复收口与延期专项)。　·　关联 PRD：FR-497　·　分支：feat/log-platform-hardening
> 关联 ADR：ADR-094（Worker 日志本地数据面）、ADR-095（磁盘权威事件存储）、ADR-093（进程生命周期韧性）
> 依赖：FR-496（采集索引 SQLite 与 `projection` 记账面）　·　被依赖：FR-498（规模压测的「60 源重启 ≤60s」判据）

## 2026-10-22 恢复证据与成本修复约束

以下保留本批修复约束，当前代码修复及已覆盖回归已收口，历史候选进度不替代顶部声明与共享验收表，也不代表自动收养 T7 或全部重启规模验收通过；共用 [采集修复契约](../worker-log-acquisition/spec.md) 与 [索引修复契约](../log-index-sqlite/spec.md)。

- 条数级对账只证明当前条数，不产生真实 `REQUEST_DONE`/ACK 或逐字段 `VerifiedRuns`；未确认的请求结果、核验证据与恢复责任保持独立，最大结束位置不能代替可取恢复来源。
- 重建成本以源、UTC 日和稳定内容窗口持久化，写前预留，实际/未知写入及发布 CAS 失败记账；不能以改变 attempt 或重启刷新额度。预算耗尽不阻止只读取证、可证明复用、正常尾采和有界排空。
- 热层窗口过滤不写、不验证也不宣称已发布；全部超窗口的 pending 收口须证明对应不确定范围已被覆盖，不能清除窗口内真实失败。混合窗口不以权威全量末端转移从未发布区间的 WAL 责任。
- 候选公平调度以实际完成源推进；共享准入必须确定性原子检查，不能按“多跑几次碰绿”代替并发上界证明。

待验收：条数相同但内容变化的证据隔离、物化预算跨重启/CAS 冲突/未知结果、真实脏环境二启/三启与回滚后再启、超热窗与混合窗责任不丢、候选并发反例结论。`60 源重启 ≤60s` 与持续负载门禁仍须实测，不能由功能测试推导。

### durable outbox r4 的恢复边界（本地候选已实现，联合验收中）

written attempt 是每源有界内存 hint，不保存正文、不是第二个发送队列，也不能跨重启继承成功。RO 确认按原代次/真实 permit 物理目标重建原 full actual-set：活动 WAL Y 重取原 cut/成员及实际 SQL 回执，已回收历史 X 另从 immutable eventstore/受控 inline 固定行界、摘要重读；保留原 count/window/digest，不用当前 cut 冒原 cut。未知结果先只读不重插，语义 Err 不冒作 Pending/copies；只有实测 missing 才交既有预算重建。

冲突只针对 actual candidates 及权威兄弟，namespace 历史诊断不封禁健康源；预存变体由既有裁定选真实合法赢家，真冲突保留 WAL/可见责任，不能按 ID 洗白。归档 Busy 在采样前原子 reservation 阶段拒绝，未改变 sampler；同对象/原游标 CAS 仅回退未接收部分并关 reader，已接收前缀及新尾 COMMIT 不丢。自动重试依赖既有 SyncArchiveScan 装配，不只依赖 Manager.Start。新恢复/停机/归档矩阵、完整 Worker 与性能仍按 r4 真实结果联合验收，旧 v8 短验不背书新 hash；详细 [共享契约](../worker-log-platform-contract/spec.md#durable-outbox-r4本地候选已实现联合验收中) 为准。

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

---

## 7. 2026-11-01 实测发现：常驻对账的整段重读会把「不可裁定身份冲突」放大到百万级

> 这是 FR-498 容量长稳中**真机上暴露**的问题，暂未修。记在这里是因为触发路径就在本节的对账循环里。

### 7.1 现象

60 源 × 30 行/s 长跑，10 分钟后 harness 日志到 **727 MB**，
`身份冲突：保留先到者并跳过冲突行` 计数 **1,805,473 次**。

### 7.2 为什么短用例复现不了

四个受控探针全部 **0 条冲突**：单源 25s、60 源 50s、60 源带真 VL 40s、脚本原样 60s。

**因为常驻对账默认周期是 15 分钟**（`reconcileLoopDefaultInterval`），
60s 的用例根本没跑到对账。把周期压到 30s（包内下限）后一轮能跑 ~60 次，
该路径才被覆盖 —— 这也是本轮给 harness 加 `-reconcile-interval` 的原因。

### 7.3 触发链（代码层面已逐环确认）

1. `reconcile_loop.go:619 canonicalDayCounts` 每轮对每源算缓存指纹：
   `fingerprint = EventsStoredThrough|Durable|Delivery`。指纹只看**水位**，不看事件段内容/大小；
   位置一变就整段重读。
2. 重读走 `runtime.go:9880 canonicalRecoveryEvents`，事件来自**磁盘段**：
   `m.events.Snapshot(key)` → `storedSnapshot.Iterate(add)`。
3. `add` 对每条事件调 `normalizeCanonicalEvent`（`runtime.go:10090`）：
   当 `EventTimeUTC == ""` 时用 `IngestTimeUTC` 反推时间，并**重算 `CanonicalHash`**。
   ⇒ **从段读回的哈希可能与写入时不同**。
4. 叠加两个结构性事实：
   - `EventID` 不含内容（`logtypes/types.go:101` = source\|gen\|record_start\|record_end\|parser_version）
   - 阈值率下的批写可能把多行归入同一 record ⇒ **同 event_id 但内容不同**
5. `adjudicateSameIntervalIdentity`（`runtime.go:4424`）：同区间 + 消息字节数相等
   ⇒ `(false,false)` = **不可裁定** ⇒ 走 `default` ⇒ **逐条 `slog.Warn`**。

### 7.4 产品侧的真正隐患（不是“夹具而已”）

`runtime.go:4452` 的注释写的假设是：

> 不可裁定的**真冲突**不走节流——它本就稀少、且必须逐条 fail loud，
> 节流它等于把真信号也压掉

**「本就稀少」这个前提在长跑下不成立。** 一旦真冲突规模化，
日志自身会把磁盘写满（727 MB / 10 分钟），可观测性反而崩掉。

### 7.5 未修的原因与建议方向

**未修**：修改身份裁定属于账本不变量范围，本轮没有充分证据链就动它是危险的
（参 ADR-101 「只许人工放弃 + 必须留痕」的教训）。

**建议方向**（未验证，不得当作已定方案）：
- 给「不可裁定」也加**节流 + 汇总**（保留首条样本 + 计数 + 按源分布），
  与 `traceIdentityAdjudication` 同一套机制，只是换一个计数键；
- 或让 `normalizeCanonicalEvent` 在重算哈希前先比对**从段读回的原始哈希**，
  若段里存了原始哈希就用它，避免“读回即变更”。

### 7.6 夹具侧也确实不合真实形态

真实 Paper 日志一行一条，不存在“同 record 多行”。长稳夹具（`genload`）
按 tick 批量写，天然会造出这种形态。**两边都要改**，但产品侧的放大是更严重的那一半。


### 7.7 追加实测：压缩对账周期是错的方向（2026-11-01 追记）

§7.2 我写了「把周期压到 30s 后该路径才被覆盖」，并据此改了夹具。
**这个方向是错的，在此更正。**

同一组夹具（60 源 × 30 行/s），只改时长：

| 用例 | 时长 | 对账轮次 | 身份冲突 | 日志体积 |
|---|---|---|---|---|
| verify-reconcile-30s | 240 s | 60 | **0** | 50 KB |
| longrun-9x-1 | 1800 s | 60 | **11,077,048** | **4.4 GB** |

**同样是 60 轮对账**，短测 0 条、长跑 1108 万条。差别不在周期，在**事件段里累积的事件量**：
冲突数 ≈ 对账轮次 × 每源事件段积累量。

⇒ 把周期从 15 min 压到 30 s，等于把「30 分钟里重读 2 次」变成「重读 60 次」，
**把问题放大了约 30 倍**（首轮 1800s 用 30s 周期得 1108 万；此前默认 15 min 周期
跑 600s 得 180 万）。

**正确做法**：不压周期。用默认 15 min，跑足够长的窗口自然覆盖对账。

### 7.8 一个被否掉的修法（记录以免重走）

曾考虑「已认定冲突的 event_id 不再重复报」。**这条不成立**：
冲突是在 `canonicalRecoveryEvents` 内部每次重读时重新算出来的，
`seen`/`events` 都是该函数的局部变量，跨轮不存在可复用的状态。
要「记住已报过」就得新增持久状态，等于把真信号永久静音 —— 比修根因更糟。

### 7.9 对 FR-498 长稳的影响：**未完成**

在 §7.3 的根因修掉之前，9×30min 长稳**跑不出可信结果**：
第一次默认周期跑到 10 分钟即 727 MB / 180 万条；第二次 30 s 周期 30 分钟到 4.4 GB / 1108 万条。
两者都因磁盘与可读性不可接受而中止。

**如实结论：FR-498 的 9×30min 项未通过（未完成），不是通过。**
要么先修 §7.3 的哈希重算，要么把夹具改成「一行一条独立 record」并配合默认周期，
才具备继续跑的前提。
