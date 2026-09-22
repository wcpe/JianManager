# API Spec — FR-060 时序监控与历史曲线

> 关联 FR: FR-060 | 优先级: P1 | 状态: 🔨 in-progress（后端完成，前端待 FR-061） | 关联 ADR: ADR-013（分级降采样存储）；ADR-014（ServerProbe 为实例指标源）

## 概述

在 FR-010 实时指标之上增加**时序存储与历史曲线**。指标已在流动——节点指标经 30s 心跳、实例富指标经 ServerProbe（ADR-014）；本 FR 把它们沉淀为时序：Worker 在心跳里附带「节点指标 + 每实例 ServerProbe 快照」，Control Plane 分级降采样持久化（ADR-013），前端按总览/节点/实例三级查询历史曲线。Worker 不碰 DB——采集经心跳上报、CP 落库。

指标来源：
- **节点级**（心跳既有 collector）：CPU% / 内存(used,total) / 磁盘(used,total) / 网络收发速率
- **实例级**（`ScrapeServerProbe` 抓本机探针 `/metrics`）：TPS / MSPT / 在线人数 / 堆(used,max) / 线程 / 系统 CPU / uptime；探针不可用回退 RCON（仅 TPS/在线）
- **分世界**（ServerProbe `serverprobe_world_*`，**现成可用、无需插件桥**）：已加载区块 / 实体 / 方块实体

## 数据模型

### `metric_series`（序列维度表）
| 字段 | 类型 | 说明 |
|---|---|---|
| id | uint PK | |
| node_uuid | varchar(64) | 所属节点 |
| instance_id | varchar(64) | 实例级/世界级序列才有；节点级为空 |
| scope | varchar(16) | `node` \| `instance` \| `world` |
| metric_key | varchar(48) | 见指标键表 |
| world | varchar(64) | `scope=world` 时的世界名，其余为空 |
| unit | varchar(16) | `pct`\|`bytes`\|`bytes_per_sec`\|`count`\|`ms`\|`tps`\|`seconds` |
| created_at / last_seen_at | datetime | |

唯一索引 `UNIQUE(node_uuid, instance_id, scope, metric_key, world)`（`idx_metric_series_identity`）。

另有**非唯一**复合索引 `(scope, metric_key)`（`idx_metric_series_scope_metric`），由 `AutoMigrate` 在**存量库**上一并补建（无需破坏性迁移）。它与上面的唯一索引**独立命名、共存**，唯一索引的列与优先级不受影响。用途：排行（FR-469）、SLO 可用率（FR-463）、玩家趋势（FR-469）等按「某 scope 的某指标」筛序列的查询，无它则只能全表扫 `metric_series`；`EXPLAIN QUERY PLAN` 走 `COVERING INDEX`。

### `metric_sample_raw`（原始，留 ~48h）
`(series_id, ts, value double NULL)`，索引 `(series_id, ts)`；`value=NULL` 表示缺测。

### `metric_rollup_5m`（留 ~30d）/ `metric_rollup_1h`（留 ≥400d）
`(series_id, bucket_ts, avg, min, max, last, count)`，索引 `(series_id, bucket_ts)`。

### 指标键
| scope | metric_key | unit | 来源 |
|---|---|---|---|
| node | node_cpu_pct | pct | 心跳 |
| node | node_mem_used / node_mem_total | bytes | 心跳 |
| node | node_disk_used / node_disk_total | bytes | 心跳 |
| node | node_net_rx_rate / node_net_tx_rate | bytes_per_sec | 心跳（CP 据累计字节算速率） |
| instance | inst_tps | tps | ServerProbe |
| instance | inst_mspt | ms | ServerProbe |
| instance | inst_players_online | count | ServerProbe（回退 RCON） |
| instance | inst_heap_used / inst_heap_max | bytes | ServerProbe |
| instance | inst_threads | count | ServerProbe |
| instance | inst_cpu_pct | pct | ServerProbe `system_cpu_load`×100 |
| instance | inst_uptime | seconds | ServerProbe |
| instance | inst_gc_count | count_per_sec | ServerProbe `serverprobe_gc_count_total`（FR-465：CP 据相邻心跳累计差算速率） |
| instance | inst_gc_time_ms | ms_per_sec | ServerProbe `serverprobe_gc_time_seconds_total`（FR-465：累计秒→毫秒后算速率；探针仅暴露次数时该因子退化） |
| world | world_loaded_chunks / world_entities / world_tile_entities | count | ServerProbe（`world` 标签） |

