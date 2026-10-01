# 功能规格：日志成本治理（分层 / 采样 / 保留）

> 状态：**部分落地**（采样与保留两包及接线已实现并自证；冷层驱动器、观测面接线、分级验证见 §7「未做」）
> 关联：FR-498（采集侧成本）、FR-477（分区 Catalog 与冷热 Lifecycle）、FR-474（容量门禁）
> 实测环境：本地 VictoriaLogs **v1.52.0**（真二进制，`.tmp/lr-probe/`，未触碰生产）

本规格不得重新定义 FR-473 Shared Contracts；字段、状态与覆盖语义以
`worker-log-platform-contract/spec.md` 为准。

---

## 1. 不变量 R（区间守恒）——本项的一等不变量

**陈述**：对任何一个日志源，把本批次被消费的行按位置升序排列，其 `Record` 区间在
`rangesTouch` 口径下是一条连续覆盖；采集侧的每一次「抑制」都不得在这条覆盖上留下空洞。

**形式化**：`MergePositionRanges(Process(in)) == MergePositionRanges(in)`（逐段相等）。

**为什么是硬约束（而不是整洁性要求）**：缺口自动消解的判据是「缺口必须完全落在**某一段**
连续覆盖区间内」（`ledger.CoveredByPositionRanges`；复审 P1-1 明确否掉了凸包判定），
而连续覆盖区间由 `gapRangesOfEvents` 从**实际投递的事件**的 `Record` 区间折出。
若抑制让事件凭空消失，被抑制行的位置就落进空洞：任何跨越该位置的既有缺口
（例如一次 `DELIVER_ERROR` 登记的 `[1000,5000)`）将**永远无法自动消解**，
而 `ResumeAcquire` 要求「零未解决缺口」⇒ 该源被永久焊死在暂停态、不再产生新数据。
这正是 2026-10-01/02 那次「13 小时零新数据」事故的形态。

**实现方式**：抑制不丢弃，而是把被抑制的行替换为**汇总事件**：
- `Record` 区间 = 被抑制行的**精确并集跨度**（仅在区间相邻时合并，绝不跨越未被抑制的事件）；
- 走完整管线（WAL → 投递 → 逐字段校验 → 发布），与普通事件无差别；
- 被抑制条数、规则、级别、首条样例全文写进正文与字段。

**精确并集的含义（三条边界）**：
1. 只在 `d.event.Record.Start <= last.Record.End+1`（相邻）时合并 ⇒ 绝不是 min..max 凸包；
2. 段内必须**同级别、同流、同规则** ⇒ 汇总的 `level`/`stream` 字段不撒谎，跨级别的行不混入；
3. 段内必须**同源** ⇒ 汇总不得给别源的行盖自己的源印章。

**汇总事件的契约地位**：`EventID = logtypes.EventID(源, 区间, parser)`、
`CanonicalHash = CanonicalContentHash(时间, 级别, 流, 正文)`，即与普通事件**同一套推导**。
不得改用 `agg-<start>-<end>-<level>` 之类的人造 ID——投影校验按 `event_id` 做 `want`/`allowed`
查表（`verifyProjectionChunk`），人造 ID 会让汇总在校验表里查不到自己。
唯一性是推出来的：汇总的 `(start,end)` 是其成员并集，成员不再出现在输出里，
输出各事件区间两两不相交 ⇒ `(start,end)` 两两不同 ⇒ sha256 推导的 ID 两两不同。

**确定性与幂等**：抑制决策只依赖事件自身携带的数据（级别、流、正文、`EventTimeUTC`），
**不读挂钟**。同一条 WAL 条目重放必然得到同一结论与同一汇总事件（ID 与哈希逐字节一致），
故账本的 `(EventID, CanonicalHash)` 去重语义不受影响：重投是「多写一次」而不是「写出一份不同的东西」。
唯一的例外是风暴降级状态（由外部读数驱动），其差异只导致「多写原文」（安全方向）。

---

## 2. 策略与配置面

