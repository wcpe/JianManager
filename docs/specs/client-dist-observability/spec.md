# 客户端分发观测数据底座 — Spec（FR-217）

> **FR-217 产出**。把客户端分发遥测（FR-093 拉取/下载明细 `client_dist_event` + FR-094 更新结果 `client_telemetry`）**定时聚合为按 频道 × 小时桶的时序快照**，供观测·客户端分发监控页（FR-218）与频道工作台统计 Tab 扩维（FR-219）消费。
> 关联 **ADR-049**（聚合决策，复用 ADR-013 分级降采样思路、精简为单档小时桶）、ADR-022/023（分发架构与端点防护）、ADR-013（节点/实例时序范式）。
> 状态：v1（2026-06-28，随实现回写）。

## 0. 约定

- 聚合与查询全部落 **Control Plane**（架构不变量：Worker 不直连 DB；分发明细本就由 CP 持有）。
- 查询端点前缀 `/api/v1`，JWT 鉴权（运营者浏览器入口），**仅平台管理员**（同 `/client-dist/stats`、`/users`）。
- 查询为只读，但记审计（FR-015）——观测数据含 IP/机器码维度，访问留痕。
- 错误响应统一 `{ "error": CODE, "message": "..." }`（见 `docs/API.md` 错误码表）。
- 时间一律 UTC；桶对齐到**整小时**起点（`Truncate(1h)`）。

## 1. 与既有遥测的关系（不重复造）

| 已有 | 角色 | 与本 FR 关系 |
|---|---|---|
| `client_dist_event`（FR-093） | 拉取/下载明细，短保留 14d | **聚合源**之一；查询端点对保留窗内区间回查它做精确机器码去重 |
| `client_telemetry`（FR-094） | 更新结果明细，短保留 | **聚合源**之一 |
| `client_dist_daily` / `client_telemetry_daily`（FR-093/094） | 写时增量、按日聚合 | **不动**；服务热路径与 FR-095 单频道看板 |
| `ClientDistStats.Overview`（FR-095） | 单频道近 N 天 ad-hoc GROUP BY | **并存不替代**；服务频道工作台「统计」Tab 按日看板 |
| `client_machine`（FR-092） | 机器码登记 | 机器码去重的语义来源（不可信、仅统计） |

本 FR **新增**：离线卷积出的小时级时序快照表 `client_dist_snapshot` + 聚合任务 + 跨频道/平台时序查询端点。与写时聚合解耦——玩家热路径零新增负担。

## 2. 数据模型（落库）

### ClientDistSnapshot（client_dist_snapshots）

每 `频道 × 小时桶` 一行。`channel_id` 可为空串（制品端点跨频道共享、明细 channel 可空 → 归入空频道桶；查询「总」时含之）。

| 字段 | 类型 | 说明 |
|---|---|---|
| id | uint PK | 自增主键 |
| channel_id | varchar(64) not null default '' | 频道 slug；与 bucket_ts 组成唯一键。空串=跨频道/制品共享桶 |
| bucket_ts | datetime not null | 小时桶起点（UTC，整小时对齐）。与 channel_id 唯一 |
| manifest_pulls | bigint default 0 | 桶内 manifest 拉取次数（`kind=manifest`） |
| artifact_pulls | bigint default 0 | 桶内制品拉取次数（`kind=artifact`） |
| download_bytes | bigint default 0 | 桶内总响应字节（明细 bytes 求和） |
| cas_hit | bigint default 0 | 制品 CAS 命中数（artifact 且 status=304） |
| cas_miss | bigint default 0 | 制品 CAS 未命中数（artifact 且 status∈{200,206}） |
| active_machines | bigint default 0 | **桶内** machineId 精确去重计数（`COUNT(DISTINCT machine_id)`，排空串）。跨桶不可简单求和，见 §4 去重口径 |
| version_dist | text | 版本分布 JSON `map[version]count`（manifest 拉取按 version 计数） |
| platform_dist | text | 平台分布 JSON `map[os]count`（来源遥测 os 字段） |
| update_total | bigint default 0 | 桶内更新遥测总条数 |
| update_success | bigint default 0 | result=success 数 |
| update_fail_static | bigint default 0 | result=fail-static 数（断网兜底启动） |
| update_rolled_back | bigint default 0 | result=rolled-back 数 |
| update_error | bigint default 0 | result=error 数 |
| lag_dist | text | 版本滞后分布 JSON `map[lag]count`（`current_version - toVersion`，下界 0；遥测 toVersion>0 时计） |
| created_at | datetime | 首次卷积时间 |
| updated_at | datetime | 末次重算时间（幂等 upsert 覆盖） |

