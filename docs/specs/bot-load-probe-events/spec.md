# 功能规格：通用Bot命令编排与调度扩展

> 目录名 `bot-load-probe-events` 为历史沿用，本规格已按 ADR-075 重命名 FR-353。
> 状态：已审核　·　关联 PRD：FR-353（增强 FR-065/066）

## 1. 背景与目标

为压力测试和自动化运行提供通用 Bot 命令编排能力。调用方提交一组相对时间命令，系统集中调度、取消、重试并聚合结果；能力只证明普通 Minecraft 命令的发送，不解释或断言任何业务事件及业务效果。

本 FR 不依赖 ServerProbe、塔防插件、业务事件或 `ProbeEvent` 表，不新增进程边界和通信协议。

## 2. 需求

- 将 FR-353 命名为“通用Bot命令编排与调度扩展”。
- 支持 `command_schedule`：相对时间命令列表，以及 `repeat interval/count`、`duration`、`jitter`。
- 由集中式 scheduler 负责排队、执行、取消、有限重试和结果聚合，避免每个命令各自创建不可管理的定时器。
- `send_command` 调用 `bot.chat` 且未抛出异常即视为发送成功；不等待服务器回执，不验证业务效果。
- 模板变量白名单仅为：`botName`、`botOrdinal`、`cohortKey`、`runId`、`actionRunId`、`correlationToken`。
- 禁止 `roomKey`、`areaId` 及其他业务变量进入命令模板或调度上下文。

**范围内**：命令计划校验、时间计算、集中调度、取消、重试、结果聚合、Bot IPC 适配、自动化测试及文档。

**不做**：ServerProbe 或塔防适配、业务事件接收与断言、房间/区域/波次/伤害/击杀效果验证、`ProbeEvent` 持久化、普通 Minecraft 命令之外的真机业务验收。

## 3. 设计

### 3.1 command_schedule

计划由相对时间命令列表组成。每项至少包含命令文本和相对执行时间；可选重复配置：`interval`、`count`。`duration` 限制计划有效窗口，`jitter` 为每次执行增加受约束的随机时间偏移。重复次数和 duration 同时存在时，以先达到者为准。

```json
{
  "commands": [
    { "atMs": 0, "command": "/say ready" },
    { "atMs": 500, "command": "/list", "repeat": { "intervalMs": 1000, "count": 3 } }
  ],
  "durationMs": 4000,
  "jitterMs": 25
}
```

所有时间必须为非负有限整数；命令顺序按计划时间和列表顺序稳定执行。计划校验拒绝未知字段、空命令、超出 duration 的项目、非法重复参数和超出系统上限的计划。

### 3.2 集中 scheduler

每个 actionRunId 由一个 scheduler 统一管理其待执行项和生命周期。scheduler 维护计划状态、下一个截止时间和取消信号；不得为每次重复执行永久保留独立 timer。执行完成、取消、超时或失败后释放计划资源。

取消是幂等的：取消后未发送的命令不再执行，正在进行的 `bot.chat` 调用按 Bot 运行时语义结束，后续结果标记为 cancelled。调度器关闭时必须取消并清理全部计划。

### 3.3 发送、重试与结果

调用 `bot.chat(command)`：调用未抛出异常即记录 `sent` 成功；抛出异常则按计划的有限重试策略重试，最终记录 `failed`。不根据聊天回显、服务器日志、房间状态或其他业务信号改变结果。

每次尝试记录 actionRunId、命令序号、尝试次数、计划时间、实际发送时间、状态和错误摘要。聚合结果按命令序号稳定输出，包含总数、sent、failed、cancelled、重试次数及开始/结束时间。重试不得突破取消、duration 或全局并发和次数上限。

### 3.4 变量与隔离

模板仅允许替换以下变量：`botName`、`botOrdinal`、`cohortKey`、`runId`、`actionRunId`、`correlationToken`。变量值在计划提交时解析并按现有转义规则写入命令。`roomKey`、`areaId` 和任何业务事件字段均必须被拒绝，不能通过额外字段、嵌套对象或动态表达式绕过白名单。

### 3.5 进程边界

Control Plane/Worker 仍按现有职责通信；Bot Worker 仅负责接收已调度的发送请求并执行 `bot.chat`。本 FR 不引入 ServerProbe、塔防插件、业务事件通道或 `ProbeEvent` 数据表。

## 4. 任务拆分

- [ ] 测试先行：计划校验、重复/持续时间/jitter 的边界和顺序。
- [ ] 实现集中 scheduler、取消、有限重试和结果聚合。
- [ ] 实现 Bot `chat` 适配及异常即失败、无异常即成功语义。
- [ ] 实现变量白名单和业务变量拒绝。
- [ ] 增加 500 Bot 命令、屏障窗口、延迟 p95、取消和 timer 清理自动化测试。
- [ ] 真机仅验证普通 Minecraft 命令发送，不验证业务效果。

## 5. 验收标准

### 自动化

- [ ] 500 个 Bot 的命令按计划顺序执行，屏障窗口内的命令不越过屏障。
- [ ] 统计调度延迟并验证 p95 满足既定阈值；jitter、重复和 duration 语义稳定。
- [ ] 取消后未发送命令不执行，取消结果可聚合且操作幂等。
- [ ] `bot.chat` 未抛异常记为 sent，抛异常按策略重试并最终记为 failed。
- [ ] scheduler 运行、取消、失败和关闭后无 timer 泄漏；重复执行不会累积不可回收 timer。
- [ ] 白名单变量全部可用，`roomKey`、`areaId` 和未知业务变量均被拒绝。
- [ ] 不创建或读取 `ProbeEvent`，不依赖 ServerProbe、塔防或业务事件。

### 真机

- [ ] 使用真实 Bot 发送普通 Minecraft 命令，确认调用成功与失败记录符合 `bot.chat` 异常语义。
- [ ] 不验房间、区域、波次、伤害、击杀或其他业务效果。

## 6. 风险 / 约束

- `bot.chat` 成功只表示客户端调用未抛异常，不等同于服务器执行成功。
- jitter 只能影响计划时间，不能破坏命令顺序、屏障窗口和 duration 上限。
- 全局并发、计划大小和重试上限沿用现有运行时限制，避免压力测试自身耗尽资源。
