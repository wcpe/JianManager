# FR-400 API 契约

## GET /api/v1/metrics/resource-attribution

- 关联 FR：FR-400
- 鉴权：JWT，**仅平台管理员**
- Query：`sort=cpu|memory`（默认 `memory`），`limit=1..10`（默认 `5`）
- 失败：`400 INVALID_ARGUMENT`（非法 sort/limit）；`403 FORBIDDEN`

响应（加性新端点）：

```json
{
  "sampledAt": "2026-07-27T12:00:00Z",
  "freshness": "fresh",
  "nodes": [{
    "nodeId": 1,
    "name": "node-a",
    "status": "fresh",
    "observedAt": "2026-07-27T12:00:00Z",
    "cpuPct": 40.0,
    "loadPct": 35.0,
    "memoryUsedBytes": 2147483648,
    "memoryTotalBytes": 4294967296,
    "workerProcessRssBytes": 12345678,
    "workerProcessCpuPct": 2.5,
    "botWorker": {"rssBytes": null, "cpuPct": null, "activeCount": null, "connectingCount": null, "eventLoopP95Ms": null, "capacityMax": null, "capacityUnavailableReason": "未启动", "available": false, "reason": "未启动"}
  }],
  "topInstances": [{"instanceId": 2, "instanceName": "lobby", "nodeId": 1, "cpuPct": 20.0, "rssBytes": 1073741824, "sampledAt": "2026-07-27T12:00:00Z"}],
  "topProcesses": [{"instanceId": 2, "instanceName": "lobby", "nodeId": 1, "pid": 1234, "name": "java", "cpuPercent": 20.0, "rssBytes": 1073741824, "sampledAt": "2026-07-27T12:00:00Z"}]
}
```

所有不可用数值为 `null`；`botWorker.capacityMax` 仅表示共享 Bot Worker 上报的真实最大容量，未启动、未就绪、缺失或无效时为 `null` 并由 `capacityUnavailableReason` 解释；`freshness/status` 取 `fresh|stale|offline|unavailable`。响应不含未受管进程、原始命令行、密钥或 Bot 配置。