| 键 | 默认 | 语义 |
|---|---|---|
| `log_ingest.sampling.enabled` | `false` | 采样总开关（关 = 恒等，零行为变化） |
| `log_ingest.sampling.min_level` | 空 | 等级过滤：低于它折叠为汇总；**无法归类的级别一律放行** |
| `log_ingest.sampling.burst_window` / `burst_threshold` | `0` / `20` | 同源同消息高频抑制 |
| `log_ingest.sampling.budget_max_events_per_window` / `budget_window` / `budget_keep_every` | `0` / `1s` / `10` | 每源预算（quota → 采样比） |
| `log_ingest.sampling.degrade_enabled` / `degrade_target_level` / `degrade_disk_percent` / `degrade_hold` | `false` / `ERROR` / `80` / `30s` | 风暴自动降级 |
| `log_retention.enabled` | `false` | 保留策略总开关 |
| `log_retention.hot_retention` | `90d` | 热层窗口：超期即搬运到冷层（用户最终口径） |
| `log_retention.cold_retention` | `730d` | 冷层保留期（**所有级别统一**；由 COLD 实例 `-retentionPeriod` 执行） |
| `log_retention.trigger.disk_percent` / `min_age` | `0` / `7d` | 磁盘水位提前搬运（取先到）；`0` = 只按年龄 |
| `log_retention.discard` | `false` | **意图闸**：显式允许直接删 |
| `log_retention.by_level.*` | D1 档位 | 按级别 TTL（`3d`/`7d`/`30d`/`90d`，支持 `d`/`w`） |
| `log_retention.sources[].match` / `by_level` | — | 来源级覆盖（`*` 后缀为前缀匹配） |
| `log_retention.sweep.vl_sweep` / `interval` / `timeout` | `false` / `1h` / `30s` | **执行闸** |

**取值口径**：`0` = 没填走默认，**负数 = 写错，启动即拒**。校验读**原始取值**而不是
`Normalize()` 之后的值——后者会把「阈值填 -1」悄悄当成「没填」，使校验成为死代码
（该缺陷由变异实验 M10 发现）。

**归并粒度按规则而定**（不是可选的细节）：等级过滤/风暴降级只按 `(级别, 流)` 归并；
高频抑制/预算才按 `(级别, 流, 模式)`。若等级过滤也按模式归并，一百条互不相同的 DEBUG
会产出一百条各自只有一条的汇总——体积没省下来，还多了一层包装。

---

## 3. 决策记录

### D0 默认口径定稿（2026-10-02）：热 90d / 冷 730d — 已采纳
`hot_retention` 默认 `90d`、`cold_retention` 默认 `730d`。**语义定稿**：D1 的分级档位表达的是
「**在热层停留多久**」；**冷层所有级别统一**（730d），不做级别档位——冷层的目的只有长期留存，
再分档只会让「同一条日志在两层的保留期不一致」变成常态。到期动作不变：**搬**（不裸删，双闸默认关）。

**连带修掉的一致性风险**：VL 自身的 `-retentionPeriod` 到期是**直接删**，它**绕过**「删前归档」——
若热层实例 retention（原默认 30d）短于 `hot_retention`（现 90d），VL 会在搬运驱动器之前先删掉数据，
硬规则被静默架空。故：`log_vl.retention_period` 默认改为 `90d`（与热层窗口对齐，留出搬运余量），
新增 `log_vl.cold_retention_period` 默认 `730d`，并让 `vlsup` 支持**按 namespace 的 retention**
（`RetentionByNamespace`，未指定时回退）。回归见 §6.9。

### D1 热层分级（保留期档位）— 已采纳
`debug 3d / info 7d / warn 30d / error 90d`。原为「分级删除」，随 D4 改为**分级搬运/归档**：
段搬运（年龄到）→ 整分区搬 COLD（复用 `catalog`/`lifecycle`）。热层窗口默认 30d
（与受管 VL 的 `-retentionPeriod` 对齐，即用户认可的热层现状）。

### D2 冷层保留 — 已采纳（本项不动 VL 启动参数）
COLD 默认保留期长（建议 365d 可配；另提供「手动清理」模式），热层保持现状 30d 没关系
（快查靠热层、长留靠冷层），查询路径保留 Rehydrate 回灌能力。
**落点**：中心侧 HOT/COLD/Rehydrate 三段路径；instance-local gz 只是回退溯源来源，
**不在本项范围**（本项不碰实例本地归档）。

