# API：FR-353 探针压测事件

> 权威公共类型和历史事件响应：`../bot-load-platform/api.md`

本 FR 不新增写 HTTP endpoint。事件复用 PluginEvent：

- domain=`bot_load`
- dedup_key=eventId
- request_id=correlationToken
- timestamp=occurredAtUnixMs
- raw_json=超级规格信封

读取由共享 `GET /api/v1/bots/stress-sessions/:id/events` 提供，权限 `bot:read`。

FR-353 另接真 ServerProbe MSPT p95 additive 指标字段；老探针缺失返回 null。
