# API Spec — FR-334 指标批量序列接口

> 关联 FR: FR-334（增强 FR-060/FR-270）| 优先级: P1 | 关联 ADR: ADR-013（分级降采样存储，沿用）| 状态: 草拟

## 概述

多实例指标对比的批量查询端点：一次请求返回多个实例目标的历史序列，替代前端对每实例逐条 `GET /metrics/series` 的 N+1 请求。POST 承载**只读**查询（UUID 数组入 body），无副作用、幂等。

既有 `GET /metrics/series`（单目标）契约不变。

## Endpoints

### POST /api/v1/metrics/series/batch

- **描述**: 批量返回多个实例目标的历史曲线。整批共用同一查询窗口与聚合档位（`range` 枚举 + `resolution` 自动/显式选档，语义同单目标端点）；每个 targetId 对应的序列数组与单目标端点响应的 `series` 字段同构。
- **关联 FR**: FR-334（消费方：节点详情实例对比 `NodeInstanceCompare`）
- **权限**: 登录 + 逐目标实例访问收敛（等价 `CanAccessInstance`，实现走 `AccessibleInstanceIDs` 集合判定）。无权/不存在的目标**剔除**并列入 `skipped`，不整拒。
- **请求**:
  ```json
  {
    "scope": "instance",
    "targetIds": ["4f9d2c…-uuid-a", "8a1b3e…-uuid-b"],
    "metrics": ["inst_tps"],
    "range": "24h",
    "resolution": "auto"
  }
  ```
  | 字段 | 类型 | 必填 | 说明 |
  |---|---|---|---|
  | `scope` | string | 是 | v1 仅接受 `instance`（node 维度无批量场景） |
  | `targetIds` | string[] | 是 | 实例 UUID 数组；服务端去重；去重后 1~50 |
  | `metrics` | string[] | 否 | 指标键过滤（如 `inst_tps`/`inst_mspt`/`inst_heap_used`/`inst_threads`）；缺省返回目标全部序列（含 `world_*` 分世界序列） |
  | `range` | string | 否 | `1h｜6h｜24h｜7d｜30d｜90d`，默认 `24h`（与单目标端点同枚举） |
  | `resolution` | string | 否 | `auto｜raw｜5m｜1h`，默认 `auto` 按区间自动选档（FR-221 同语义） |

- **响应** (200):
  ```json
  {
    "resolution": "5m",
    "from": "2026-07-14T12:00:00Z",
    "to": "2026-07-15T12:00:00Z",
    "series": {
      "4f9d2c…-uuid-a": [
        { "metricKey": "inst_tps", "unit": "tps", "world": "",
          "points": [ { "ts": "2026-07-14T12:05:00Z", "avg": 19.8, "min": 14.2, "max": 20.0 } ] }
      ],
      "8a1b3e…-uuid-b": []
    },
    "skipped": [
      { "targetId": "c0ffee…-uuid-c", "reason": "forbidden" },
      { "targetId": "dead00…-uuid-d", "reason": "not_found" }
    ]
  }
  ```
  - `series` 键 = 请求中通过鉴权且存在的 targetId；值与 `GET /metrics/series` 响应的 `series` 数组同构（`SeriesPoint` 的 `avg/min/max` 可为 `null` 表缺测断点）。目标存在但窗口内无序列 → 空数组（区别于被剔除）。
  - `skipped[].reason`: `forbidden`（无实例访问权）| `not_found`（UUID 不存在）。
  - 全部目标被剔除仍返回 200（`series: {}` + 完整 `skipped`）。

- **错误**:
  | HTTP | error 码 | 触发 |
  |---|---|---|
  | 400 | `INVALID_REQUEST` | body 非法 / `targetIds` 缺失或去重后为空 |
  | 400 | `INVALID_SCOPE` | `scope` 非 `instance` |
  | 400 | `INVALID_RANGE` | `range` 不在枚举 |
  | 400 | `INVALID_RESOLUTION` | `resolution` 不在枚举 |
  | 403 | `FORBIDDEN` | 未认证 / 无授权上下文（整请求级） |
  | 422 | `TOO_MANY_TARGETS` | 去重后 `targetIds` > 50 |
  | 500 | `INTERNAL_ERROR` | 查询失败 |

  错误体统一 `{ "error": "<码>", "message": "<中文说明>" }`（与既有 metric 路由一致）。

## 与数据模型 / 架构一致性

- 读 `metric_series`（`WHERE instance_id IN`）+ `metric_samples_raw` / `metric_rollup_5m` / `metric_rollup_1h`（按档位，ADR-013），无 schema 变更。
- 纯 CP 内 REST 查询，无 gRPC / Worker / 前端直连变化，符合 ARCHITECTURE 通信协议约束。
- 响应类型可直接派生 TS：`MetricSeriesBatchResponse { resolution: string; from: string; to: string; series: Record<string, MetricSeries[]>; skipped: { targetId: string; reason: 'forbidden' | 'not_found' }[] }`（`MetricSeries`/`SeriesPoint` 复用 `apps/control-plane-web/src/api/metrics.ts` 既有定义）。
