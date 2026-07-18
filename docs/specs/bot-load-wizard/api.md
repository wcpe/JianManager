# API：FR-356 压测模板与创建向导前端

> 权威完整定义：`../bot-load-platform/api.md`

本 FR 不新增后端 endpoint，只消费：

- load-nodes
- load-templates CRUD
- template runs/create stress session
- preflight
- start

前端不得新增未进入共享 API 的校验或预览 endpoint。
