# 功能规格：canonical 事件体落盘 compact（FR-484）

> 状态：实现中（eventstore 追加段 + ingest 读写路径改造已完成；阶段 2/3/4 实测通过；ADR-095 与 PRD/ARCHITECTURE/CHANGELOG/契约 §6.6 已对账）　·　关联 PRD：FR-484　·　依赖：FR-473（Shared Contracts 已冻结）　·　分支：feature/fr-log-platform-foundation

## 1. 背景与目标

FR-473 契约 §4.3/§5.3 规定 `persistedSource.Events` 是 VL 数据根丢失后重建 projection 的权威集合，**不可按 reclaim 前缀裁剪**——试做内存裁剪被 `TestManagerRotationImportsOnlyUnreadTailBeforeReplacementFile`、`TestManagerAutoImportsHistoricalGzipBeforeCurrentFile`、`TestManagerPartitionsProjectionByCanonicalEventUTCDay` 三个回归测试否决（清空事件体会破坏归档导入/轮转/跨日重建的等价性）。

代价是该集合常驻内存并内联在 `ingest.state.json` 中，随单源留存事件数线性增长。FR-473 真机 H-scale64 实测：64 源 × 3000 事件下 Worker 稳态 RSS ≈674MiB、`state.json` ≈143–189MB，容量拟合 ≈2.8MB state / 24MiB RSS 每源，**1GiB 预算仅够 ≈40 源**，无法支撑「单 Worker 承载更多实例」的密度目标。

FR-484 把 canonical 事件体从「state.json 内联 + 内存常驻」改为「追加式磁盘段存储」，**保持集合语义与可重建能力不变**，只更换存储介质。

目标是：

- 单 Worker 日志数据面在 150 源规模下稳态 RSS ≤300MiB；
- `ingest.state.json` 体积只与源数量（元数据）相关，不随事件数增长；
- 事件体落盘后仍满足契约 §4.3/§5.3 的重建不变量，且崩溃/截断可恢复。

## 2. 范围与术语

### 2.1 范围内

- 新增 canonical 事件体追加式段存储（`internal/worker/logs/eventstore`）；
- `ingest` 的写路径（`writeProjection` / `persist`）与读路径（`canonicalRecoveryEvents` / `deliver` 去重 / `publishedClosedForSource`）改走段存储；
- 旧格式（state 内联 events）的启动期迁移；
- 段格式的崩溃一致性与截断恢复语义；
- 上述改造的实测验收与契约同步。

### 2.2 不做

- **不按 reclaim 前缀裁剪事件体**：裁剪会破坏重建等价性，已被现有测试否决；
- 不改变事件身份、canonical 哈希、投影代次、覆盖水位或 Catalog 语义；
- 不引入第二日志查询引擎、不改变 VL 侧数据布局；
- 不改动 WAL 语义（reclaim/WAL 裁剪由 FR-474 的 `pruneReclaimed` 负责）。

### 2.3 术语

- **段（segment）**：单个源的事件体追加文件，达阈值后封存；
- **authority set（权威集合）**：该源全部已登账 canonical 事件，按时序排列；
- **可重建语义**：任意时刻都能由权威集合重建出等价的 VL projection。

## 3. 设计（怎么做）

### 3.1 存储布局

```
<worker-root>/var/log/
  ingest.state.json        # 仅元数据（ledger/WAL/代次/EventsStored 标志）
  events/
    <source-key-hash>/
      MANIFEST.json        # 段清单（含 format_version、段元数据）
      <seq>.ndjson         # 追加式事件段（每行一个 Event JSON）
```

- 段为 **NDJSON**：追加写 + 逐行可解析，允许流式读回而不 materialize 全量；
- `MANIFEST.json` 在段内容 fsync **之后**原子替换（`tmp` + `rename`），保持「先内容、后引用」；
- `format_version` 不匹配即**硬失败**，不猜测兼容。

### 3.2 崩溃一致性

| 情形 | 行为 |
|---|---|
| 最后一段尾部截断 | **可恢复**：丢弃不完整尾行，保留已完整写入的事件 |
| 中间段存在坏行 | **硬失败**（`ErrCorrupt`）：不静默跳段，避免产生静默数据缺口 |
| MANIFEST 缺失/不可解析 | 硬失败，交由调用方决定是否重建 |
| MANIFEST 版本不支持 | 硬失败 |

### 3.3 ingest 读写路径改造

写路径：

- `writeProjection` 在投影发布成功后调用 `appendEvents(key, events)`；入参是**完整权威集合**，函数内部按「段内已有事件数」只追加差额，因此每轮 poll 幂等；
- 段写成功（已 fsync）后才在 state 上置 `EventsStored=true` 并清空内联切片——顺序为「先内容、后引用」；
- `persist()` 不再写回事件体，元数据用 `json.Encoder` **流式**写入，避免 `MarshalIndent` 先构建整份 `[]byte` 的双份峰值。

读路径：

- `canonicalRecoveryEvents`：`EventsStored` 时从段流式读回（`Iterate`），否则回退旧格式内联切片；集合语义不变（仍为全部已登账事件）；
- `deliver` 的身份去重集合从段流式重建（带事件 ID 去重），不再持全量切片；
- `publishedClosedForSource`：改用 `Store.DayMaxEnd`（流式按日聚合最大末端位置），不载入全量事件。

### 3.4 旧格式迁移