### D3 搬运/删除/回灌全部进 G10 观测面 — 部分采纳（见 §7）
要求「谁、何时、多少、去哪」可随时审计。`ArchiveOutcome` 已含
`Result`/`TargetDirID`/`At`/`Err`，`ArchiveStats` 已含计数与 `LastError`；
**对外暴露点（日志行 + 指标）尚未接线**。

### D4 删前归档 — 已采纳（硬规则，默认开启）
保留期到期数据**绝不裸删**，三选一，①为默认：
1. **首选**：搬运到 COLD（复用 `ROUTING_FROZEN→…→CLEANED` 状态机）——搬运成功 + 校验通过
   才允许 HOT 侧回收；搬运失败 = **保留原物 + 告警**（宁占盘不丢数据）；
2. 次选：导出为独立副本（接口已留，实现见 §7）；
3. **仅当**配置显式 `discard: true` 才允许直接删。

**次序不可调换**（`retention.PlanArchive`）：搬运判在删除**之前**。即使运维显式写了
`discard: true`，只要冷层路径可用，默认动作仍是**搬运**。`discard` 的作用是解除
「没有归档路径时的阻塞」，不是把默认动作从搬运改成删除。
**该次序是变异实验 N13 暴露出来的真实缺陷**：初版把 `discard` 判在前面，
语义变成「只要曾显式开启过删除，默认动作就永久变成删除」，与规则意图相反。

**两道互相独立的闸**（而非一个开关）：执行闸 `sweep.vl_sweep` + 意图闸 `discard`，
两层都满足才可达删除，且受管 VL 还需带 `-delete.enable`（VL 自身默认关，实测未带时
`/delete/*` 返回 400）。理由：这条路径后果不可逆，一处误开与两处同时误开的概率差得很远。
默认路径永远是搬运（数据仍在、仍可查），使**所有配置错误的最坏后果从「数据永久丢失」
降级为「盘没省下来」**。

### D5 观测面 — 部分采纳（见 §7）
运行期可动态调整日志等级（debug 用于现场定位、平时压到 info 以降开销）——
**尚未接线**（且既有 `log.level: debug` 本身未接到 slog handler，见 §7）。

---

## 4. 保留策略：动作模型与判定

```go
const (
  ActionMoveToCold  = "move_to_cold"  // 首选
  ActionArchiveCopy = "archive_copy"  // 次选（接口已留）
  ActionDiscard     = "discard"       // 仅显式 discard 可达
  ActionBlocked     = "blocked"       // 无归档路径且未显式放弃 ⇒ 保留原物 + 告警
)
```

**删除过滤器由本包生成，绝不从配置读**（安全关键）。VL 的删除接口接受 LogsQL 过滤器，
若把配置字符串直接当过滤器，一次误写（`filter: "*"`）就能在几分钟内删掉全部日志且不可逆。
因此：级别必须命中白名单（`TRACE/DEBUG/INFO/WARN/ERROR`），源标识必须通过字符集白名单
`^[A-Za-z0-9_.:-]{1,128}$`（`ValidSourceID`），过滤器由二者拼出。
源标识含冒号，**必须加引号**（`log_source_id:"inst:147"`），否则会被解析成字段过滤、语义完全不同。

**计划不按「源 × 级别」展开**：只有写了来源覆盖的源才单独成一条目标，其余走「全实例 + 该级别」。
61 个源 × 5 级别的展开会产出数百个删除任务，而删除接口每次都要扫一遍索引，代价与收益完全不成比例。

**已知边界（粒度张力，如实登记）**：日分区里混着所有级别，而搬运的最小粒度就是「一天」
（`catalog.PartitionKey` 是 `(namespace, utcDay)`）。因此按级别的 TTL **无法**在热层独立生效：
`HotWindow()` 默认取显式 `hot_retention`（30d）；若未配则回退到「级别 TTL 最长档」（= 90d，ERROR 那档）。
按最长档取的代价是 DEBUG 会跟着多留（成本没省到头），但**承诺不失守**。
要做到真正独立的按级别热层保留，需要把级别分流到不同存储命名空间——
与采样侧「把低价值等级路由到独立流」是同一类改动，见 §7 的未做栏。

