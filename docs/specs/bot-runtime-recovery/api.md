# API：FR-354 Bot 恢复与归真

> 权威请求/响应和错误：`../bot-load-platform/api.md`

## HTTP 所有权

- `POST /api/v1/bots/stress-sessions/:id/retry-failed`
  - 权限：`bot:manage`
  - 状态：仅 running/degraded
  - 目标：botIds 或 errorCodes；空表示全部失败 Bot
  - 响应：requested/accepted/skipped/errors

其余恢复为内部 desired-state/reconcile，不新增浏览器 endpoint。