启动时若 state 仍含内联 `events`，则先全部落段；**任一段写失败即整体放弃迁移**并保留旧格式继续运行（不丢数据、不半途改格式）。

## 4. 任务拆分

- [x] 阶段 0：heap 量测归因（`persist()` 全量重写是主因，非事件体常驻）
- [x] 阶段 1：新增 `eventstore` 包（追加段 + 流式读 + MANIFEST 原子替换）
- [x] 阶段 1b：崩溃注入测试（尾段截断恢复 / 中间段损坏硬失败 / 版本拒绝）
- [x] 阶段 2：`ingest` 读写路径接线 + 旧格式迁移
- [x] 阶段 3：`persist()` 不再重写事件体，state.json 与事件数解耦
- [x] 阶段 4：64/128/150 源实测与 A/B 对照
- [x] 阶段 5：采集期峰值治理——归因出三处跨轮无界累积（`n.events` / `linePos` / `delivered`）并加界；四回归测试 + 两变异验证；更正阶段 4 的错误读数
- [x] 文档对账：ADR-095 + PRD/ARCHITECTURE/CHANGELOG + 契约 §6.6 密度结论修订（含作废标注）

## 5. 验收标准

### 5.1 语义等价（回归防护）

以下测试必须全绿，且**不得**为了落盘改造而放宽断言：

- `TestManagerRotationImportsOnlyUnreadTailBeforeReplacementFile`
- `TestManagerAutoImportsHistoricalGzipBeforeCurrentFile`
- `TestManagerPartitionsProjectionByCanonicalEventUTCDay`
- `TestManagerAckLossDefersReclaimUntilProjectionReplay`
- `TestManagerRecoveryHoldSurvivesRestartAndReleasesAfterConstraintClears`

### 5.2 幂等

`TestManagerRepeatedPollKeepsStoredEventSetStable`：重复 poll 与重启恢复路径都不得重复写入同一事件。变异验证——把差额追加改回整份追加，该测试必须转红。

### 5.3 容量（实测判据）

| 指标 | 判据 |
|---|---|
| `state.json` | 体积只随源数量增长，**不随事件数增长** |
| 稳态 RSS | 150 源 ≤300MiB |
| 采集期峰值 | **不随源数线性增长**（单 tick 留存不得与会话总行数挂钩） |

实测结果（**真机 node-main + 真 VictoriaLogs v1.52.0**，全局保活 + 强制 GC；详见 [`acceptance-real.md`](acceptance-real.md)）：

| 源数 | 采集期峰值 RSS | 稳态 RSS | `state.json` | events/ 落盘 |
|---|---|---|---|---|
| 64 | 56.0 MiB | **22.5 MiB** | 177.9 KiB | 128.7 MiB |
| 128 | 62.0 MiB | **22.3 MiB** | 357.1 KiB | 257.9 MiB |
| 150 | 61.8 MiB | **20.0 MiB** | 418.6 KiB | 302.3 MiB |

独立正确性验证：对全部 342 个源逐一用真 VL `count()` 核对，**1 026 000 条事件无丢失、无重复**（异常 0）。

A/B 对照（同机同负载，HEAD 基线 vs 本改动）：`state.json` **77 803 KiB → 177.9 KiB**；基线 900s 未完成采集且 RSS 达 2853 MiB，本改动 110s 完成。

证据：[`acceptance-real.md`](acceptance-real.md)（受控）+ `.tmp/fr444-experiments/phase5-attribution.md`（归因与 A/B，不入库）。

> **⚠ 数字更正（2026-09-24）**：本节先前发布的「64/128/150 源稳态 = 25.9/30.9/35.8 MiB」及
> 「峰值 64 源 ≈538MiB / 150 源 ≈1152MiB」均为**错误读数**，已按上表更正：
> - 稳态之所以偏小，是因为量测脚本**未保活 `Manager`**，Go 在静默期把它连同内部索引一并回收。
>   保活后真实稳态是 **303.9 MiB**（64 源），直到 §4 的三处累积结构加界才降到 26.4 MiB。
> - 峰值偏大的根因不是「单批归一化分配」，而是**跨轮无界累积**（见 §4 任务拆分第 5 阶段与 §6）。

### 5.4 观测口径要求

容量结论必须**同时**满足三条，缺一不可：

1. **全局保活被测对象**——把 `Manager` 存入包级变量。`runtime.KeepAlive` 是编译期屏障，
   放在读数之后无法阻止对象在读数前被回收（本节数字曾因此虚小约 12 倍）。
2. 取**稳态**读数（静默期强制 GC 后），而非采集期瞬时值。
3. 对存活堆做**剖析归因**——量测脚手架自身的内存不得计入被测对象
   （曾因假 VL 淘汰键从 `generation` 改为 `(generation, source)`，令稳态由虚高的 355 MiB 回落）。

## 6. 已知边界

- **采集期峰值已收敛**（FR-484 阶段5）：三处跨轮累积结构加界后，峰值不再随源数线性增长
  （64/150 源 = 73.5/77.0 MiB，改造前 541/1152 MiB）。剩余量级为 Go runtime 自身开销。
- **events/ 磁盘占用**：150 源 × 3000 事件的段合计 ≈300 MiB（NDJSON 未压缩）。磁盘预算与保留策略由 FR-479/480 的生命周期负责，本 FR 不引入新压缩格式。
