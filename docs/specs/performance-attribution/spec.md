# 性能归因（含 GC 采集）（FR-465）

> 状态：📋 计划　·　关联 PRD：FR-465　·　依赖：FR-060（时序底座，已交付）、ADR-013（分级降采样）、ADR-014（ServerProbe 为实例指标源）　·　关联：FR-464（容量预测，同用外推/相关分析）、`docs/specs/cluster-observability/spec.md`

## 1. 背景与目标

### 1.1 现状痛点

- **原始指标齐、归因缺失**：`inst_tps`/`inst_mspt`/`inst_heap_used`/`inst_threads`（`internal/controlplane/model/metric.go`）与分世界 `world_loaded_chunks`/`world_entities`/`world_tile_entities` 均已入 `metric_series`，但平台只画曲线。TPS 掉到 15 时，运维不知道是 **GC 抖动**、**区块/实体过载** 还是 **堆压力** 哪个是主因。
- **GC 指标已暴露却丢弃**：真机 `/metrics` 样本（`internal/worker/metrics/serverprobe_test.go` 的 `realProbeMetrics`）含 `# TYPE serverprobe_gc_count_total counter` 与 `serverprobe_gc_count_total{gc="G1 Young Generation"} 16`，但 `parseServerProbeMetrics`（`internal/worker/metrics/serverprobe.go`）**只解析固定 12 个指标**，`ProbeSnapshot`（结构体 18–28 行）无 GC 字段，GC 数据在 Worker 侧即被丢弃。

### 1.2 目标

- **先补采集**：把 `serverprobe_gc_*`（次数 + 耗时）解析、入 `ProbeSnapshot`、经心跳上报、由 CP 落 `metric_series`（沿用三档降采样）。
- **再做归因**：对指定窗口给出 TPS/MSPT 劣化的**主要贡献因子**（GC / 区块 / 实体 / 堆 / 线程）及权重，落实例详情展示。

### 1.3 范围外（不做）

- 不做 JVM 火焰图 / 线程 dump 级根因定位。
- 不引入因果推断模型；首版用相关性 + 标准化系数排序（标注「相关性非因果」）。
- 不改 ServerProbe 探针本体（只消费其已暴露的 `serverprobe_gc_*`）。

## 2. 设计

### 2.1 阶段一：GC 采集链路补全（4 跳）

GC 是 **cumulative counter**（真机样本 `serverprobe_gc_count_total 16`），不能直接存累计值当曲线用（重启归零、跨实例不可比）。沿用 CP 侧「据相邻差推导速率」的既有模式（`internal/controlplane/service/metric.go` 的 `netCounters`/`lastNet`）。

**① Worker 解析（`internal/worker/metrics/serverprobe.go`）**
```go
// ProbeSnapshot 追加：
GCCountTotal  int64   // Σ serverprobe_gc_count_total{gc=...}（跨收集器求和）
GCTimeMillis  float64 // Σ serverprobe_gc_time_seconds_total{gc=...} × 1000（收集器耗时秒→毫秒）
```
`parseServerProbeMetrics` 的 `switch s.name` 新增两个 case，按 `gc` 标签累加（收集器名如 `G1 Young Generation`）。

**② gRPC（`proto/worker.proto` `InstanceMetricSample`）** 追加向后兼容字段：
```proto
int64 gc_count_total = 13;      // 累计 GC 次数（counter，跨收集器求和）
double gc_time_millis = 14;     // 累计 GC 耗时（ms，counter）
```

**③ Worker 上报（`internal/worker/heartbeat/heartbeat.go` `collectInstanceMetrics`）** 抓到快照后 `sample.GCCountTotal = snap.GCCountTotal; sample.GCTimeMillis = snap.GCTimeMillis`。

**④ CP 入库（`internal/controlplane/service/metric.go` `ingestHeartbeatAt`）** 新增 `lastGC map[string]gcCounters`（与 `lastNet` 同构），据相邻心跳差推导速率：
```go
// 累计差 / 间隔秒；差为负（实例重启，counter 归零）时跳过该拍。
MetricInstGCCount = "inst_gc_count"      // 单位 count_per_sec：GC 次数速率
MetricInstGCTime  = "inst_gc_time_ms"    // 单位 ms_per_sec：GC 暂停占用（千分比语义）
```
两键加进 `internal/controlplane/model/metric.go`，随既有 `instSample(...)` 落库；探针不可用沿用 NULL 断点语义。同步 `docs/specs/timeseries-metrics/api.md` 指标键表。

### 2.2 阶段二：归因分析