## gRPC 变更（`proto/worker.proto`）

`HeartbeatRequest` 追加（向后兼容；分世界复用既有 `WorldMetric`）：
```proto
message InstanceMetricSample {
  string instance_uuid = 1;
  bool probe_available = 2;            // false = 回退 RCON / 缺测
  double tps = 3; double mspt_millis = 4; int32 players_online = 5;
  int64 heap_used_bytes = 6; int64 heap_max_bytes = 7; int32 threads = 8;
  double cpu_load = 9;                 // 0~1 系统 CPU；<0 不可用
  double uptime_seconds = 10;
  repeated WorldMetric worlds = 11;    // 复用 GetInstanceMetrics 既有 WorldMetric
  // FR-465 追加（**字段号 24/25**：13/14 已被预留的 motd/version 占用，FR-465 spec 原稿写的 13/14 已按现状顺延）
  int64 gc_count_total = 24;           // Σ serverprobe_gc_count_total{gc=...}（cumulative counter，跨收集器求和）
  double gc_time_millis = 25;          // Σ serverprobe_gc_time_seconds_total{gc=...} × 1000（cumulative counter）
}
// HeartbeatRequest  += repeated InstanceMetricSample instance_metrics = 10;
// CreateInstanceRequest += int32 probe_port = 12;  // CP 分配后下发，Worker 持久化到 PID 记录
```
Worker 心跳 tick 对每个 RUNNING 且 `ProbePort>0` 的实例 `ScrapeServerProbe(localhost, ProbePort)`，装入 `instance_metrics`（探针不可用 `probe_available=false`）。ProbePort 经 `CreateInstanceRequest.probe_port` 喂给 Worker，daemon 模式透传到 wrapper→PID 记录，Worker 重启经 `RecoverDaemonInstances` 恢复。CP 收心跳经 `IngestHeartbeat` → upsert series → 写 raw（缺测/探针不可用写 NULL；网络速率据相邻累计字节差算）。

## REST API

### GET /api/v1/metrics/series
- **权限**: 登录；按 `scope`/`targetId` 校验 RBAC 读（平台管理员全量；组成员仅有权节点/实例）。
- **Query**: `scope`(node|instance) 必填；`targetId` 必填（node_uuid 或 instance_id）；`metrics` 可选（逗号分隔；`scope=instance` 且含 `world_*` 时按 `world` 维度返回多序列）；`range`(1h|6h|24h|7d|30d|90d) 或 `from`/`to`；`resolution`(auto|raw|5m|1h，默认 auto：≤6h→raw、≤30d→5m、>30d→1h)。
- **响应 200**:
```json
{ "resolution": "5m", "from": "...", "to": "...",
  "series": [
    { "metricKey": "inst_tps", "unit": "tps", "world": "",
      "points": [ { "ts": "...", "avg": 19.8, "min": 14.2, "max": 20.0 } ] },
    { "metricKey": "world_entities", "unit": "count", "world": "world_nether",
      "points": [ { "ts": "...", "avg": 312 } ] }
  ] }
```
（raw 档 `points` 为 `{ts, value}`，缺测 `value:null`。）
- **错误**: 400 `INVALID_SCOPE`/`INVALID_RANGE`/`INVALID_RESOLUTION`；403 `FORBIDDEN`；404 `TARGET_NOT_FOUND`。

