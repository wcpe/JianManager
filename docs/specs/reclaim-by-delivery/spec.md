# reclaim 简化：改按投递水位推进

> 用户裁定（2026-10-29 05:12）：【两者都删（reclaim 改为按投递水位推进，不再要恢复段/投影证据）】
>
> 落地提交：`84d3c5e4`（生产代码）。本文记录**实际落地的语义**，与裁定当时的设想要点一致，
> 但有两处是实施中修正的（见 §2.1 与 §2.2），都写清了原因。

## 1. 为什么这裁定是对的（生产数据支撑）

生产 10 个停采源 `reclaim = 0`，而 `recovery_audit`（恢复段）里**只有健康源**：

```
inst:328=49, 329=17, 330=15, 331=11, 332=5, 333=14, 334=13, 335=11
inst:317~327 → 一条都没有
```

卡死链：

```
tryReclaimLocked 找不到恢复段
  → ErrReclaimNoSegment: no recovery segment covers position 0
  → 走补段 backfillDeliveryConfirmedCoverage
  → 补段要求「插入 + 逐字段校验 + 已发布投影」
  → 那 10 源取不到已发布证据（超热层保留期 / 投影缺陷）
  → 补段永不成功 → reclaim 永为 0
  → WAL 删不掉（旧架构）→ 撞上限 → 整个源停采
```

**这套机制的论证前提**：WAL 里还有未投递副本，需要一个受管副本接管责任后才能删 WAL。
WAL 改纯内存（不落盘、`source_wal` 表已删）后：
- 没有副本需要有人接管
- 没有 WAL 行需要删
⇒ **前提消失，机制失去对象**。

## 2. 新语义（实际落地形态）

```go
// 回收上界：从当前 Reclaim 起、被 REQUEST_DONE 证据连续覆盖到的最远位置
reclaim = min(contiguousCertifiedEnd(entry), max(Positions.Read, Positions.Durable))
```

- 不需要恢复段、不需要投影证据、不需要 `ReleaseReason`
- 不再有 `ErrReclaimNoSegment`
- 「无事可做」（`target <= Reclaim`）返回 `(当前Reclaim, nil)`，不再报错

### 2.1 修正一：`Delivery` 自己的 floor 从 Reclaim 改成自身

原设想是 `reclaim = min(delivery, read)`，而 `delivery` 仍按 `contiguousDeliveryEnd(batches, reclaim)` 推导。
**这会死锁**，原因不是「自引用」那么简单：

- `contiguousDeliveryEnd` 的 floor 只是起点，返回值恒 ≥ floor，本身不会把值往下拉，循环能收敛；
- 真正致命的是**裁剪**：批次水位 `min(Reclaim, Delivery)` 一旦追平，全部历史批次都成了
  「水位之下的恒等元」被裁掉 ⇒ `contiguousDeliveryEnd(空列表, reclaim) == reclaim` ⇒ **回收永久停滞**。
  即「推进依据被裁剪吃掉」——这才是旧架构最终必须靠「恢复段 + 投影证据」兜底的根因。

因此 `Delivery` 改为**自身当 floor 且取 max**（`advanceDeliveryEnd`）：

```go
Delivery = max(旧值, contiguousDeliveryEnd(batches, Delivery))
裁剪水位 = Positions.Delivery    // deliveryBatchWatermark，与上同一口径
```

自引用消失；裁掉的条目都满足 `End <= Delivery`，而 `contiguousDeliveryEnd(裁剪后, Delivery) == Delivery`
恒等 ⇒ floor 处信息不需重建。

### 2.2 修正二：回收上界不是 `Delivery`，是 REQUEST_DONE 证据（**数据安全红线**）

第一版实现用过 `min(Delivery, max(read,durable))`。**这是数据丢失级缺陷**：

`Positions.Delivery` 的语义是「所有字节均有**请求结果**」，而请求结果**包括 UNKNOWN**——
`acquire.recordHTTPResultLocked` 把「响应丢失 / 非 2xx」也登记成批次（为了可审计）。
拿它当回收上界 = 把「发出去了但不知道落没落库」的数据从内存队列里删掉。

旧架构正好靠「恢复段只在 `Delivery >= to` 且 REQUEST_DONE 覆盖时才建」避开了这一层，
删恢复段时把这层保护一起删了。

