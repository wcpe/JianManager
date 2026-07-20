# API：FR-354 Bot 恢复与归真

> 权威请求/响应和错误：`../bot-load-platform/api.md`

## HTTP 所有权

- `POST /api/v1/bots/stress-sessions/:id/retry-failed`
  - 权限：`bot:manage`
  - 状态：仅 running/degraded
  - 目标：botIds 或 errorCodes；空表示全部失败 Bot
  - 响应：requested/accepted/skipped/errors

其余恢复为内部 desired-state/reconcile，不新增浏览器 endpoint。

## 内部恢复契约

- `command_schedule` checkpoint 持久化命令计划、发送状态、终态结果与 actionRunId。
- 恢复时默认跳过已成功命令；`restart_step` 必须生成新的 actionRunId 并拒绝旧结果归并。
- stop/cancel 完成 intent 持久化后，未发送命令作废，重连、reconcile 与迟到事件均不得触发新命令。
- 契约不要求 Probe、塔防或其他特定游戏适配器。