---

## 5. 实测：VictoriaLogs v1.52.0 能力边界

| 能力 | 实测结果 |
|---|---|
| per-level / per-tenant retention | **不存在**。`-retentionPeriod` 是实例级单一值，最小 1d、默认 7d |
| `/delete/*` | **存在**，真实路由是 `/delete/run_task`、`/delete/active_tasks`、`/delete/stop_task`（不是 `/delete`） |
| 删除调用形态 | `POST /delete/run_task?filter=<LogsQL>[&start=][&end=]` → `{"task_id":"..."}`；`filter` 是参数名（不是 `query`/`match[]`）；`| delete` 管道**不**被接受 |
| 门禁 | 未开 `-delete.enable` → 400 `requests to /delete/* are disabled`；受 basic auth 保护（无凭据 401） |
| 生效时延 | **即时**（实测 t+0s 已不可查；`active_tasks` 为空 = 任务已完成） |
| per-level 真删 | 实测可行：`filter=level:debug` 删净 debug、info/error 原样保留 |
| `-retention.maxDiskSpaceUsageBytes` / `maxDiskUsagePercent` | **存在**（超限自动丢最老 per-day 分区），**尚未接线** |

⇒ per-level TTL 在 VL 侧只有两条路：(a) 按级别路由到不同 namespace/实例（数据面重构，需运维决策）；
(b) `/delete` 定期扫描（需 `-delete.enable`，删除是异步后台作业）。本项取 (b) 且默认关闭。

---

## 6. 测试用例清单

### 6.1 `logs/sampling`（不变量 R）
- `TestInvariantR1_RangeUnionPreservedExactly` — 随机 40 轮 × 5 组策略，逐段相等
- `TestInvariantR1b_InterleavedLevelsWithChaoticClock` — 时间戳乱序下仍成立
- `TestInvariantR2_GapStillAutoResolvesAcrossSuppressedSpan` — **缺口跨越被抑制区间仍能自动消解**（真实账本 + 真实判据 + 真实允许名单）
- `TestInvariantR2b_NaiveDropBreaksGapResolution` — **反证**：朴素丢弃下同一缺口消解不掉
- `TestInvariantR3_NonContiguousInputKeepsItsHoles` — 输入自带空洞必须原样保留（不得缝成凸包）
- `TestInvariantR3b_NoSuppressionIsExactIdentity` — 无抑制时逐字段恒等
- `TestInvariantR4_AggregateIsAnOrdinaryEvent` — 汇总的 ID/哈希与契约推导一致

### 6.2 `logs/sampling`（投递契约）
- `TestDeliveryOrderIsMonotonicAndNonOverlapping` — 就地产出、不失序、不重叠
- `TestMaxRecordEndIsPreserved` — 批内最大 `record_end` 不回退
- `TestEventIDIsUniqueWithinBatchAndContractDerived` — 批内唯一 + 契约推导形式
- `TestAggregateNeverShrinksOrGrowsSpanBeyondMembers` — 不收缩、不越界（不圈入未被抑制的行）

### 6.3 `logs/sampling`（策略行为）
- `TestDisabledPolicyIsIdentity`、`TestLevelFilterFoldsBelowMinLevel`、`TestLevelFilterKeepsUnknownLevel`
- `TestBurstSuppressCountsAndFolds`、`TestBurstWindowExpiry`、`TestBurstDifferentMessagesNotFolded`、`TestBurstUnparseableTimeNeverSuppresses`
- `TestBudgetSamplingRatio`、`TestBudgetDeterministicAcrossRuns`
- `TestMixedLevelsNeverShareAggregate`、`TestMixedSourceInOneBatchNotMerged`、`TestPerSourceIsolation`
- `TestAggregatePayloadIsCompleteAndHonest`、`TestMaxAggregateEventsSplitsRun`、`TestSignatureTableIsBounded`
- `TestStormDegradeStateMachine`、`TestDegradeOnlyRaisesMinLevel`、`TestStatsReporting`
- `TestRankAndLevelBelow`、`TestMaskMessageIsDeterministicAndMasksVolatile`、`TestTruncateRunesKeepsValidUTF8`、`TestSignatureSeparatesLevelAndStream`、`TestPolicyValidateRejectsAndDefaults`