索引：`uniqueIndex(channel_id, bucket_ts)`；`index(bucket_ts)`（按区间扫描 + TTL 清理）。

留存：快照表自身 ≥180d（`snapshotRetention`），后台任务按 TTL 清理；明细到期由 FR-093/094 各自滚动清理，不影响已卷积快照。

## 3. 聚合任务

- **服务**：`ClientDistObservabilityService`（`internal/controlplane/service/client_dist_observability.go`）。
- **周期**：`Start()` 起后台 ticker，默认每 **10min** 卷一次（`aggregateEvery`），复用 scheduler 式 goroutine（同 `MetricService`/`ClientDistTrackingService`）。
- **卷积口径**（`AggregateAndPurge(now)`，幂等可重跑）：
  1. 只卷**已完结**的小时桶：`bucket_ts < Truncate(now, 1h)`。为避免每次全量重扫，按「上次水位 `lastAggregatedBucket`」往后扫到完结边界（首次启动从最早明细桶或近 `backfillWindow` 起；持久化水位非必须，重扫幂等 upsert 也安全——MVP 取**重算近 `reaggregateWindow`=48h 完结桶**，保证延迟到达的明细被纳入，且开销有界）。
  2. 对每个 `(channel_id, hour)`：
     - 从 `client_dist_event` 卷出 `manifest_pulls/artifact_pulls/download_bytes/cas_hit/cas_miss/active_machines/version_dist/platform_dist`（平台分布 event 无 os → 平台分布主由遥测侧补，event 侧可为空）。
     - 从 `client_telemetry` 卷出 `update_*` 计数、`platform_dist`（os）、`lag_dist`。
     - 按 `(channel_id, bucket_ts)` 唯一键 **upsert**（OnConflict 覆盖全部聚合列 + 刷新 updated_at）。
  3. **TTL 清理**：删 `bucket_ts < now - snapshotRetention` 的快照行。
- **缺数语义**：某桶无任何明细 → 不产出该桶行（查询返回时该小时缺点，前端断点渲染，不补 0）。

## 4. machineId 去重口径（ADR-049 决策 4）

- **桶内 `active_machines`** = 该小时 `client_dist_event` 的 machineId **精确去重计数**（排空串）。machineId 不可信（ADR-023），仅统计近似。
- **跨区间「活跃客户端独立数」≠ 各桶求和**（同一客户端跨小时重复 → 求和是「人次」上界）。查询端点：
  - 区间**完全落在明细保留窗（14d）内** → 实时回查 `client_dist_event` 做区间级 `COUNT(DISTINCT machine_id)`，返回**精确独立数**，`activeMachinesExact=true`。
  - 区间**超出保留窗**（含部分超出）→ 返回各桶 `active_machines` **求和**（标注「活跃人次」近似上界），`activeMachinesExact=false`。
- 不存 machineId 集合（膨胀），不在 UI 谎报精确独立数。

## 5. 查询端点

### GET /client-dist/observability

跨频道/单频道的分发观测时序 + 区间分布聚合 + 汇总标量。

**权限**：平台管理员（`requirePlatformAdmin`）。记审计 `client_dist_observability.query`。

**Query 参数**：
| 参数 | 必填 | 说明 |
|---|---|---|
| channelId | 否 | 不传=**总**（跨频道合并所有桶，含空频道桶）；传=仅该频道 |
| from / to | 否 | RFC3339；同时给则用之（`to` 必须晚于 `from`） |
| range | 否 | 无 from/to 时用枚举回退：`24h`/`7d`/`30d`/`90d`/`180d`，默认 `7d` |