新增 `internal/controlplane/service/attribution.go`，`AttributionService.Analyze(q)`：

- **对齐**：把窗口（默认 7d，走 5m rollup）内 TPS 与各候选因子按同一 `bucket_ts` 对齐成样本对；世界级因子（区块/实体/方块实体）先按 `instance_id` 跨 world **求和**成实例级序列。
- **候选因子**：`inst_gc_time_ms`（GC 占用）、`world_loaded_chunks`（区块）、`world_entities`（实体）、`world_tile_entities`（方块实体）、`inst_heap_used / inst_heap_max`（堆比）、`inst_threads`（线程）。
- **评分（决策：Pearson 相关 + 标准化系数）**：
  1. 对每个因子算与 TPS 的 Pearson 相关 r（TPS 越低因子越高时 r 为负）；
  2. 取 TPS 低于基线（窗口 P10 或固定阈值）的样本子集，算各因子相对窗口均值的 z 分；
  3. **贡献权重** = 归一化的 |r|·|z|；排序输出 TopN。
- **输出**：
```go
type AttributionResult struct {
    Window      struct{ From, To time.Time }
    TLDR        string   // 一句话结论：如「TPS 劣化主因：GC 暂停时间（权重 0.62）」
    Factors     []Factor // { MetricKey string; Correlation float64; Weight float64; Note string }
    Samples     int
}
```
- 样本不足（对齐点 < 30）时返回 `insufficient` 而不硬给排序。

### 2.3 API 与前端

| 端点 | 语义 |
|---|---|
| `GET /metrics/performance/attribution?scope=instance&targetId=&range=7d&metric=inst_tps` | `AttributionResult` |

路由挂 `internal/controlplane/router/metric.go` 的 `RegisterRoutes`；实例维度权限复用 `authz.CanAccessInstance`（与 `Series` 同口径）。前端：实例详情新增「性能归因」卡片（延迟加载，按需点击分析），GC 速率两条曲线并入实例时序图（复用 `packages/ui` 图表）。

## 3. 任务拆分

- [ ] ① Worker：`parseServerProbeMetrics` 解析 `serverprobe_gc_*` + `ProbeSnapshot` 加字段 + `serverprobe_test.go` 扩样本断言（依赖：无）。
- [ ] ② proto：`InstanceMetricSample` 加 13/14 字段 + `make proto` 重新生成（依赖 ①）。
- [ ] ③ Worker 心跳：`collectInstanceMetrics` 填充 GC 字段（依赖 ②）。
- [ ] ④ CP 入库：`lastGC` 速率推导 + 两个新 metric_key + 单测（含 counter 归零跳过）（依赖 ③）。
- [ ] ⑤ `attribution.go`：对齐 + 相关/权重（纯函数可测）+ 单测（依赖 ④）。
- [ ] ⑥ 路由 + 前端归因卡片 + 时序图加 GC 曲线（依赖 ⑤）。
- [ ] ⑦ 文档同步：本 spec、`timeseries-metrics/api.md` 指标键、PRD FR-465 状态、ARCHITECTURE/API。

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | GC 次数与耗时进入 `metric_series` 时序（`inst_gc_count`/`inst_gc_time_ms`），曲线可查 | 单测 + 真机 |
| 2 | counter 归零（实例重启）拍被跳过，不产生负速率假尖峰 | 单测（构造序列） |
| 3 | 探针不可用时 GC 指标写 NULL 断点，不补假值 | 单测 |
| 4 | 给出 TPS 劣化的主要贡献因子与权重，样本不足时返回 insufficient | 单测 + 真机 |
| 5 | 注入 GC 抖动（或区块暴涨）后，归因结论指向对应因子 | 真机（注入） |
| 6 | 实例维度归因对无权用户返回 403 | 单测 |

## 5. 风险 / 待定

- **GC 耗时指标名待确认**：真机样本只确证 `serverprobe_gc_count_total`；`serverprobe_gc_time_seconds_total`（或 `..._millis_total`）命名需对照 ServerProbe 源码/README 确认（submodule `third_party/ServerProbe` 当前工作树未检出）。若探针仅暴露次数不暴露耗时，「GC 占用」因子退化为次数速率。
- **counter 与并发**：`lastGC` 是 CP 内存态，CP 重启后首拍不出速率（与 `lastNet` 同局限，可接受）。
- **相关性非因果**：GC 与区块/实体常同源共变（都因负载上升），权重可能分散；首版接受，必要时 TS 引入偏相关或 FR-462 基线对照。
- **世界因子聚合口径**：跨 world 求和会掩盖单世界异常，后续可支持下钻到 world 维度。
