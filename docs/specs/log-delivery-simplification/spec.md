# 日志投递路径简化：语义变化与实测结论

> 2026-11-02 · 用户裁定：「去掉代次+白名单+三层账本，改成『边读边扔 VL，event_id 做幂等』」

## 1. 最终形态

`deliverCtx` 从 335 行 / 179 分支降到约 60 行：

```
① acquireSourceDelivery（投递守卫，提供带 pipe 的 ctx）
② 批内 event_id 去重
③ normalizeCanonicalEvent 逐条规范化（Raw 事件按稳定摄取时间）
④ appendEvents（写本地磁盘段）
⑤ insertInBatchesCtxYield（写 VL）
⑥ scheduleGapResolve（异步登记缺口核对，不阻塞）
```

查询侧同步去掉 `projection_generation` 过滤（`vlrange` / `query_text_budget`）。

## 2. 被删除的机制（**投递路径上永久不再运行**）

> 口径很关键：这些机制是在**投递路径**（`deliverCtx`）上被摘除的。
> `publishSnapshot` / 物化预算 / `PublicationPending` 这三个**代码仍在**，仍服务于重建与对账路径——
> 详见 §8.3 的修正。

| 机制 | 说明 |
|---|---|
| 代次取名 / 预留 / `projection_generation` 标签 | 一批数据只写一次，不需要按批次区分副本 |
| 发布快照 `publishSnapshot` | 不再有「已发布投影」这个概念 |
| `PublicationPending` 发布确认链 | 不再需要确认「上次发布是否成功」 |
| 整窗重发 / `replace` 分支 / 增量尾部快路径 | 不再重写历史 |
| 物化预算 `reserveMaterialization` | 服务于整窗重写 |
| `activeTails` 活跃尾部代次簿记 | 服务于代次分配 |
| 逐字段校验（`verifyProjectionQuery` 家族在投递路径上） | 投递以 VL 的 HTTP 结果为准 |
| 三层账（closed / debt / verified 互为前提） | 死锁根源 |

## 3. 语义损失（**必须如实记录**）

### 3.1 不再保证「哪段数据已完整发布」
旧系统能回答「该源历史上每一天是否完整」；新系统只保证「写进去的都在、重投不重复」。

### 3.2 投递失败 → 重读重投；重读也拿不到（文件已轮转）→ **数据静默丢失**
旧系统会记缺口让运维看见欠账；新系统没有这个欠账视图。**用户批准时已知情。**

### 3.3 重启不保留未投递队列
WAL 是纯内存队列，重启即丢。可靠性由「持久化 cursor + 至少一次重读」承担
（对齐 Filebeat `queue.mem` + registry 模型）。

### 3.4 `ResolveCoveredGaps` 的拒绝理由变了（运维接口契约变化）
旧：源没有完整已发布投影 ⇒ 返回 `has no complete published projection`。
新：不再读已发布投影（概念没了），该文案在生产代码中**已完全消失**。
**契约本身保留**：全源自动路径**无论 metadata 是否齐备都不得放弃缺口**
（`TestResolveGapsAbandonRendezvous` 已改为直接断言「不产生任何 `Abandonments`、水位与缺口原样」）。

## 4. 本轮修出的**真 bug**（都是简化时漏掉的基础设施，不是账本机制）

1. **`deliverCtx` 漏了 `scheduleGapResolve`** ⇒ 缺口只登记不消解 ⇒ reclaim 永久钉死。
2. **多处仍按「已发布代次白名单 + ClosedVisibleSeq」过滤**
   （`landedVisibleProbeOnce` / `visibleEventsInVL` / `autoResolveGapsFromPublished` /
   `resolveOneSourceGaps` / `tryConfirmPendingPublicationVerdict` / `reconcileWithExpectation`）
   ⇒ 新数据行没有代次标签 ⇒ 查不到 ⇒ 已写成功的被判缺失。
   **统一改成按 `event_id` 直查 VL**（`eventIDsVisibleInVL`）。
3. **`RegisterSource` 从不初始化 `m.state.Sources[key]`** ⇒ `recordVerifiedRuns` 第一行
   `if !ok { return }` 静默丢弃 ⇒ verified runs 永不推进。
4. **重放切片死循环**：预算 1 天/轮时，每轮都把同一个被误判缺失的天重写一遍，
   另一天永远排不上（`rows` 一直涨但数据永远不齐）。
