# FR-402 API 契约

## GET /api/v1/observability/overview

- 关联 FR：FR-402
- 鉴权：JWT，**仅平台管理员**
- Query：无
- 失败：`403 FORBIDDEN`

响应（所有列表最大 5 条）：

```json
{
  "sampledAt": "2026-07-27T12:00:00Z",
  "health": {"nodeCount": 3, "onlineNodeCount": 2, "staleNodeCount": 1, "runningInstanceCount": 5, "crashedInstanceCount": 1},
  "resources": {"cpuPct": 42.5, "loadPct": 35.0, "memoryUsedBytes": 2147483648, "memoryTotalBytes": 4294967296, "freshness": "fresh"},
  "bots": {"sharedRuntime": true, "nodeCount": 2, "botWorkerRssBytes": 12345678, "botWorkerCpuPct": 3.5, "workerProcessRssBytes": 23456789, "workerProcessCpuPct": 1.5, "activeCount": 20, "connectingCount": 1, "eventLoopP95Ms": 4.2, "unavailable": []},
  "alerts": [{"id": 1, "severity": "warning", "title": "节点资源偏高", "createdAt": "2026-07-27T12:00:00Z"}],
  "tasks": [{"id": 2, "state": "failed", "title": "备份失败", "updatedAt": "2026-07-27T12:00:00Z"}],
  "exceptions": [{"kind": "node_stale", "nodeId": 1, "title": "node-a 心跳陈旧", "href": "/monitor?nodeId=1"}]
}
```

响应只含受管资源与链接所需最小标识；不可用数值为 `null`，不以零替代。
