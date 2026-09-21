# 跨实例排行与玩家在线趋势（FR-469）

> 状态：📋 计划　·　关联 PRD：FR-469　·　依赖：FR-060（时序底座）、FR-340（`POST /metrics/series/batch`，已交付）、FR-247（实例搜索分页）、FR-215（观测导航，`/monitor`）、ADR-013（分级降采样）　·　关联：FR-220（统计页）、FR-402/406（总览与监控整合）

## 1. 背景与目标

### 1.1 现状痛点

- **跨实例对比被三重限死（FR-340）**：`NodeInstanceCompare`（`apps/control-plane-web/src/pages/NodesPage.tsx:241`）只在**单个节点内**取实例，`COMPARE_TARGET_CAP = 12`（前端只取名称升序前 12），`COMPARE_METRICS` 只 4 个键（`inst_tps`/`inst_mspt`/`inst_heap_used`/`inst_threads`），后端 `POST /metrics/series/batch` 硬上限 `metricBatchMaxTargets = 50`（`internal/controlplane/router/metric.go`）。**没有跨节点全局排行**——64 台实例时无法回答「全网 TPS 最差的 10 台是谁」。
- **无玩家在线趋势/时段分析**：只有 `inst_players_online` 时序曲线（`internal/controlplane/model/metric.go`）与 `PlayersPage` 的**实时**名单；`Overview` 的 `aggregateTrend` 会跨序列 sum 出「总在线」曲线，但**没有时段分布（几点是高峰）、峰值、日维度趋势**。

### 1.2 目标

- 新增**跨节点/跨实例全局排行**：可按 TPS / MSPT / CPU / 堆内存 / 在线人数排序全量实例（受权限收敛），带窗口聚合与排名。
- 新增**玩家在线趋势与时段分析**：全网在线人数的历史趋势曲线 + 按小时（时段）分布 + 峰值/日均。

### 1.3 范围外（不做）

- 不替换既有 `NodeInstanceCompare`（节点内对比保留）；本 FR 是**新增全局视角**。
- 不做玩家个人跨实例迁移/画像分析（属 FR-054 玩家管理域）。
- 排行首版**不跨节点聚合同一实例的多序列**——一个实例一行，取该实例的实例级序列值。

## 2. 设计

### 2.1 排行后端：`InternalInstanceRanking`

现状瓶颈在「逐实例多次查询」——`QuerySeriesBatch` 是「N 目标逐序列 `queryPoints`」。排行需要的是**每实例一个代表值**，是另一种查询形态（与 `QueryProcessTop` 的「取每实例最新一拍再排序」同族）。故新增 `internal/controlplane/service/ranking.go`：

```go
type RankingQuery struct {
    MetricKey  string    // inst_tps | inst_mspt | inst_cpu_pct | inst_heap_used | inst_players_online
    Order      string    // asc | desc（默认按指标语义定方向）
    Window     time.Duration // 代表值的聚合窗口（默认 5m）
    Limit      int       // 默认 20，上限 100
    NodeUUID   string    // 可选：限定节点
}
type RankingItem struct {
    InstanceID, InstanceUUID, Name, NodeUUID string
    Value    float64
    Rank     int
    SampledAt time.Time
}
```

- **代表值（决策：窗口内均值）**：取每实例在 `[now-Window, now]` 内该指标的样本均值（走 5m/1h rollup，避免拉 raw）。均值比「最新一拍」稳健，不受单拍抖动影响；`SampledAt` 返回窗口末样本时刻。
- **SQL 形态**：以 `metric_series`（`scope=instance AND metric_key=?`）为驱动，JOIN `metric_sample_raw`/`metric_rollup_5m` 按 `series_id` 分组求 `AVG/MAX(ts)`，再 JOIN `instances`（`metric_series.instance_id` 存的是实例 UUID）取名称与节点，最后按值排序 LIMIT。与 `QueryProcessTop` 的 `JOIN (…) AS latest ON …MAX(sampled_at)` 同一模式。
- **缺测语义**：窗口内无样本的实例**不进榜**（不伪造 0）；需前端区分时可在响应附 `skippedNoData` 计数。

### 2.2 排行权限（关键）

- 平台管理员：全量实例可排。
- 非管理员：复用 `AuthzService.AccessibleInstanceIDs`（`internal/controlplane/router/metric.go:341` 已在用）取可访问实例 ID 集，排行在集合内进行——**与 `SeriesBatch` 的「剔除越权目标」一致**，不整拒。响应带 `scoped:true/false` 标识是否为受限视图。