### 6.4 `logs/retention`（硬规则「删前归档」）
- `TestArchiveRule1_MoveFailureKeepsOriginalAndAlerts` — 搬运失败 ⇒ 保留原物 + 告警，**零删除**
- `TestArchiveRule2_ExpiredDataTakesMoveNotDelete` — 到期且有 COLD 路径 ⇒ 必走搬运
- `TestArchiveRule3_NeverNakedDeletesWithoutExplicitDiscard` — 穷举 enabled/vlSweep/hasMover 组合，**零删除**
- `TestArchiveRule3b_NoPathMeansBlockedNotDelete` — 无路径 ⇒ blocked + 告警，不是删
- `TestArchiveRule4_MoveYieldsAuditableTargetAndLeavesUndueData` — 承接物可审计 + 未到点不动
- `TestArchivePrefersMoveEvenWhenDiscardIsExplicit` — **次序**：能搬就搬（N13 缺陷的闸门）
- `TestArchiveExplicitDiscardIsReachable` — **正向对照**：显式放弃后删除确实可达（否则 Rule3 恒真）
- `TestArchiveSkipsUnparseableDay` — 日期不可解析不猜、不动手

### 6.5 `logs/retention`（策略 / 计划 / 执行器 / HTTP）
- `TestDefaultPolicyMatchesRecommendedTTLs`、`TestNormalizeFillsMissingLevelWithRecommended`、`TestEffectiveTTLUnknownLevelIsKeepForever`、`TestEffectiveTTLSourceOverridePrecedence`、`TestValidateRejectsHazardousConfig`
- `TestBuildFilterRejectsInjection`（12 类注入载荷）、`TestBuildFilterShapesAndQuotesSource`
- `TestPlanDoesNotExpandEverySource`、`TestPlanSkipsKeepForeverAndOverridesMatchingGlobal`、`TestPlanWhenDisabledIsEmpty`
- `TestSweeperDryRunNeverDeletes`、`TestSweeperSubmitsCorrectFilterAndCutoff`、`TestSweeperThrottlesRepeatedFilters`、`TestSweeperIsolatesPerTargetFailures`、`TestSweeperDisabledPolicyIsNoop`
- `TestClientDeleterBuildsRequest`、`TestClientDeleterRejectsMissingTaskID`、`TestClientDeleterRejectsMalformedBody`、`TestClientDeleterSurfacesHTTPError`、`TestClientDeleterWithoutClientFails`、`TestSweeperToRealHTTPEndToEnd`

## 6.7 `logs/retention`（G6 冷层搬运驱动器）

- `TestDriverMovesDuePartitions` — **到点必搬**（驱动器的存在理由）
- `TestDriverKeepsOriginalOnMoveFailure` — **搬运失败留存 + 告警**，零删除
- `TestDriverSkipsColdAndInFlight` — 已在冷层 / 在途迁移不得重复驱动
- `TestDriverDiskWatermarkTriggersEarlyMove` — 磁盘水位触发（年龄 + 水位取先到）
- `TestDriverDiskTriggerRespectsMinAge` — 盘再满也不搬刚写进来的分区
- `TestDriverDiskReadFailureFallsBackToAgeOnly` — 读数拿不到就退回纯年龄，不猜「盘满了」
- `TestDriverNoopWhenDisabledOrEmpty` — 未启用 / 无分区时彻底空操作
- `TestDriverListFailureSurfaces` — 列举失败如实报错（区分「没有分区」与「读不出来」）
- `TestDriverWithoutMoverBlocksInsteadOfDeleting` — 无搬运路径 ⇒ blocked + 保留原物
- `TestDriverRunStopsOnContextCancel` — 周期循环随 ctx 退出且**首轮立即执行**

## 6.8 配置接线（`internal/worker`）

- `TestLogLevelIsParsedAndRejectsGarbage` — `log.level` **有装配点**且非法值启动即拒
- `TestLogFormatIsParsedAndRejectsGarbage` — `log.format` 同上
- `TestMaxWALBytesHasFiniteDefault` — WAL 字节上界默认有限，且高于正常稳态量级
- `TestWALBudgetNoticeNamesTheUnlimitedCase` — 显式不限必须被点名