5. **`deliverCtx` 漏了 `normalizeCanonicalEvent`** ⇒ Raw 事件以 `EventTimeUTC=""` +
   错误 hash 写进 VL ⇒ 核对路径永远判「不在 VL」。
6. **`selfHealPausedSource` 排空条件漏了「有未决缺口」** ⇒ 简化后没有整窗重放，
   失败批次只能靠它重投产生证据。
7. **可见性查询按条数切片**（512 条）⇒ 加上内容比对字段后超 VL 的 query 字节上限
   （实测 400 `query too long`）⇒ 改为**按字节切片**（`eventIDsQuerySpans`）。

## 5. 数据诚实性红线（一个字没放宽）

- VL 里查不到的字节，绝不因为「本地段有」而算作已落库证据。
- 同 `event_id` 不同内容（被篡改）**不算可见**（逐字段比对）。
- 缺口消解的**原因白名单**与**连续覆盖**两重判据原样保留。

## 6. 测试取舍口径

| 类别 | 处理 | 条款 |
|---|---|---|
| 守数据正确性 | **改写断言**，不放宽 | `TestDeliverEvidenceFace*`、`deliver_plan_outside_lock_test` 的锁断言、`TestResolveGapsAbandonRendezvous` |
| 测已删机制 | `t.Skip` + 写清守什么、为何失对象 | 投递尾部快路径、pending 轮内复用、`TestIngestSixFix*` 多组、`TestVerify*` 三条、`TestPR61*` |
| 索引已改纯内存（2026-10-30） | `t.Skip` | 跨重启用例 |

判断口径：**先 grep 这条用例引用了哪些符号**，全部落在已删链上才 Skip。

## 7. 验收（2026-11-03 更新：已无任何 skip）

**当前口径**：

```
go test ./internal/worker/logs/ingest/ -count=1 -timeout 1200s
→ ok  293.5s   执行 633 · PASS 457 · SKIP 47 · FAIL 0 · PANIC 0

go test ./internal/worker/logs/... -count=1 -timeout 1200s
→ 全绿（除 ingest 外 26 个包）

go build ./... && go vet ./...   → 零输出
```

**不再需要 `-skip`。** 2026-11-03 那轮清理里，原先两个「坏源」的形态都已消失：

| 原状 | 现状 |
|---|---|
| `TestIndexManagerConfirmedPhysicalFingerprint…` **panic** | `SKIP`（索引纯内存，无磁盘库可重开三次） |
| `TestIngestSixFixDeferredPersistFailureNeverSends` 挂死 11m56s | `PASS 0.01s` |

那两个现象是**简化改造期的中间态**：`deliverCtx` 简化时 `PublishedProjection` 变成 nil，
某条用例解引用就 panic，而一 panic 就中止整包——于是后面一大批本该红的既有失败全被掩盖。
根因（就绪面依赖已发布投影、pending 无收口）本轮修掉后，panic 与挂死同时消失。

47 条 SKIP 全部带明确理由，分四类：

| 类别 | 条款 |
|---|---|
| 投递路径简化（2026-11-02） | 写后校验 / 发布链 / 代次白名单 / 隔离视图 / 整窗重发已删 |
| 索引纯内存（2026-10-30 裁定） | 跨重启前提整体取消 |
| race 构建豁免 | `verify_partial_progress` 那类依赖时间比例的断言 |
| 数据面已变 | 写前幂等使重建零物理写入等 |

## 8. 后续清理（2026-11-03 已完成）

2026-11-02 简化落地时遗留的 56 条基线失败，已于 2026-11-03 全部处理完毕（11 个 commit）。

### 8.1 顺带修出的 8 个**真 bug**

都是「简化删掉了机制，但依赖它的判据没同步收口」这一类：

