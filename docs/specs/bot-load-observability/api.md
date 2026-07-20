# API：FR-361 压测观测前端

> 权威完整定义：`../bot-load-platform/api.md`
> 命令成功边界：`../../adr/075-bot-command-orchestration.md`

本 FR 不新增后端语义，只消费：

- run detail
- metrics
- run bots
- failures
- events 历史
- retry-failed
- report JSON/CSV
- 会话级 SSE stream

单 run SSE、历史指标、Bot/失败/事件分页和报告接口保持不变。前端展示的通用观测字段聚焦连接进度、命令计划发送成功/失败、调度 lag、屏障状态和 Worker 健康；不默认要求业务事件、TPS、MSPT 或 ServerProbe 数据。

正式 verdict 只展示后端结果，不在浏览器重算。命令发送成功仅表示 Bot Worker 调用 `bot.chat` 时未同步抛错，不证明服务器接受、权限校验通过或产生预期业务效果；报告和页面必须持续展示该免责声明。可选业务观测存在时应作为独立扩展数据展示，不改变通用命令动作结果。
