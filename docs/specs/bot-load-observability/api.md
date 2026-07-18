# API：FR-357 压测观测前端

> 权威完整定义：`../bot-load-platform/api.md`

本 FR 不新增后端语义，只消费：

- run detail
- metrics
- run bots
- failures
- events 历史
- retry-failed
- report JSON/CSV
- 会话级 SSE stream

前端正式 verdict 只展示后端结果，不在浏览器重算。
