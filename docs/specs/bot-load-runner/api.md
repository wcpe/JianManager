# API：FR-355 压测运行与判定

> 权威完整定义：`../bot-load-platform/api.md`

## 模板

- GET/POST `/api/v1/bots/load-templates`
- GET/PUT/DELETE `/api/v1/bots/load-templates/:id`
- POST `/api/v1/bots/load-templates/:id/runs`

## 运行

- 扩展 GET/POST `/api/v1/bots/stress-sessions`
- GET `/api/v1/bots/stress-sessions/:id`
- POST `.../stop`、`.../cancel`

## 观测数据

- GET `.../metrics`
- GET `.../bots`
- GET `.../failures`
- GET `.../events`（历史分页）
- GET `.../report?format=json|csv`
- GET SSE `.../stream`（实时聚合）

历史与实时路径已冻结，禁止 handler 临时改名或使用内容协商复用同一路径。
