# FR-527 对外只读统计契约

> 状态：草拟 · 优先级：P1 · 关联：FR-528（API 规范）

## 1. 背景

JM 的现有 API 是给自己前端用的，没有对外契约。业务台与统计面板需要稳定的只读接口。

## 2. 边界（最重要）

> **只出通道与基础设施维度，不出任何业务指标。**

| 出 | 不出 |
|---|---|
| 实例 CPU/RSS/TPS/玩家数 | 活动参与人数 |
| 节点资源、磁盘、网络 | 经济流水、余额分布 |
| 日志检索、崩溃趋势 | 活动完成率、留存 |
| 在途变更、桥状态 | 任何需要理解 domain 语义的指标 |

**如果暴露按 `domain` 分组的计数**，必须在文档标注：

> `domain` 为不透明标签，平台不解释其语义，不保证其稳定性。

## 3. 接口定义

### 3.1 集群总览

```http
GET /api/v1/stats/overview
```

```json
{
  "nodes": { "total": 3, "online": 3, "maintenance": 0 },
  "instances": { "total": 120, "running": 118, "stopped": 2, "crashed": 0 },
  "resources": {
    "cpuCores": 120,
    "cpuUsagePct": 43.2,
    "memoryTotalMb": 262144,
    "memoryUsedMb": 98304
  },
  "probes": { "connected": 115, "disconnected": 5 }
}
```

无时间窗参数，返回**当前快照**。

### 3.2 实例指标

```http
GET /api/v1/stats/instances?instanceIds=1,2,3&from=&to=&resolution=5m&page=1&pageSize=100
```

| 参数 | 说明 |
|---|---|
| `instanceIds` | 可选，逗号分隔；缺省=全部（受权限限制） |
| `from` / `to` | RFC3339，缺省=最近 1 小时；**范围上限 30 天** |
| `resolution` | `raw` / `15s` / `1m` / `5m` / `1h`，缺省 `5m` |

返回**走 rollup 表**（`MetricRollup5m` / `MetricRollup1h`），不打 `MetricSampleRaw`。

```json
{
  "series": [
    {
      "instanceId": 1,
      "metric": "cpu_pct",
      "points": [{ "t": "2026-10-23T12:00:00Z", "v": 43.2 }]
    }
  ],
  "pagination": { "page": 1, "pageSize": 100, "total": 120 }
}
```

### 3.3 节点指标

```http
GET /api/v1/stats/nodes?nodeIds=&from=&to=&resolution=5m
```

同上结构，维度为节点。

### 3.4 日志检索

```http
GET /api/v1/stats/logs?instanceId=&nodeId=&level=&q=&from=&to=&page=1&pageSize=200
```

| 参数 | 说明 |
|---|---|
| `q` | 全文检索（可选） |
| `level` | `ERROR` / `WARN` / `INFO` / `DEBUG` |
| 时间窗上限 | 7 天 |

**返回内容不含玩家敏感信息**（日志脱敏由采集侧负责）。

### 3.5 崩溃趋势

```http
GET /api/v1/stats/crashes?instanceId=&days=30
```

```json
{
  "byDay": [{ "date": "2026-10-23", "count": 2 }],
  "byReason": [{ "reason": "OOM", "count": 5 }]
}
```

### 3.6 在途变更

```http
GET /api/v1/stats/changes?status=rolling,paused&page=1&pageSize=50
```

返回配置交付的进行中任务（不含配置内容）。

## 4. 通用约定

### 4.1 认证

全部使用 Agent Token（`Authorization: Bearer <token>`），受 scope 收敛与写白名单约束（只读工具天然在白名单外）。

### 4.2 分页

| 参数 | 默认 | 上限 |
|---|---|---|
| `page` | 1 | — |
| `pageSize` | 100 | **200** |

超过上限返回 `400 PAGE_SIZE_EXCEEDED`，不静默截断。

### 4.3 时间窗

| 接口 | 缺省 | 上限 |
|---|---|---|
| instances / nodes | 1 小时 | 30 天 |
| logs | 1 小时 | 7 天 |
| crashes | — | 365 天 |

超上限返回 `400 TIME_RANGE_EXCEEDED`，并在响应里给出 `maxRange`。

### 4.4 限流

| 维度 | 默认 |
|---|---|
| 每 token 每分钟 | 120 次 |
| 单次查询最大扫描行数 | 100 万（超出拒绝） |

超限返回 `429 RATE_LIMITED` + `Retry-After`。

### 4.5 响应头

```
X-Total-Count: 120
X-Page: 1
X-Page-Size: 100
X-RateLimit-Remaining: 88
```

## 5. 性能要求

| 场景 | 目标 |
|---|---|
| overview（100+ 实例） | P95 < 100ms |
| instances 查 100 实例 × 1 天 × 5m | P95 < 500ms |
| logs 查 1 小时 | P95 < 800ms |

**必须走 rollup 表**：raw 表只用于 `resolution=raw` 且时间窗 < 1 小时。

## 6. 验收

1. 六组接口全部可经 Agent Token 调用
2. 超出分页上限返回明确错误
3. 超出时间窗返回 `maxRange`
4. `resolution=5m` 时**不查 raw 表**（用 SQL 日志验证）
5. 限流生效
6. 契约写入 `docs/API.md` 并有契约测试守门
7. **无任何业务语义字段**（code review 检查）
8. 按 `domain` 分组时文档有免责声明

## 7. 待定

1. 是否需要「导出 CSV」能力（当前：不做，由调用方自己转）
2. `overview` 是否需要按分组维度拆分（当前：只有全局）
3. 是否支持 WebSocket 推送实时指标（当前：不做，用轮询）

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-527 对外只读统计契约（feat，需 spec，spec

**需求补充**：`docs/specs/external-stats-contract/spec.md`）：把 JM 的统计数据打包为稳定外部接口（Agent Token 鉴权、写入 `docs/API.md`）：`/stats/overview`（集群总览摘要）、`/stats/instances`（实例指标，走 `MetricRollup*`）、`/stats/nodes`（节点指标）、`/stats/logs`（日志检索）、`/stats/crashes`（崩溃趋势）、`/stats/changes`（在途变更状态）。**统计只覆盖通道与基础设施维度**（成功率、延迟、在途、积压、丢弃），**不产出任何业务指标**（如活动参与数、完成率——那是业务台的职责）；若暴露按 `domain` 分组的计数，须在文档标注「domain 为不透明标签，平台不解释其语义」。统一约定：RFC3339 时间窗、`page/pageSize`（≤200）、`resolution`（raw/15s/1m/5m/1h）、限流与分页上限。验收：六组接口全部可经 Agent Token 调用；查询走 rollup 表不打原始表；超时间窗/超分页被明确拒绝；契约入 API.md 并有契约测试守门；无任何业务语义字段。

**交付状态**：✅ 已交付@v0.24.0

### FR-527

只读统计契约

✅ 代码完成+真机验收（2026-10-24）：`service/stats.go` + `router/stats.go`，六组接口。`resolution=5m/1h` 走 rollup 表（`TestStats_Resolution5mNeverTouchesRaw` 用 SQL 日志守住），每 token 120 次/分钟限流。**真机**：overview 返回真实数据、`pageSize>200` 返回 `PAGE_SIZE_EXCEEDED` + `maxPageSize:200`。