修正：回收上界改 `contiguousCertifiedEnd(e)`——按 `MergePositionRanges(RequestDoneRangesOf(e))`
从 `Reclaim` 起算连续覆盖，**两端各容差 1 字节**（分隔符，与 `CoveredByPositionRanges` 同一口径；
真正的空洞 ≥2 字节仍阻断）。UNKNOWN 区间因此成为回收的**停滞点**，直到重投成功、
或队列越界按丢弃语义处理——两个出口都是显式的。

> **教训**：删一层机制时，必须逐个问「这层除了它自己声明的职责，还顺带挡住了什么」。
> 恢复段声明的职责是「证明副本存在」，但它还顺带挡住了「UNKNOWN 被当成已投递」。

## 3. 已删除的（全部落地）

### ledger
| 内容 |
|---|
| `RecoveryRef` 类型、`Entry.RecoveryRefs` 字段及 `clone()` 里的拷贝 |
| `RegisterRecovery` / `TransitionRecovery` / `recoveryRank` / `SetRecoveryHold` / `HasRecoveryHold` |
| `tryReclaimLocked` 的寻段分支与 `CanReclaim` 门禁、凭据放行分支 |
| `ErrReclaimNoSegment` |
| `recovery_archive.go`（整文件：`RecoveryArchive*` 全套） |

### logtypes
`RecoverySegmentState` 常量组、`ReleaseReason` 常量组 + `ValidReleaseReason`、`CanReclaim`。

### stateindex
`recovery_audit.go`（整文件）。`Open` 里新增幂等的 `DROP TABLE recovery_audit` 迁移
（同 `source_wal` 的处理：旧库已经把表建在磁盘上了，不显式删就永远拖着）。

### ingest
`backfillDeliveryConfirmedCoverage` / `advanceDeliveryConfirmedSegment` /
`transferPreparedDeliveryPrefix` / `repairStalledReclaim` / `archiveCleanedRecovery` /
静默源出口的补段链（只留 `TryReclaim`）/ `recovery_archive_runtime.go`。

### acquire
`WAL.BindRecoverySegment` / `WAL.RecoverySegmentID` / `recoverySegID` 字段。

### pipeline
`BindRecoverySegment` / `TransitionRecovery` / `RecoveryRef` / `RecoveryCovering` / `SetRecoveryHold`；
`CanReclaimNow` 改为「是否还有已确认未被回收的前缀」。

## 4. 绝对不能删的（已守住）

- **`published_evidence.go` 一个字未动**：缺口消解（`gap_autoresolve.go` + `runtime.go` 的
  `autoResolveGapsFromPublished`）依赖 `capturePublishedEvidence` / `applyPublishedEvidence` /
  `currentPublishedRuns`。只服务补段的 `capturePublishedPrefixEvidence` 已随调用方一起删。
- `delivery_batch` 表（`Restore` 用它重算 `Positions.Delivery`）
- `PositionInvariantViolations`（reclaim ≤ read 观测面）
- `gap` 表与缺口机制、`Abandonments` 弃投凭据（人工裁定永久丢失仍可让回收跨过空洞）
- `WAL.TryReclaim*` 的剪枝动作（`pruneReclaimed`）—— 内存队列仍需它回收条目
- `releaseRecovery` 的 `Delivery >= to` 确认闸（2026-10-21 数据丢失事故的防线）

## 5. 跟着一起解棠的 17 条测试

### A 类（7 条，依赖「重启后队列还在」）
它们的**关切**（重启不丢数据）没变，但实现从「恢复队列」改成「重读」：
- 旧：Append → Commit → persist → Stop → 重启 → 断言队列还在
- 新：验「重启后 read/durable 退到 reclaim（`RollbackDurableToReclaim`），
  tailer 从 `min(durable, read)` 重读，那段能重新产生并投递」

### B 类（10 条，依赖补段/恢复段/投影证据）
机制删了，用例随之删或改验新语义。

## 6. 验收标准

- `reclaim` 能推进到 `min(REQUEST_DONE 连续覆盖末端, max(read,durable))`，且不需要任何恢复段 ✓
- 生产那 10 个停采源的 `reclaim` 能从 0 推进（部署后读 `ResumeDiagnostics`）
- 全包 `go test ./internal/worker/logs/...` 绿
- 启动后不会因为「找不到恢复段」而反复告警 ✓
- `go build ./...` / `go vet ./...` 零输出 ✓