### GET /api/v1/metrics/overview
- **权限**: 登录（聚合总量与曲线，不暴露单实例明细；与 node 维度指标一致）。
- **Query**: `range`(1h|6h|24h|7d|30d|90d|1y) 或 `from`/`to`（默认 24h）；`resolution`(auto|raw|5m|1h)。
- **响应 200**（实际实现形状）:
```json
{ "totals": { "nodeCount": 3, "onlineNodeCount": 2, "runningInstances": 5,
              "cpuPct": 47.5, "memUsedBytes": 3221225472, "memTotalBytes": 8589934592,
              "onlinePlayers": 12 },
  "resolution": "5m",
  "trends": [
    { "metricKey": "node_cpu_pct", "unit": "pct", "points": [ { "ts": "...", "avg": 47.5 } ] },
    { "metricKey": "node_mem_used", "unit": "bytes", "points": [ { "ts": "...", "avg": 3.2e9 } ] },
    { "metricKey": "inst_players_online", "unit": "count", "points": [ { "ts": "...", "avg": 12 } ] }
  ] }
```
- `totals` 取 Node/Instance 表当前值 + 各实例最近 2min 在线人数合计；`trends` 跨序列按档位桶对齐后聚合（CPU 取均值、内存/玩家取合计）。`alertsActive` 不纳入（属 FR-011 告警域）。
- **错误**: 400 `INVALID_RANGE`/`INVALID_RESOLUTION`；403 `FORBIDDEN`。

## 错误码汇总
| HTTP | error | 场景 |
|---|---|---|
| 400 | INVALID_SCOPE / INVALID_RANGE / INVALID_RESOLUTION | 参数非法 |
| 400 | INVALID_METRIC / INVALID_ORDER / INVALID_WINDOW / INVALID_LIMIT / INVALID_TARGET / INVALID_THRESHOLD / INVALID_TZ | FR-463/464/465/469 新端点参数非法 |
| 403 | FORBIDDEN | 越权访问节点/实例指标 |
| 404 | TARGET_NOT_FOUND | 节点/实例不存在 |
| 422 | TOO_MANY_TARGETS | 批量对比目标 > 50 |
| 500 | INTERNAL_ERROR | 查询/聚合失败 |

## FR-463/464/465/469 派生端点（同一 `/metrics` 路由组）

| 端点 | 语义 | 权限 |
|---|---|---|
| `GET /metrics/performance/attribution?scope=instance&targetId=&range=7d&metric=inst_tps` | 性能归因（FR-465） | instance 维度 `CanAccessInstance`，无权 403 |
| `GET /metrics/instances/ranking?metric=&order=&window=&limit=&nodeId=` | 跨实例全局排行（FR-469） | 非管理员按 `AccessibleInstanceIDs` 收敛（`scoped=true`），不整拒 |
| `GET /metrics/players/trend?range=7d&resolution=auto&tz=` | 玩家在线趋势 + 24 时段（FR-469） | 登录（聚合总量，与 overview 同口径） |
| `GET /metrics/slo?scope=platform\|node\|instance&targetId=&range=&target=` | 可用性/SLO（FR-463） | platform/node 登录即可（platform 对非管理员收敛到可访问实例）；instance 无权 403 |
| `GET /metrics/capacity/forecast?scope=&targetId=&metrics=&range=7d&thresholdDays=` | 容量耗尽预测（FR-464） | node 登录即可；instance 无权 403 |