| # | bug | 线上后果 |
|---|---|---|
| 1 | `CutoverReadiness` 仍以「已发布投影」为准 | cutover 闸门**恒不可满足**，无任何数据面动作能恢复 |
| 2 | `PublicationPending` 永不消退 | 就绪面永久 `publication_pending` |
| 3 | STDIO 源持久化游标从未生效 | 每次重启重读整个 raw spool（event_id 不同 ⇒ 下游幂等救不了） |
| 4 | `dropEventsAlreadyWritten` 代次过滤永不命中 | 写前幂等完全失效，重复写 VL |
| 5 | `deliverCtx` 丢弃 `clientForSource` 的 frozen 标志 | 冻结日（迁移/归档窗口）照常写入且当成功 |
| 6 | 重启后任何规则升级都必定失败 | `state.Instances` 恒空 ⇒ `exists=false` ⇒ 撞旧 pipe |
| 7 | FR-504 归档 glob 重算永久失效 | `recalcInstanceSourceConfig` 找不到绑定 ⇒ 旧 glob 永久残留 |
| 8 | `RegisterInstance` 在「绑定已存在且未变」分支从不写回 | 重启后绑定恒空、`AppendInstanceOutput` 恒报 pending |

第 1、2 条是同一类故障的两种表现，共同特征是**不报错、只表现为「一直不就绪」**——
在生产上会被当成配置问题排查很久。

### 8.2 其余 48 条的归类

| 类别 | 处理 |
|---|---|
| D1 夹具时序（20+ 处） | `StartAtEOF: true` 要求「先登记再产生日志」；改夹具 |
| 跨重启（前提已被 2026-10-30 裁定取消） | 改断言或 `t.Skip` |
| 断言口径过时（写后校验/发布链已删） | 改断言或 `t.Skip`，每条写清失去什么、关切由谁接 |

判断「失去对象」的口径：`grep -rn "funcName(" --include='*.go' | grep -v _test.go`
只抓到定义行 ⇒ 只剩测试在调 ⇒ 该组用例整体失效。

### 8.3 对本文 §2 的**修正**

§2 说物化预算与 `publishSnapshot` 已删——**不够准确**。实测它们仍在，只是**不在投递主路径上**：

| 机制 | 现状 |
|---|---|
| `publishSnapshot` | 仍被重建（`writeProjectionDay`）、冲突清空后重发布、物化复用三条路径调用 |
| `reserveMaterialization` / `updateMaterialization` | 仍服务于**重建**路径（`rebuildDaysInner` → `writeProjectionPlan`） |
| `PublicationPending` | 仍存在，但置位点只剩「重建切片预算用尽」；收口已补在「重建整窗完成」与「确认取证通过」两处 |

准确的说法是：**投递路径（`deliverCtx`）不再走发布/物化**；重建与对账路径仍然走。
这是有意的——重建需要「写一版新代次」这个动作本身，而投递不需要。

### 8.4 第 9 个真 bug：投递后 Catalog 无记录 ⇒ 数据写进去了却查不到

**2026-11-03 清完 56 条后才浮出来的**——它没有对应任何失败用例，是读文档、
查 `PublishedProjection` 的消费者时发现的。

#### 缺陷形态

`Catalog` 分区记录的唯一创建者是 `publishSnapshot` → `cat.PublishProjection`。
投递路径不再调它 ⇒ **新源首次投递后 Catalog 里永远没有记录**。

而 `catalog.querySnapshotLocked` 的门禁是：

```go
if rec == nil || rec.PublishedProjection == nil || !Recover(rec).Queryable || !OwnerForQuery(rec).OK {
    continue   // 该分区不进快照
}
```

⇒ `query.Planner.Plan` 拿到 `view targets=[]` ⇒ **用户看到「这天没日志」**。

夹具实测（`node:cp/g1` 写两行）：

```
VL writes=1 · 事件段 1 条 · catalog record exists=false
Planner.Plan → view targets=[] · queryable={}
```

#### 为什么它比「丢数据」更危险

日志一切正常、VL 里也有行、投递计数也对——就是查不到。
运维会先去查「是不是查询配置错了」「是不是 VL 挂了」，而真相是写路径少了
一个建记录的步骤。

#### 修法

在 `deliverCtx` 写 VL 成功之后调 `ensureCatalogPartition(source)`：

1. 已有记录 ⇒ 直接返回（重建/迁移可能已写过，owner 可能不同，不能覆盖）；
2. 没有 ⇒ 用 `WriteAdmission` 取 owner / generation / dirID（**与写路径同源**，
   避免「记录写 HOT、数据写 COLD」）；
3. 建 `NewStableRecord` + **一个非 nil 但代次为空的 `PublishedProjection`**；
4. **必须走 `AppendJournal`，不能直接 `cat.Put`**。

