# 跨实例排行与玩家在线趋势（FR-469）

> 状态：🟡 **代码与单测已交付，真机验收待做**（R-1；FR-469 端到端已实现，单测/构建覆盖通过；§4 中标「真机」的验收项尚未在真机验证）　·　关联 PRD：FR-469　·　依赖：FR-060（时序底座）、FR-340（`POST /metrics/series/batch`，已交付）、FR-247（实例搜索分页）、FR-215（观测导航，`/monitor`）、ADR-013（分级降采样）　·　关联：FR-220（统计页）、FR-402/406（总览与监控整合）

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
    Timezone    string          // 实际生效时区（tz 不可解析时回退 UTC 并标注来源）
    Trend       []SeriesPoint   // 全网在线合计曲线（跨实例 sum，复用 aggregateTrend 的 sum 语义）
    HourlyDist  []float64       // 24 个时段（0~23 时）的平均在线（本地时区）
    PeakValue   float64
    PeakAt      *time.Time      // 可为 null：窗口内无有效点时不给峰值时刻
    DailyAvg    float64
}
```

- **趋势曲线**：直接复用 `MetricService.aggregateTrend(model.MetricScopeInstance, model.MetricInstPlayersOnline, "count", …, sum=true)`（`internal/controlplane/service/metric.go:705`），语义即「全网在线合计」。
- **时段分布（决策：本地时区按小时分桶求均值）**：把窗口内各桶值按其 `bucket_ts` 的**小时**（0~23）归并求均值。窗口按跨度自动选档（`selectResolution`：**≤6h→raw、≤30d→5m、更长→1h**；`metricRangeDurations` 已含 `1h`/`6h`/`24h`/`7d`/`30d`/`90d`/`1y`）。**必须声明时区口径**（默认服务器本地时区，避免 UTC 下「高峰在 12 点」的误导）。
  - **`tz` 解析失败不再 400**：回退 UTC 并在响应 `timezone` 中以 `"UTC (fallback from <原值>)"` 标注。原因是官方容器镜像（alpine）不含 tzdata、部分裸机亦缺 `/usr/share/zoneinfo`，而前端每次查询都下发浏览器时区——若 400，官方部署下该卡片必然不可用。CP 入口现内嵌 `time/tzdata` 保证合法时区名必可解析。
  - **半小时偏移时区**（如 `Asia/Kolkata`，UTC+5:30）：桶按整点（UTC）对齐，展示时落到 **:30**，即「该时区 00:00~00:30 的样本被计入 0 时段」。这是整点分桶的已知口径，前端按 `hour: 0` 展示不额外换算。
- **峰值/日均**：峰值取趋势曲线最大点（`PeakAt` 为 null 表示无有效点）；日均 = 曲线均值。

### 2.4 API 与前端

| 端点 | 语义 |
|---|---|
| `GET /metrics/instances/ranking?metric=&order=&window=&limit=&nodeId=` | `RankingResult`（窗口代表值排序；`window` 白名单 `5m/15m/1h/6h/24h/7d/30d`，默认 `5m`） |
| `GET /metrics/players/trend?range=7d&resolution=auto&tz=` | `PlayerTrendResult` |

挂 `internal/controlplane/router/metric.go` 的 `RegisterRoutes`。前端：
- 观测页（`/monitor`，FR-406）新增「全局排行」表（可切指标、点击下钻实例详情）；
- 统计页（FR-220）或观测页新增「玩家在线趋势」卡片（趋势折线 + 24 时段柱状图 + 峰值/日均）。

**`nodeId`/`nodeUuid` 的身份口径（B4）**：`nodeId` 是**节点 UUID**（不是数字 node id）——路由取 `c.Query("nodeId")` → `RankingQuery.NodeUUID` → SQL 按 `s.node_uuid = ?` 过滤；响应 `items[].nodeUuid` 同为**真实节点 UUID**（取自 `metric_series.node_uuid`）。前端 `InstanceRankingPanel` 用 `nodes.find(n => n.uuid === row.nodeUuid)?.name` 渲染「节点」列，节点筛选下拉的 value 也是节点 UUID——两侧必须同一身份，否则「节点」列解析不出名称（退化成截断 UUID，如 `node-moc…`）且筛选「选了没反应」。
- **假后端（`packages/devmock`）必须同口径**：`rankingSeed` 不得杜撰 `nodeUuid`，须经 `nodes` 集合把实例的 `nodeId` 映射成真实节点 UUID；并**消费** `nodeId` 参数。保持「取候选池 → 按节点过滤 → 排序 → `limit` 截断」的先后（若反过来每节点各取满额，节点筛选永远返回满额、看起来依然没反应）。`nodeId` 不在已 seed 节点中时与真后端一致返 404 `TARGET_NOT_FOUND`（不静默退化成全量榜）。

## 3. 任务拆分

- [x] ① `internal/controlplane/service/ranking.go`：`RankingQuery` + 单条聚合 SQL（不逐实例查）＋单测（缺测排除、排序方向、LIMIT）。
- [x] ② 权限收敛：复用 `AccessibleInstanceIDs`，`scoped` 标识＋单测（越权实例不入榜）。
- [x] ③ `PlayerTrend`：趋势复用 `aggregateTrend` + 时段分桶 + 峰值/日均＋单测（时区、空窗）。
- [x] ④ 路由 + DTO + `docs/API.md`（依赖 ①②③）。
- [x] ⑤ 前端：全局排行表 + 玩家在线趋势卡片（依赖 ④）。
- [x] ⑥ 文档同步：本 spec、PRD FR-469 状态、ARCHITECTURE/API。
- [ ] ⑦ **待真机验收**：全量实例真机排行（64 台/500 实例量级耗时）、前端卡片下钻（验收 4）、跨时区时段分布（验收 6）。

## 4. 验收标准

> **交付状态**：下表「方式」列的**单测**部分已由本次实现覆盖并通过；标 **真机** 的条目为**待真机验收**，故不标 ✅。

| # | 验收项 | 方式 | 状态 |
|---|---|---|---|
| 1 | 可按 TPS/MSPT/CPU/堆/在线排序**全量实例**（跨节点），返回名次与代表值 | 单测 + 真机 | [x] 单测 / [ ] 真机 |
| 2 | 窗口内无数据的实例不入榜，不显示 0 | 单测（负例） | [x] |
| 3 | 非管理员只看到自己可访问的实例排行，越权实例不出现 | 单测 + 真机 | [x] 单测 / [ ] 真机 |
| 4 | 排行点击可下钻到实例详情 | 真机（前端） | [ ] 待真机验收 |
| 5 | 给出全网在线趋势曲线 + 24 时段分布 + 峰值/日均 | 单测 + 真机 | [x] 单测 / [ ] 真机 |
| 6 | 时段分布时区口径正确（声明本地时区，与 UTC 下结果可区分） | 单测 | [x]（含 tz 回退 UTC 用例） |

## 5. 风险 / 待定

- **全量排序的性能**：64 台尚可，数百台时「分组求均值 + 排序」仍需走 rollup 档且加索引（`metric_series(metric_key, scope)` 可考虑）；若仍慢可引入 FR-461/462 的窗口聚合缓存。
  **实现取舍**：单条聚合 SQL 以样本表为驱动 JOIN `metric_series` + `instances`，按窗口跨度选档——排行窗口档为 **≤6h→raw、≤7d→5m、更长（≤30d）→1h**；`sampled_at` 用 `CAST(strftime('%s', MAX(ts)) AS INTEGER)` 取 unix 秒——SQLite 对聚合列直接扫描 `time.Time` 会报 `unsupported Scan`。
  - **30d 改走 1h 档（性能）**：500 实例 × 2016 个 5m 桶 ≈ 100.8 万行参与聚合（实测主聚合约 2.3s、未进榜计数约 0.9s），而前端 30s 轮询会持续堆积慢查询。30d 以上改走 1h 档（168 桶/实例，行数降约 12 倍）；代表值是窗口均值，1h 档均值与 5m 档加权均值在该场景等价，不改变语义。**仍未破坏「单条聚合 SQL」约束**（§2.1）——只是档位更粗。
  - **未进榜计数合并为单条 SQL**：原实现跑两条 `COUNT(DISTINCT s.instance_id)`（一条不限定时间窗、一条限定），现合并为一条条件聚合 `COUNT(DISTINCT CASE WHEN <在窗口内> THEN s.instance_id END)`，减半聚合开销。
  - **索引**：`metric_series` 补 `(scope, metric_key)` 复合索引（`idx_metric_series_scope_metric`，AutoMigrate 创建；与既有唯一索引 `idx_metric_series_identity` 不冲突），覆盖排行/SLO/趋势的「某 scope 的某指标」序列筛选。
    - **纯新增、与既有索引共存（合并安全）**：该索引是**追加的第二个索引标记**，不覆盖也不改动 `idx_metric_series_identity` 的列与优先级——`idx_metric_series_identity` 仍是 `UNIQUE(node_uuid, instance_id, scope, metric_key, world)`（priority 1–5）**逐字节未变**；新索引独立命名 `idx_metric_series_scope_metric`，故 GORM 不会因「同名已存在」而跳过建索引。二者语义正交：唯一索引保证序列身份不重复，复合索引服务按 (scope, metric_key) 的筛选/覆盖扫描。故本分支与未改该模型的 W10 分支均可合入 dev，二者共存不冲突。
    - **存量库由 AutoMigrate 补建**：本改动**不含破坏性迁移**，仅靠 `AutoMigrate(MetricSeries)` 在存量库上补建该索引。三库实测：`/tmp/e2e3/cp/jm.db` 与 `/tmp/e2e3-w9/cp/jm.db` 均已建出，`EXPLAIN QUERY PLAN SELECT id FROM metric_series WHERE scope='instance' AND metric_key='inst_tps'` 报 `SEARCH metric_series USING COVERING INDEX idx_metric_series_scope_metric (scope=? AND metric_key=?)`。
    - **反面影响**：该索引建出前，上述「某 scope 的某指标」筛选只能全表扫 `metric_series`。排行（FR-469）、SLO 可用率（FR-463）与玩家趋势（FR-469）都依赖它，故合并审查时若发现该索引缺失或命名被改，上述三处性能假设即失效。
- **代表值口径**：均值 vs 最新一拍 vs P95——MSPT 类「尖刺敏感」指标可能需要 P95 才有意义，首版用均值，待观测需求反馈。
  - **同实例多序列只占一席（M-6）**：主聚合按 `instance_id` 分组（**一行 = 一个实例**，§1.3 语义），而**不**按 `(instance_id, node_uuid)`——序列身份含 `node_uuid`，实例换节点后新旧序列都残留，按后者分组会让同一实例产出两行、rank 各占一名（审查实测：同 UUID 同 name 同时占 rank 1 与 rank 2，仅 `nodeUuid` 不同），且 `skippedNoData`（按 `DISTINCT instance_id` 计）与 `items` 口径不一致、`items` 数可超过 `limit`。
    - **代表值取跨序列全体样本的窗口均值**（不求和）：同一实例的多条序列是**同一测量的先后两段**，不是可加的分区；求和会让实例换节点后该指标凭空翻倍（与 `capacity.go` 的多序列口径一致——实例级序列收敛为一条后不相加）。
    - **`nodeUuid` 取自窗口内最新样本所属序列**（即实例迁移后的当前节点）：实现上把 `s.node_uuid` 保留为**裸列**（不在 `GROUP BY` 内、不受聚合包裹），并让 `MAX(v.<ts>)` 成为查询中**唯一**的 `max()` 聚合——SQLite 保证此时所有裸列取自该 max 所在行（已实测：旧序列 ts=100/200、新序列 ts=300/400 时裸列取到新节点）。`AVG` 不属于该规则（非 min/max），故代表值不受它影响。这条依赖 SQLite 的文档化行为，故在 `ranking.go` 的 doc 与代码注释中都写明了依据。
    - **代价**：实例换节点当口若新旧序列**都**有窗口内样本，均值会把两段混算。这是「一个实例一个代表值」的必然结果，比「同一实例占两席、各报一个部分均值」更贴近 spec 语义；若将来要「只取当前序列」，应显式按 `s.node_uuid = i.node_uuid` 过滤，而不是恢复 `(instance_id, node_uuid)` 分组。
- **时段分布时区**：**实现**为请求参数 `tz`（IANA 名，如 `Asia/Shanghai`），缺省服务器本地时区；响应回显 `timezone`，前端下发浏览器时区并在卡片角标展示，故「不同时区出不同时段分布」可验证。跨区运维按客户端时区切换的目标已达成。
  - **解析失败回退 UTC**（不 400）：宿主缺时区库时任何合法时区名都会解析失败，故 CP 入口内嵌 `time/tzdata`，并对确实非法的 `tz` 回退 UTC + 在 `timezone` 字段标注 `"UTC (fallback from <原值>)"`，前端据此显示提示——「下发了什么」与「实际用了什么」都可核对。
    - **回显串截断到 64 字节并加省略号（m6）**：该串拼的是**未净化的用户输入**，Gin 的 JSON 编码会正确转义（无注入风险）但原实现无长度上限——`?tz=<1MB 串>` 会原样进入响应体，响应尺寸由调用方决定。截断只影响诊断提示的可读长度，不影响实际生效时区（固定 UTC）；合法 IANA 名最长约 32 字节，64 留足余量又能把响应体钉在常数级。
  - **半小时偏移时区**：整点（UTC）分桶 + 按时区偏移展示，故 `Asia/Kolkata` 这类 +5:30 时区的时段边界落在 **:30**（0 时段覆盖其 00:00~00:30 样本）。整点分桶的已知口径，不在首版按半小时细分。
- **与 FR-340 的关系**：全局排行是新增能力，**未**放宽 `metricBatchMaxTargets`（50）与 `COMPARE_TARGET_CAP`（12）——排行不走 `/series/batch`，与既有批量对比的负载假设互不影响。