## 6.9 热/冷默认口径与按 namespace retention

- `TestSupervisorRetentionPerNamespace` — VL retention **可按 namespace 分开**（热 90d / 冷 730d），
  未指定的 namespace 回退，**空白值不得被当成有效覆盖**
- `TestDefaultPolicyMatchesRecommendedTTLs` — 热 90d / 冷 730d 默认，且冷层不得按级别分档

### 6.6 变异实验（`go test -overlay`，原文件零改动）
- `logs/sampling`：**11/11 全部以断言转红**（`.tmp/sampling-mutation/REPORT.txt`）
- `logs/retention`：**21/21 全部以断言转红**（`.tmp/retention-mutation/REPORT.txt`）

其中两条变异直接暴露了实现缺陷并据此修复：**M10**（校验读 `Normalize()` 之后 ⇒ 成死代码）、
**N13**（`discard` 判在搬运之前 ⇒ 默认动作被永久改成删除）。

---

## 7. 未做 / 需外部决策（如实登记）

| 项 | 状态 | 说明 |
|---|---|---|
| 冷层搬运**驱动器** | **已接线** | `retention.Driver`（`Step` 可单测 + `Run` 周期调度，首轮立即执行）；生产适配层 `apps/worker/log_retention.go` 把 `catalog` 分区与 `lifecycle.DayManager` 接成 `PartitionLister`/`Mover`，在采集运行时之后随同一 ctx 启动。触发口径 = 年龄 + 磁盘水位**取先到**（`log_retention.trigger.*`，默认只按年龄）；磁盘读数复用采集侧**同一份** `ingest.DiskCapacityProvider`。磁盘触发带 `min_age` 下限（默认 7d）：盘再满也不搬正在写的日分区 |
| `ActionArchiveCopy`（导出独立副本） | 仅留接口 | `retention` 已定义动作与结果语义，执行分支未实现 |
| eventstore 段/归档对象的回收 | 未做 | `logs/eventstore` 目前无 `Prune`/`Trim`/`Reclaim`；段是「权威事件集合」的承载体，加保留必须带**下界**（有界保留）且**不得破坏** `deliverTailPlan` 的水位语义（段只换介质不裁剪） |
| COLD/Rehydrate 的 VL 启动参数 | 未做 | `-delete.enable`、`-retention.maxDiskSpaceUsageBytes`、按 namespace 差异化 `-retentionPeriod` 尚未接入 `vlsup` 启动参数（按指示**默认不开**） |
| G10 观测面对外暴露点 | **部分** | `ArchiveStats`/`SweepStats`/`DriverResult`/`sampling.Stats` 已具备读数，驱动器每轮打印 listed/skipped/due/moved/keptOriginal/notDue；**指标端点未接线**。`log.level` 已接线并暴露 `LevelVar`（运行期可调，见 §6.8） |
| 分级验证（info/debug 只存不验） | 未做 | 需落在 `ingest/runtime.go` 的 `verifyProjection` 调用点（该文件由并行工作流持有） |
| 按级别独立热层保留 | 未做（可选路径） | 需把级别分流到不同存储命名空间（见 §4 已知边界），属数据面重构 |
| `log_capacity.max_wal_bytes` 有限默认 + 告警 | **已做** | 默认由 `0`（不限）改为 `DefaultMaxWALBytes`（512MiB，远高于正常稳态的 KB 量级，只在真失控时触发 PAUSED + 登记缺口）；显式写 0 时启动日志点名提醒（`WALBudgetNotice`），使「没配」与「配成不限」不再不可区分 |
| 真机部署复验 | 未做 | 本项**不含部署动作**，未触碰生产与用户实例 |

---

## 8. 回滚

- 采样：`log_ingest.sampling.enabled: false`（即默认值）⇒ `Process` 恒等返回入参，逐指令等价于改动前。
- 保留：`log_retention.enabled: false`（默认）⇒ 不计算、不暴露、不删除。
- 删除路径：`sweep.vl_sweep: false`（默认）**或** `discard: false`（默认），任一即可关闭；两者独立。
