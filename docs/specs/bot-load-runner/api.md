# API：FR-370 压测运行与判定

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
  - 默认指标：连接成功率、命令发送成功率、调度完成率、屏障到达率、schedule lag、Worker 健康与 crash 数。
  - TPS、MSPT、房间、伤害为可选 legacy 字段；缺失时为 null，不阻断默认 verdict。
- GET `.../bots`
- GET `.../failures`
- GET `.../events`（历史分页）
- GET `.../report?format=json|csv`
  - 报告必须包含免责声明：默认 verdict 只证明当前环境下 Bot 连接、命令发送、调度、已配置屏障与 Worker 健康达到阈值；`bot.chat` 成功不证明服务器接受、权限通过或业务效果；TPS/MSPT、ServerProbe 和业务事件仅为附加观测。
- GET SSE `.../stream`（实时聚合）

历史与实时路径已冻结，禁止 handler 临时改名或使用内容协商复用同一路径。
