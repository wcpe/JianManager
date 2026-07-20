# API：FR-354 Bot 恢复与归真

> 权威请求/响应和错误：`../bot-load-platform/api.md`

## HTTP 所有权

- `POST /api/v1/bots/stress-sessions/:id/retry-failed`
  - 权限：`bot:manage`
  - 状态：仅 running/degraded
  - 请求：`{requestId:string;botUuids?:string[];errorCodes?:string[];fromStepId?:string}`；requestId 为 UUID 幂等键，botUuids/errorCodes 均省略表示全部失败 Bot
  - 响应：202 `BotLoadRetryResult`（requested/accepted/skipped/errors）

其余恢复为内部 desired-state/reconcile，不新增浏览器 endpoint。

## 内部恢复契约

- `command_schedule` checkpoint 的稳定唯一键为 `runUuid + botUuid + stepId + commandId + occurrence`，并持久化最近 generation/scheduleRunId/actionRunId、nullable plannedAt/sentAt、发送状态与终态结果；prepared 未 release 即取消时两者为 null。
- 恢复时默认跳过已成功执行项；`restart_step` 必须生成新的 scheduleRunId 和 occurrence 级 actionRunId，拒绝旧 actionRunId 结果归并，但不得改变稳定 checkpoint 身份。
- stop/cancel 完成 intent 持久化后，未发送命令作废，重连、reconcile 与迟到事件均不得触发新命令。
- 契约不要求 Probe、塔防或其他特定游戏适配器。