**响应 200**：
```json
{
  "channelId": "myserver",
  "from": "2026-06-21T00:00:00Z",
  "to": "2026-06-28T00:00:00Z",
  "series": [
    {
      "ts": "2026-06-27T10:00:00Z",
      "manifestPulls": 120, "artifactPulls": 35, "downloadBytes": 8123456,
      "casHit": 20, "casMiss": 15, "activeMachines": 48,
      "updateTotal": 30, "updateSuccess": 27, "updateFailStatic": 1,
      "updateRolledBack": 1, "updateError": 1
    }
  ],
  "summary": {
    "manifestPulls": 1500, "artifactPulls": 400, "downloadBytes": 99000000,
    "casHit": 240, "casMiss": 160,
    "updateTotal": 360, "updateSuccess": 330, "updateFailStatic": 10,
    "updateRolledBack": 12, "updateError": 8,
    "successRate": 0.9167, "failStaticRate": 0.0278, "rollbackRate": 0.0333,
    "casHitRate": 0.60,
    "activeMachines": 512, "activeMachinesExact": true
  },
  "versionDist": [ { "version": 7, "count": 900 }, { "version": 6, "count": 600 } ],
  "platformDist": [ { "os": "windows", "count": 1200 }, { "os": "linux", "count": 300 } ],
  "lagDist": [ { "lag": 0, "count": 320 }, { "lag": 1, "count": 30 } ]
}
```

- `series`：按 `ts` 升序的小时桶时序（跨频道时同小时跨频道桶合并求和；缺数小时无点）。
- `summary`：区间内跨桶汇总标量 + 派生率（分母为 0 时率为 0）。`activeMachines`/`activeMachinesExact` 见 §4。
- `versionDist`/`platformDist`/`lagDist`：区间内跨桶 JSON map 合并求和后，按 count 降序的数组。

**错误**：
| 场景 | HTTP | error |
|---|---|---|
| 非平台管理员 | 403 | FORBIDDEN |
| from/to 非法（解析失败或 to≤from） | 400 | INVALID_RANGE |
| range 非枚举值 | 400 | INVALID_RANGE |
| 内部错误 | 500 | INTERNAL_ERROR |

> 不存在的 channelId 不报 404：观测查询对未知频道返回空时序 + 零汇总（与 FR-095 一致，避免泄露频道存在性、且便于前端统一渲染空态）。

## 6. 装配

- `Services` 加 `ClientDistObservability *service.ClientDistObservabilityService`。
- `main.go` 构造 + `Start()` + `defer Stop()`（同 `clientDistTrackingSvc`）。
- 路由：`ClientDistObservabilityHandler` 挂 **admin（JWT 平台管理员）组**，紧邻 `clientStatsHandler`。
- AutoMigrate 接入 `&model.ClientDistSnapshot{}`。

## 7. 测试

- **服务单测**（`client_dist_observability_test.go`）：
  - 卷积口径：拉取/制品计数、字节求和、CAS 命中/未命中分流、桶内 machineId 去重、版本/平台/滞后 JSON 分布、更新 result 分流。
  - 幂等：同一窗重跑两次结果一致（upsert 覆盖不翻倍）。
  - TTL 清理：超 `snapshotRetention` 的桶被删。
  - 去重口径：区间在保留窗内 → 精确独立数 + `activeMachinesExact=true`；跨保留窗 → 人次求和 + `false`。
- **端点测试**（`client_dist_observability_test.go` in router）：
  - happy path：总 + 单频道 + 时间范围筛选，断言 series/summary/分布。
  - RBAC：非管理员 403。
  - 参数：非法 range / from≤to 400；未知频道空集 200。

## 8. Gate-API 自检

- [x] 端点已定义（路径/方法/请求/响应/错误码）：`GET /client-dist/observability`，§5。
- [x] error code 已定义：FORBIDDEN/INVALID_RANGE/INTERNAL_ERROR（复用 `docs/API.md` 错误码表）。
- [x] 权限要求已标注：平台管理员 + 审计。
- [x] 与 ARCHITECTURE 通信协议一致：CP HTTP 端点（运营者 JWT 入口），无新 gRPC/WS。
- [x] 与数据库模型一致：§2 字段名/类型与 model struct 对齐。
- [x] 响应 JSON 可直接生成 TS 类型：§5 扁平结构。
- [x] 标注 FR：FR-217（消费方 FR-218/219）。
