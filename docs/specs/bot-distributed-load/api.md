# API：FR-351 Bot 分布式调度

> 权威公共类型、请求/响应和错误：`../bot-load-platform/api.md`

## HTTP 所有权

- `GET /api/v1/bots/load-nodes`：`bot:read`，目标实例隔离。
- `POST /api/v1/bots/stress-sessions/:id/preflight`：`bot:manage`。
- `POST /api/v1/bots/stress-sessions/:id/start`：扩展为 planToken + 202。
- `POST /api/v1/bots/stress-sessions/:id/stop`：批次停止兼容入口。

## gRPC 所有权

- GetBotCapacity
- ApplyBotBatch
- GetBotFleetSnapshot
- StreamBotFleetEvents
- SignalBotActions 通用 request/result 投递层

本 FR 一次铺齐共享字段；具体结构严格使用超级规格 §9，禁止子分支另行改名。
