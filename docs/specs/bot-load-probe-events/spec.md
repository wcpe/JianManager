# 功能规格：通用Bot命令编排与调度扩展

> 目录名 `bot-load-probe-events` 为历史沿用；本规格承接 FR-369，并按 ADR-075 取代未实施的旧 FR-364。
> 状态：已审核　·　关联 PRD：FR-369（取代 FR-364，增强 FR-363/365）

## 1. 背景与目标

为压力测试和自动化运行提供通用 Bot 命令编排能力。调用方提交一组相对时间命令，系统集中调度、取消、重试并聚合结果；能力只证明普通 Minecraft 命令的发送，不解释或断言任何业务事件及业务效果。

本 FR 不依赖 ServerProbe、塔防插件、业务事件或 `ProbeEvent` 表，不新增进程边界；在既有 Control Plane ↔ Worker gRPC 上加性增加独立批量计划/时间释放/取消 RPC，并在既有 Worker ↔ Bot Worker stdin/stdout JSON IPC 上增加命令计划准备、绝对时间释放、取消和结果消息。

## 2. 需求

- FR-369 定义“通用 Bot 命令编排与调度扩展”，取代旧 FR-364 的 ServerProbe 断言桥交付范围。
- 支持 `command_schedule`：相对时间命令列表，以及 `repeat interval/count`、`duration`、`jitter`。
- 由集中式 scheduler 负责排队、执行、取消、有限重试和结果聚合，避免每个命令各自创建不可管理的定时器。
- `send_command` 调用 `bot.chat` 且未抛出异常即视为发送成功；不等待服务器回执，不验证业务效果。
- 模板变量白名单仅为：`botName`、`botOrdinal`、`cohortKey`、`runId`、`actionRunId`、`correlationToken`。
- 禁止 `roomKey`、`areaId` 及其他业务变量进入命令模板或调度上下文。

**范围内**：命令计划校验、时间计算、集中调度、取消、重试、结果聚合、Bot IPC 适配、自动化测试及文档。

**不做**：ServerProbe 或塔防适配、业务事件接收与断言、房间/区域/波次/伤害/击杀效果验证、`ProbeEvent` 持久化、普通 Minecraft 命令之外的真机业务验收。

## 3. 设计

### 3.1 command_schedule

计划由 1..100 个相对时间命令组成，展开后 occurrence 总数最多 1000，规范 JSON 最大 256KiB。每项包含计划内唯一稳定 `id`、命令文本和相对执行时间；可选重复配置：`interval`、`count`，其中 `count` 是包含首次执行在内的总 occurrence 数。`duration` 限制计划有效窗口，`jitter` 为每次执行增加受约束的随机时间偏移。若任一 occurrence 的基础时间 `atMs + occurrence*intervalMs` 超出 duration，整份计划在提交时拒绝，不做运行时静默截断。

```json
{
  "commands": [
    { "id": "announce-ready", "atMs": 0, "command": "/say ready" },
    { "id": "list-players", "atMs": 500, "command": "/list", "repeat": { "intervalMs": 1000, "count": 3 } }
  ],
  "durationMs": 4000,
  "jitterMs": 25
}
```

所有时间必须为非负有限整数；展开全部 occurrence 后按 `(基础时间, commandDeclarationIndex, occurrence)` 稳定执行，再计算 jitter 和单调钳制。计划校验拒绝未知字段、空命令、超出 duration 的项目、非法重复参数，以及超过 commands/occurrence/JSON/time 明确上限的计划。

### 3.2 集中 scheduler

每个 `scheduleRunId` 由 Bot Worker 内的一个本地 scheduler 统一管理其待执行项和生命周期；每个 `commandId + occurrence` 通过 UUIDv5(`scheduleRunId`, UTF-8 `botUuid + NUL + stepId + NUL + commandId + NUL + occurrence`) 得到独立确定性 `actionRunId`。scheduler 维护计划状态、下一个截止时间和取消信号；不得为每次重复执行永久保留独立 timer。执行完成、取消、超时或失败后释放计划资源。

取消是幂等的：取消后未开始的 occurrence 不再执行并逐项记为 cancelled；已经进入同步 `bot.chat` 调用的当前 occurrence 按实际结果记 sent 或 failed，不得被取消覆盖，完成后不再启动后续项。调度器关闭时必须取消并清理全部计划。

### 3.3 发送、重试与结果

调用 `bot.chat(command)`：调用未同步抛错即记录 `sent`；同步抛错按 V1 固定策略最多尝试 3 次，失败后退避 250ms、500ms。调用前路由、模板/参数错误、Bot 不存在、取消和 deadline/duration 到期不可重试；下一次尝试无法在截止前开始时立即以 `COMMAND_DEADLINE_EXCEEDED` 收敛为 timedOut/timed_out，先前发送错误仅保留在 attemptErrors。不根据聊天回显、服务器日志、房间状态或其他业务信号改变结果。

