# API：通用Bot命令编排与调度扩展

> 目录名 `bot-load-probe-events` 为历史沿用，本规格已按 ADR-075 将 FR-353 重命名为“通用Bot命令编排与调度扩展”。

## 1. command_schedule

本 FR 提供命令计划数据结构，不新增业务事件接口，也不依赖 ServerProbe、塔防或 `ProbeEvent` 表。

```json
{
  "commands": [
    {
      "atMs": 0,
      "command": "/say ready",
      "repeat": { "intervalMs": 1000, "count": 3 }
    }
  ],
  "durationMs": 5000,
  "jitterMs": 20
}
```

- `commands`：按相对时间排列的命令列表。
- `atMs`：相对计划起点的毫秒数。
- `repeat.intervalMs` / `repeat.count`：重复间隔和次数；重复不得超出 duration。
- `durationMs`：计划有效持续时间。
- `jitterMs`：允许的执行时间随机偏移，不得破坏顺序或屏障窗口。

实现必须校验非负有限时间、非空命令、重复参数、duration 边界及系统上限，并拒绝未知字段。

## 2. 调度与执行语义

由集中 scheduler 管理每个 actionRunId 的计划、取消、重试和结果聚合。调用 `send_command` 时执行 `bot.chat(command)`：未抛异常即返回并记录 `sent`，抛异常则按有限重试策略处理，最终记录 `failed`。不等待 Minecraft 服务端回执，不从聊天、日志或业务事件推断结果。

取消操作幂等。取消后未发送命令不得执行；已产生的结果保留并将未完成项聚合为 `cancelled`。计划结束、取消、失败或 scheduler 关闭时必须释放定时器和计划状态，禁止 timer 泄漏。

聚合结果按命令序号稳定返回，至少包含：总命令数、`sent`、`failed`、`cancelled`、重试次数、开始时间、结束时间及每项错误摘要。

## 3. 模板变量

命令模板只允许以下变量：

`botName`、`botOrdinal`、`cohortKey`、`runId`、`actionRunId`、`correlationToken`

`roomKey`、`areaId` 以及任何其他业务变量均不允许出现；未知变量必须在计划提交时拒绝，不能通过嵌套字段或动态表达式绕过白名单。

## 4. 验收接口边界

自动化测试必须覆盖 500 个 Bot 命令顺序、屏障窗口、调度延迟 p95、取消和无 timer 泄漏。真机测试只验证普通 Minecraft 命令发送及 `bot.chat` 异常语义，不验证房间、区域、塔防或其他业务效果。
