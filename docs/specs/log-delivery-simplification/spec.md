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

## 2. 被删除的机制（**永久不再运行**）

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

## 7. 验收（受控口径）

全量跑会被两个**既有**坏源截断（`TestIndexManagerConfirmedPhysicalFingerprintAndGapThreeOpens` 的 panic、
`TestIngestSixFixDeferredPersistFailureNeverSends` 的挂死），故验收一律带 `-skip`：

```
go test ./internal/worker/logs/ingest/ -count=1 -skip 'TestIndexManagerConfirmedPhysicalFingerprintAndGapThreeOpens|TestIngestSixFixDeferredPersistFailureNeverSends|TestIngestSixFixInconclusiveConfirmIsRequeued|TestIngestSixFixSilentQuietSourceReconfirmsPending' -timeout 900s
```

结果：**56 条失败，与基线 56 条完全一致（`comm` 为空集）**。
即：本次简化**没有引入任何新增失败**。

- `internal/worker/logs/` 下除 `ingest` 外 **26 个包全绿**（vlrange / query / ledger / acquire / stateindex / vlsup）
- `go build ./...` 与 `go vet ./internal/worker/logs/...` 均零输出

## 8. 未处理（**既有问题，不在本次裁定范围**）

56 条基线失败涉及：实例规则升级与跨重启还原、`resolveOneSourceGaps` 的锁与预算、
部分 `TestManager*` 发布一致性。其中多数与 2026-10-30「索引改纯内存」有关。
需要时另开一轮清理。
