# API：FR-363 Bot 场景引擎

> 权威公共类型和错误：`../bot-load-platform/api.md`

本 FR 不新增独立浏览器 endpoint；扩展既有压测会话创建/详情中的 `scenario: BotLoadScenarioV2`，并由 preflight 返回 `BOT_LOAD_SCENARIO_INVALID` path 级错误。

内部契约：

- Scenario V2/YAML 单点解析在 Control Plane。
- ActionSignalRouter 使用 FR-362 SignalBotActions。
- action-event 经 FR-362 StreamBotFleetEvents 上行。

字段和动作类型严格使用 `../bot-load-platform/api.md` §2.2 的 `BotLoadScenarioV2` / `BotLoadScenarioAction` 判别联合；超级规格 §8 是 FR-369 command_schedule，不是 Scenario schema。