### 2.3 玩家在线趋势：`PlayerTrend`

新增 `PlayerTrend(from, to, resolution)`，落在同一 `ranking.go` 或 `metric.go`：

```go
type PlayerTrendResult struct {
    Resolution  string
    Trend       []SeriesPoint   // 全网在线合计曲线（跨实例 sum，复用 aggregateTrend 的 sum 语义）
    HourlyDist  []float64       // 24 个时段（0~23 时）的平均在线（本地时区）
    PeakValue   float64
    PeakAt      time.Time
    DailyAvg    float64
}
```

- **趋势曲线**：直接复用 `MetricService.aggregateTrend(model.MetricScopeInstance, model.MetricInstPlayersOnline, "count", …, sum=true)`（`internal/controlplane/service/metric.go:705`），语义即「全网在线合计」。
- **时段分布（决策：本地时区按小时分桶求均值）**：把窗口内各桶值按其 `bucket_ts` 的**小时**（0~23）归并求均值。窗口可选 7d/30d：7d 走 5m 档、30d 走 1h 档（`selectResolution` 自动选档，`metricRangeDurations` 已含 `7d`/`30d`）。**必须声明时区口径**（默认服务器本地时区，避免 UTC 下「高峰在 12 点」的误导）。
- **峰值/日均**：峰值取趋势曲线最大点；日均 = 曲线均值。

### 2.4 API 与前端

| 端点 | 语义 |
|---|---|
| `GET /metrics/instances/ranking?metric=&order=&window=&limit=&nodeId=` | `[]RankingItem`（窗口代表值排序） |
| `GET /metrics/players/trend?range=7d&resolution=auto&tz=` | `PlayerTrendResult` |

挂 `internal/controlplane/router/metric.go` 的 `RegisterRoutes`。前端：
- 观测页（`/monitor`，FR-406）新增「全局排行」表（可切指标、点击下钻实例详情）；
- 统计页（FR-220）或观测页新增「玩家在线趋势」卡片（趋势折线 + 24 时段柱状图 + 峰值/日均）。

## 3. 任务拆分

- [ ] ① `internal/controlplane/service/ranking.go`：`RankingQuery` + 单条聚合 SQL（不逐实例查）＋单测（缺测排除、排序方向、LIMIT）。
- [ ] ② 权限收敛：复用 `AccessibleInstanceIDs`，`scoped` 标识＋单测（越权实例不入榜）。
- [ ] ③ `PlayerTrend`：趋势复用 `aggregateTrend` + 时段分桶 + 峰值/日均＋单测（时区、空窗）。
- [ ] ④ 路由 + DTO + `docs/API.md`（依赖 ①②③）。
- [ ] ⑤ 前端：全局排行表 + 玩家在线趋势卡片（依赖 ④）。
- [ ] ⑥ 文档同步：本 spec、PRD FR-469 状态、ARCHITECTURE/API。

## 4. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | 可按 TPS/MSPT/CPU/堆/在线排序**全量实例**（跨节点），返回名次与代表值 | 单测 + 真机 |
| 2 | 窗口内无数据的实例不入榜，不显示 0 | 单测（负例） |
| 3 | 非管理员只看到自己可访问的实例排行，越权实例不出现 | 单测 + 真机 |
| 4 | 排行点击可下钻到实例详情 | 真机（前端） |
| 5 | 给出全网在线趋势曲线 + 24 时段分布 + 峰值/日均 | 单测 + 真机 |
| 6 | 时段分布时区口径正确（声明本地时区，与 UTC 下结果可区分） | 单测 |

## 5. 风险 / 待定

- **全量排序的性能**：64 台尚可，数百台时「分组求均值 + 排序」仍需走 rollup 档且加索引（`metric_series(metric_key, scope)` 可考虑）；若仍慢可引入 FR-461/462 的窗口聚合缓存。
- **代表值口径**：均值 vs 最新一拍 vs P95——MSPT 类「尖刺敏感」指标可能需要 P95 才有意义，首版用均值，待观测需求反馈。
- **时段分布时区**：本地时区 vs UTC 需产品拍板；跨区运维可能希望按客户端时区切换。
- **与 FR-340 的关系**：全局排行是新增能力，是否顺带放宽 `metricBatchMaxTargets`（50）与 `COMPARE_TARGET_CAP`（12）尚待确认——本 FR 不强制，避免影响既有批量对比的负载假设。
