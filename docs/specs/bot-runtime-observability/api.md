# FR-401 API 契约

## GET /api/v1/metrics/bot-runtime

- 关联 FR：FR-401
- 鉴权：JWT；`nodeId`/`instanceId` 按既有资源访问权收敛；无筛选的全平台聚合仅平台管理员
- Query：`nodeId?`、`instanceId?`、`sessionId?`（三者最多一个）、`from?`、`to?`（RFC3339）、`resolution=auto|raw|5m|1h`（默认 `auto`）
- 失败：`400 INVALID_ARGUMENT`（筛选互斥、时间/档位非法）；`403 FORBIDDEN`；`404 NOT_FOUND`

响应：

```json
{
  "resolution": "5m",
  "from": "2026-07-01T00:00:00Z",
  "to": "2026-07-27T00:00:00Z",
  "sharedRuntime": true,
  "notice": "Bot Worker 资源为共享进程观察值，不代表任一 Bot 或会话的独占资源。",
  "nodes": [{"nodeId": 1, "nodeName": "node-a", "series": [{"metricKey": "bot_worker_rss_bytes", "unit": "bytes", "points": [{"ts": "2026-07-27T12:00:00Z", "avg": 12345678, "min": 12000000, "max": 13000000}]}, {"metricKey": "bot_worker_cpu_pct", "unit": "percent", "points": [{"ts": "2026-07-27T12:00:00Z", "avg": 3.5, "min": 3.0, "max": 4.0}]}]}],
  "unavailable": []
}
```

`avg/min/max` 为 `null` 表示缺测断点。会话筛选返回该会话的 executor 节点，不代表资源独占归属。