每个执行项只产生一次最终 IPC/Fleet 终态；先前失败尝试只进入最多 2 条 `attemptErrors`。聚合结果按命令声明顺序和 occurrence 稳定输出，包含总数、sent、failed、timedOut、cancelled、重试次数及开始/结束时间；deadline error 计 timedOut，不得同时计 failed。checkpoint 稳定键为 `runUuid + botUuid + stepId + commandId + occurrence`，generation/scheduleRunId/actionRunId/attempt 仅记录最近执行；重试不得改变当前 actionRunId，也不得突破取消、duration 或运行 deadline。

### 3.4 变量与隔离

模板仅允许替换以下变量：`botName`、`botOrdinal`、`cohortKey`、`runId`、`actionRunId`、`correlationToken`。`runId` 固定为数据库数字 ID 的十进制文本，运行 UUID 通过独立 `runUuid` 字段传递。提交时校验变量名及模板原文 1..1024 UTF-8 bytes/无控制字符；botName/botOrdinal/cohortKey/runId 在 CP 冻结 Bot 计划时展开，计划级 `correlationToken` 随同一 schedule 原样复用，occurrence 级唯一 `actionRunId` 按确定性公式展开并写入命令。CP 在 Apply 前把每个 occurrence 的最终 command、declarationIndex/baseAt/jitterOffset/actionRunId 组成有序 occurrence plan，并对最终文本再次执行同一 1..1024 bytes 与无 CR/LF/NUL/控制字符校验；Worker/Bot Worker 不再展开模板变量，只做防御复验。违规 Bot 计划不进入 scheduler，统一以 `COMMAND_ARGUMENT_INVALID` 收敛。`roomKey`、`areaId` 和任何业务事件字段均必须被拒绝，不能通过额外字段、嵌套对象或动态表达式绕过白名单。

### 3.5 进程边界

Control Plane/Worker 仍按现有职责通信；CP 是运行快照、cancel intent 与 checkpoint 真源，Worker 不访问数据库。既有 WorkerService 加性增加 `ApplyBotCommandSchedules` / `ReleaseBotCommandSchedules` / `CancelBotCommandSchedules`，由 CP 批量下发已展开 occurrence plan、共同 releaseAt、已 sent 跳过集或未终态取消集；同步逐项结果为 accepted/rejected/unknown，最终 occurrence 终态仍经 Fleet `action_event`。Bot Worker 仅负责接收已调度的发送请求并执行 `bot.chat`；既有 stdin/stdout JSON IPC 加性增加 `cmd:"command-schedule"`、`evt:"command-schedule-accepted"`、`cmd:"command-schedule-release"`、`evt:"command-schedule-release-result"`、`evt:"command-schedule-result"`、`cmd:"command-schedule-cancel"` 和 `evt:"command-schedule-cancel-result"`。请求贯穿批次 requestId、派生 IPC requestId、runId/runUuid/botUuid/generation/stepId/scheduleRunId/correlationToken/start mode、scheduleStartAt 或 barrierKey/releaseAt、runDeadlineUnixMs/十进制字符串 jitterSeed/skipOccurrences；每个 commandId+occurrence 产生独立 actionRunId，结果不再依赖 requestId，Worker 再映射到既有 Fleet `action_event`。完整字段、错误码、前置失败合成、reconcile payload 和幂等规则以同目录 `api.md` 为准。本 FR 不引入 ServerProbe、塔防插件、业务事件通道或 `ProbeEvent` 数据表。

## 4. 任务拆分

- [ ] 测试先行：计划校验、重复/持续时间/jitter 的边界和顺序。
- [ ] 实现集中 scheduler、取消、有限重试和结果聚合。
- [ ] 实现 CP↔Worker `ApplyBotCommandSchedules`/`ReleaseBotCommandSchedules`/`CancelBotCommandSchedules`、带稳定派生 requestId 的 command-schedule/release/cancel IPC，以及 Fleet action-event 结果映射。
- [ ] 实现 Bot `chat` 适配及异常即失败、无异常即成功语义。
- [ ] 实现变量白名单和业务变量拒绝。
- [ ] 增加 500 Bot 命令、屏障窗口、延迟 p95、取消和 timer 清理自动化测试。
- [ ] 真机仅验证普通 Minecraft 命令发送，不验证业务效果。

## 5. 验收标准

### 自动化

- [ ] 500 个 Bot 的命令按计划顺序执行，屏障窗口内的命令不越过屏障。
- [ ] 统计调度延迟并验证 p95 满足既定阈值；jitter、重复和 duration 语义稳定。
- [ ] 取消后未发送命令不执行，取消结果可聚合且操作幂等。
- [ ] IPC 写入或 accepted 不得提前记 sent；requestId 只匹配同步 accepted，异步 result 仅按 runUuid/botUuid/generation/scheduleRunId/actionRunId/stepId/commandId/occurrence 更新终态。
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
- 计划大小按共享 API 的 1..100 命令限制；单执行项固定最多 3 次尝试、退避 250ms/500ms。全局并发沿用运行协调器的有界队列与节点限流，禁止无界调度。