第 3 条是关键：空代次让 `vlrange.projectionGenerations` 返回 nil ⇒ 查询侧
**不加代次过滤**（这正是简化后的预期）；而结构非 nil 才能过上面的快照门禁。

第 4 条是**修这个 bug 时差点又造一个 bug**的地方：第一版用了 `cat.Put`，
它**不走 journal**、直接写内存 map。后果是「journal 追加失败时分区也可见」——
`TestManagerDoesNotExposeProjectionWhenJournalAppendFails` 当场转红（它的诉求正是
「未提交的分区绝不可查」）。改成 `AppendJournal` 后：journal 失败 ⇒ 记录不落地
⇒ 该分区不可查。代价是那一批数据晚一拍可见（下批重试），这正是正确的安全方向。

#### 回归

`catalog_partition_after_delivery_test.go` 的 `TestDeliveryLeavesCatalogPartitionQueryable`：
判据① Catalog 记录存在且 owner/dirID 与 `WriteAdmission` 同源；
判据② `Planner.Plan` 能规划出该目标且 `Queryable == true`；
判据③ 不得被标成 `PROJECTION_INCOMPLETE` / `DupUnresolved` / `StatsPartial`。
**已做变异验证**：注释掉 `ensureCatalogPartition` ⇒ 三条同时转红。

### 8.5 第 10 个真 bug：新数据全被标成「不完整 / 未对账」

修完第 9 个后，查询能规划出目标了，但 `state=LOG_COVERAGE_PARTIAL`、
`reasons=[PROJECTION_INCOMPLETE]`、`quality=DupUnresolved/StatsPartial`。

**根因**：`publishSnapshot` 末尾**无条件** `proj.CoverageComplete = true`（runtime.go:10460），
而这正是简化后不再走到的地方。我补建的空壳投影 `CoverageComplete=false`
⇒ `planner.go:347` 据它推「不完整/未对账」⇒ 前端**每次查都显示一条降低信任度的横幅**，
而实际上什么都没错。

**修法**：`query.projectionlessScope(proj)` —— 三条同时成立才认定是简化空壳，不据此降级：

```
ManifestVersion == ""  &&  len(SourceProjections) == 0  &&  len(ClosedVisibleSeq) == 0
```

`publishSnapshot` 末尾**总会**写 manifest（`manifest-<sha256>`）⇒ 旧数据三者至少有一个非空，
不会被误判。修后实测：`state=LOG_COVERAGE_SUCCESS`、`quality=EXACT/EXACT`、`reasons=[]`。

### 8.6 死代码清理（2026-11-03）

对每个被删/弱化的符号做「非测试引用」盘点后，删除 9 个已无生产调用者的函数 + 1 个整文件
（约 330 行）。清单与本轮验证口径见 commit `16ba55ea`。

值得记录的一条：**删函数不能用「下一个顶层 func」当边界**——类型定义/变量声明经常夹在
两个函数之间，那样会连带删掉。用**花括号配平**定位函数体，并用 `assert` 校验首行确实是目标函数。

还有一个「半死」形态：`activeTails` 的两个消费者删除后，写入方 `rememberActiveTailGeneration`
变成**纯写不读**（每次投递白拿 `publishMu` + `m.mu` 两把锁）。这类也要一并清理，
但保留类型定义与 `Manager` 字段供已 Skip 的回归用例引用（它们是「失去对象」的占位）。

### 8.7 盘点方法（以后改投递/查询链路请照做）

对每个被删的符号，**grep 全仓非测试引用**，逐个确认消费方：

```bash
grep -rn "\b<symbol>\b" --include='*.go' internal/ apps/ | grep -v '_test.go'
```

三种归宿：① 有现役等价物（改调用）；② 已合理降级（写注释说明）；③ 真正的消费方丢了（就是 bug）。

**最要紧的一条：「测试全绿」不等于「功能正确」。** 那 47 条 Skip 里隐藏的「发布链已删」
这个前提，其实还有未被覆盖的消费方——第 9、10 个 bug 就是这么漏过去的。

以后改投递/查询链路，必须端到端验一遍：

```
投递 → catalog.Get → Planner.Plan → Queryable / state / quality
```

只验写入侧（VL 有行、事件段有条数）是不够的。
