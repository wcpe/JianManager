# reclaim 简化：改按投递水位推进

> 用户裁定（2026-10-29 05:12）：【两者都删（reclaim 改为按投递水位推进，不再要恢复段/投影证据）】

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

## 2. 新语义

```
reclaim 可推进到 min(delivery, read)
```

- 有 `delivery`（请求结果已确认的连续前缀）就可推进
- 不需要恢复段、不需要投影证据、不需要 `ReleaseReason`
- 不再有 `ErrReclaimNoSegment`、`ErrReclaimPrecondition`

推导：`delivery` 的语义是「所有字节均有请求结果的连续前缀」（`ledger.ContiguousDeliveryEnd`）。
它已经是比「恢复段覆盖」更强的证据——恢复段只在旧模型里用来证明「另一份副本已经安全存在」。
新模型下队列本来就不存副本，那份证据无从谈起也不必要。

## 3. 要删的

### ledger.go
| 位置 | 内容 |
|---|---|
| `RecoveryRef` 类型 | 整块 |
| `Entry.RecoveryRefs` | 字段 + `clone()` 里的拷贝 |
| `RegisterRecovery` / `TransitionRecovery` / `recoveryRank` | 整块 |
| `HasRecoveryHold` | 整块 |
| `tryReclaimLocked` 里找 `ref` 的分支 | 只保留「按 delivery/read 推进」 |
| `abandonmentIntersectingLocked` 凭据放行分支 | 只服务恢复段 |

### logtypes
- `RecoverySegmentState` 常量组
- `ReleaseReason` 常量组 + `ValidReleaseReason`
- `CanReclaim` 函数

### ingest
- `BindRecoverySegment` / `TransitionRecovery` / `RecoveryRef` 查询
- `backfillDeliveryConfirmedCoverage` / `advanceDeliveryConfirmedSegment`
- `transferPreparedDeliveryPrefix` / `releaseRecovery` / `silentExitNoCopy*`
- `hot_retention_window.go` 的 `abandonBeyondHotRetention`（为 reclaim 跨空洞而存在）
- `capturePublishedPrefixEvidence`（只服务补段）

### stateindex
- `recovery_audit` 表 + `ArchiveRecovery` 读写
- `ledger.RecoveryArchive` 接口

## 4. ⚠ 绝对不能删的

### `published_evidence.go` 只能删一半
调用面分两拨（已核实）：
- **缺口消解**（`gap_autoresolve.go:97,105,371,377` + `runtime.go:1629,1670`）
  ⇒ `autoResolveGapsFromPublished` 依赖 ⇒ **必须保留**
  `capturePublishedEvidence` / `applyPublishedEvidence` / `currentPublishedRuns`
- **补段（reclaim）**（`runtime.go:6236,6248,6330,9865,9923,9986,10165,10177`）
  ⇒ 随 reclaim 删
  `capturePublishedPrefixEvidence` 只服务补段 ⇒ **可删**

### 其它
- `delivery_batch` 表（`ledger.Restore` 用它重算 `Positions.Delivery`）
- `PositionInvariantViolations`（reclaim ≤ read 观测面）
- `gap` 表与缺口机制（那是“数据有没有丢”的记录，与 reclaim 无关）
- `WAL.TryReclaim*` 的**剪枝动作**（`pruneReclaimed`）—— 内存队列仍需它回收条目

## 5. 跟着一起解棠的 17 条测试

### A 类（7 条，依赖「重启后队列还在」）
它们的**关切**（重启不丢数据）没变，但实现从「恢复队列」改成「重读」。
例 `TestDurableOutboxDirtySecondThirdBootRecoverWithoutInput`：
- 旧：Append → Commit → persist → Stop → 重启 → 断言队列还在
- 新：应该验「重启后 read/durable 退到 reclaim，重读那段能重新产生并投递」
⇒ 必须先有 `reclaim > 0` 才验得了，所以只能与 reclaim 一起做。

### B 类（10 条，依赖补段/恢复段/投影证据）
机制删了，用例随之删或改验新语义（`reclaim = min(delivery, read)`）。

## 6. 执行顺序
1. 先改 `ledger`（新语义 + 删恢复段）→ `ledger` 包测试绿
2. 再改 `logtypes` → 跟着编译
3. 再改 `ingest` 调用面（删补段链）
4. 再改 `stateindex`（删 `recovery_audit`）
5. 最后处理 A/B 类 17 条测试
6. 全包回归 + 真机验证（生产那 10 个源 `reclaim` 应能从 0 推进）

## 7. 验收标准（可量化）
- `reclaim` 能推进到 `min(delivery, read)`，且不需要任何恢复段
- 生产那 10 个停采源的 `reclaim` 能从 0 推进（部署后读 `ResumeDiagnostics`）
- 全包 `go test ./internal/worker/logs/...` 绿
- 启动后不会因为「找不到恢复段」而反复告警
