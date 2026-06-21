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

唯一索引 `UNIQUE(node_uuid, instance_id, scope, metric_key, world)`。

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
- **Query**: `range`(1h|6h|24h|7d|30d|90d) 或 `from`/`to`（默认 24h）；`resolution`(auto|raw|5m|1h)。
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
| 403 | FORBIDDEN | 越权访问节点/实例指标 |
| 404 | TARGET_NOT_FOUND | 节点/实例不存在 |
| 500 | INTERNAL_ERROR | 查询/聚合失败 |

## 一致性
- 与 `docs/ARCHITECTURE.md` 数据库模型章节（新增 4 张 metric 表）+ 通信协议章节（`Heartbeat` 负载扩展）一致——实现时同步。
- 与 ADR-013（分级降采样）、ADR-014（ServerProbe 实例指标源）、ADR-002（gRPC）一致；细化 FR-010。
- 实例指标源为 ServerProbe（`internal/worker/metrics/serverprobe.go` `ProbeSnapshot`）；不新增深度指标采集路径、不依赖已退役的 FR-103 插件桥。