- **FR-465**：`AttributionResult{target,window,status,tldr,factors[],samples}`，`status=insufficient`（对齐点 <30 或无显著因子）时 `factors` 为空，不伪造排序。
- **FR-469**：`RankingResult{metricKey,order,windowSeconds,scoped,skippedNoData,items[]}`，代表值取窗口内均值；窗口内无样本的实例不入榜。支持指标：`inst_tps|inst_mspt|inst_cpu_pct|inst_heap_used|inst_players_online`（`limit` 1~100，`window` ≤30d）。排行**不复用** `/series/batch`，走单条聚合 SQL。
- **FR-463**：`SLOResult{availability,totalSamples,upSamples,incidents,activeIncidents,mttrSeconds,mtbfSeconds,budgetAllowedSec,budgetBurnedSec,target,approximatedBuckets}`。可用证据 = 非 NULL `inst_uptime` 拍（节点维度用 `node_cpu_pct` 拍）；分母按窗口/30s 推算，缺拍计不可用；**MTTR/MTBF 无故障时为 `null`，不是 `Infinity`**；窗口 >48h 按 rollup 桶近似（`approximatedBuckets=true`，一个 5m 桶最多 10 拍）。
- **FR-464**：`{forecasts:[ForecastResult]}`，Theil–Sen 稳健斜率 + 残差/斜率标准误合成的 80% 区间。`confidence=insufficient` 表示样本不足 / β≤0 / β 的 80% 区间跨 0（趋势不显著），此时 `exhaust*` 全为 `null` 且 `note` 给出原因。命中「预计 N 天内耗尽」时经既有 metric 规则发趋势告警（DedupKey 含 target+metricKey，去抖）。
- **总览聚合成本（N-8，决策）**：`/metrics/overview` 的 `totals` 与 `trends` 曾按序列逐条查询（每条趋势序列 1~2 次往返），而总览页每 **10s** 轮询（`useMetricOverview` 的 `refetchInterval`）——成本会随实例数**线性**增长。现已全部改为**常数条聚合 SQL**（`aggregateTrend` 用内层 `GROUP BY series_id, 桶` + 外层 `GROUP BY 桶` 的单条两级聚合；`latestSum` 用 `MAX(ts)`+`MAX(id)` 自联接的单条查询），实测 4 序列与 40 序列的 SQL 条数相同（7 条），不再随实例数增长。
  - **`latestSum` 的「每序列只计一行」是硬约束（B-1）**：**不能**用 `latest.mts = v.ts` 这类等值自联接收尾——`metric_sample_raws` 无任何唯一约束（仅 `id` 自增主键）、`idx_metric_raw_series_ts` 是**普通索引非 UNIQUE**、`Ingest` 对样本**纯追加**且无 `OnConflict` 去重，故心跳重放/重试会产生同 `(series_id, ts)` 多行；按 `ts` 等值匹配会让这些行**全部**计入外层 `SUM`，使在线人数按重放次数翻倍（实测：真实 7 报 21）。
    - **「取一行」的写法有讲究**：子查询只带 `MAX(id)` 并让 ON 锚定它（`latest.mid = v.id`）**不够**——那是按写入先后而非 `ts` 取值，心跳重试让更旧的拍后到时会把陈旧值当当前值报出（实测：最新拍 7 + 陈旧拍 3 后到 → 错报 3）。而让 ON 同时要求 `mts = v.ts AND mid = v.id` **更糟**——`MAX(ts)` 与 `MAX(id)` 是**独立**聚合，乱序到达时二者未必取自同一行，没有任何行能同时满足两个条件，该序列的 SUM 会丢成 0。
    - **正解**：先用 `MAX(ts)` 收敛到「最新拍」（保留原 JOIN 形态），再用相关子查询在该 `ts` 内取 `MAX(id)` 决胜——`AND v.id = (SELECT MAX(v3.id) … v3.ts = v.ts)`。「窗口内最新非空拍」的时间语义与改造前完全一致，仅把「同刻多行全取」收紧为「同刻取最后一次观测」（同刻多行只可能是重放，后到者才是最新观测）。子查询只对「已是最新拍的候选行」求值、命中 `(series_id, ts)` 索引，仍是**单条**聚合 SQL，不恢复 N+1（`TestMetric_OverviewTrendQueryCountIsConstant` 守住常数条查询）。
  - `1y`（365d）区间的代价因此**不再是往返次数**，而是单次聚合的扫描行数：`selectResolution` 对 >30d 选 `1h` 档，`1y` 下每序列约 8760 桶，故扫描行数 ≈ `场景数 × 8760`。这是 ADR-013 三档降采样的固有取捨，不是新的规模化隐患；若将来实例数增长到让单次聚合超时，应下沉为「跨序列预聚合表」而非恢复逐序列查询。
  - 时间分桶用 `strftime('%s', ts) / 桶秒`，**不是**把 `MIN(ts)` 截断到桶起点：`raw` 档存储文本带纳秒与时区偏移、rollup 档为分钟对齐，直接截断文本会与 Go 侧 `TS.Truncate(bucket)` 得出不同结果。

## 一致性
- 与 `docs/ARCHITECTURE.md` 数据库模型章节（新增 4 张 metric 表）+ 通信协议章节（`Heartbeat` 负载扩展）一致——实现时同步。
- 与 ADR-013（分级降采样）、ADR-014（ServerProbe 实例指标源）、ADR-002（gRPC）一致；细化 FR-010。
- 实例指标源为 ServerProbe（`internal/worker/metrics/serverprobe.go` `ProbeSnapshot`）；不新增深度指标采集路径、不依赖已退役的 FR-103 插件桥。
