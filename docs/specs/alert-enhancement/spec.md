# 功能规格：告警体系全面增强

> 状态：已交付@v0.13.0　·　关联 PRD：FR-085 / FR-011 / FR-216　·　分支：dev

## 1. 背景与目标

FR-011 已提供基础阈值告警，但只能覆盖单一指标阈值与有限通知出口。FR-085 将告警体系收口为可运营的完整闭环：规则可表达不同触发来源，事件可分级、聚合、静默、确认与归档，通知可按规则路由到多种通道，并与 FR-216 统一通知中心共享告警未读入口。

目标：

- 支持多通知通道：Webhook、邮件、钉钉、企业微信、飞书、Discord、Telegram、站内通知。
- 支持多触发类型：指标阈值、实例崩溃、节点离线、日志关键字、玩家事件、备份失败。
- 支持 `info` / `warn` / `critical` 分级、去抖聚合、静默窗口与恢复通知。
- 提供告警事件历史、已读、确认/认领与多维筛选。
- 保持 ADR-048 约束：告警作为全局运维事件进入统一通知流，不按用户复制告警站内信。

## 2. 需求（要什么）

- 提供 `/alerts` 页面，包含规则、事件、通道三类管理视图；侧栏入口可收口到通知中心，但直链和通知中心跳转必须可达。
- 所有认证用户可查看全局告警事件和进入 `/alerts`；本 FR 不收紧为平台管理员专用。若后续需要区分“查看事件”和“管理规则/通道”，另立 FR。
- 告警规则必须支持以下字段：
  - `triggerType`: `metric` / `instance_crash` / `node_offline` / `log_keyword` / `player_event` / `backup_failed`。
  - `level`: `info` / `warn` / `critical`。
  - `targetType`: `node` 或 `instance`。
  - `targetId`: 空表示该目标类型全局匹配；非空表示只匹配指定节点或实例。
  - `channelIds`: 路由到的通知通道 ID 列表；为空仍落事件库。
  - `dedupWindowSec`: 同一去抖键在窗口内重复触发只累计，不重复通知。
  - `silenceStart` / `silenceEnd`: `HH:MM` 静默窗口，支持跨午夜。
  - `notifyRecover`: 可恢复事件恢复时是否发恢复通知。
- 触发类型与目标类型的语义必须明确：
  - `metric`、`node_offline` 使用 `node` 目标。
  - `instance_crash`、`log_keyword`、`player_event`、`backup_failed` 使用 `instance` 目标。
- 告警事件必须记录触发级别、触发类型、目标、消息、计数、触发时间、最后触发时间、恢复状态、已读状态、确认人和确认时间。
- 事件列表必须支持分页和筛选：规则、恢复状态、确认状态、级别、触发类型、消息关键字、时间范围。
- 通知通道必须支持创建、编辑、删除、启停与测试发送；被规则引用的通道不得删除。
- 凭证字段必须以 `${ENV_VAR}` 引用环境变量，避免明文凭证落库。
- 在 `/alerts` 确认或全部已读后，统一通知中心未读数必须及时刷新。

### 范围内

- 后端规则校验、事件触发、分发、历史查询、确认、已读和通道 CRUD 的回归补齐。
- 前端 `/alerts` 规则/事件/通道页面的最小闭环增强：目标选择、触发类型筛选、通道校验、通知中心联动。
- mock 假后端与自动化测试覆盖筛选、分页、通道引用冲突、已读/确认联动。
- PRD、ARCHITECTURE、API、CHANGELOG 同步。

### 范围外

- 不提供每用户告警订阅偏好，不为每个用户生成告警站内信副本。
- 不新增独立“告警路由策略表”；本期由规则自身的 `triggerType` / `level` / `targetType` / `targetId` / `channelIds` 表达路由。
- 不新增短信、电话、Slack、PagerDuty 等通知通道。
- 不改变 FR-216 `/notifications/feed` 聚合模型。
- 不引入异步通知重试队列；外部通道投递失败记录日志，不阻断事件落库。

## 3. 设计（怎么做）

### 3.1 后端

- 复用现有模型：`AlertRule`、`AlertEvent`、`AlertChannel`。
- 规则创建/更新由 `AlertService` 统一校验：
  - 触发类型、级别、目标类型必须在枚举内。
  - 触发类型与目标类型必须匹配。
  - `dedupWindowSec`、`durationSec` 不得为负。
  - 静默窗口非空时必须是 `HH:MM`。
  - `log_keyword` 必须有 `keyword`。
  - `player_event` 的 `eventMatch` 允许空或 `join` / `quit` / `chat` / `cross_server`。
- `AlertEvaluator` 负责周期评估指标阈值与节点离线，并在恢复时调用 `AlertDispatcher.Resolve`。
- `AlertEventTriggers` 订阅实例状态、日志、玩家事件与备份失败回调，产生事件驱动触发。
- `AlertDispatcher` 负责：
  - 以 `rule_id + dedup_key` 查找活跃事件。
  - 去抖窗口内只更新 `count` 与 `last_fired_at`。
  - 可恢复事件在活跃期间避免重复建事件；瞬时事件超窗后可形成新历史。
  - 静默窗口内仍写事件库，但不外发外部通道。
  - 按 `channelIds` 查询启用通道并扇出投递；站内通道由事件落库承载。
  - `notifyRecover=true` 且不在静默窗口时发送恢复通知。
- `ChannelNotifier` 按通道类型发送：
  - webhook/dingtalk/wecom/feishu/discord: `config.url` 必填且为 `${ENV_VAR}`。
  - telegram: `config.token` 必填且为 `${ENV_VAR}`，`config.chatId` 必填。
  - email: `host` / `port` / `to` 必填；`password` 非空时必须为 `${ENV_VAR}`。
  - inapp: 不外发。
- REST 端点挂 `protected` 分组：未登录 401；已登录用户均可访问告警事件与告警管理接口。本 FR 只修正文档歧义，不改变路由权限。

### 3.2 前端

- `AlertsPage` 保留三 Tab：规则、事件、通道。
- 规则表单：
  - 创建模式可选择触发类型、级别、目标范围、通道、去抖、静默、恢复通知。
  - `metric` / `node_offline` 固定为节点目标；其它触发类型固定为实例目标。
  - 目标范围支持“全局”和“指定节点/实例”；指定时写入 `targetId`。
  - 编辑模式保持既有约束，不修改触发类型/目标类型，只展示目标摘要并允许更新可变字段。
- 事件页：
  - 筛选栏包含级别、触发类型、恢复状态、确认状态与关键字。
  - 行内展示级别、触发类型、目标、计数、恢复、确认、已读状态。
  - 确认或全部已读成功后同时失效告警事件、告警未读数与统一通知流查询。
- 通道表单：
  - 按类型展示必要配置项。
  - 明文凭证在前端即阻止保存，后端继续兜底校验。
  - 测试发送和删除失败必须保留页面状态并提示错误。

### 3.3 Mock 与测试数据

- mock `/alerts/*` 必须校验登录，与后端 protected 分组一致。
- mock 事件列表必须支持 `ruleId`、`resolved`、`acknowledged`、`level`、`triggerType`、`keyword`、`from`、`to`、`page`、`pageSize`，并按触发时间倒序分页。
- mock 删除通道时，若任一规则 `channelIds` 引用该通道，返回 `409 CHANNEL_IN_USE`。
- seed 数据至少包含一条 critical 事件、一条日志关键字或节点离线事件、一条被引用通道，支撑 DOM 回归。

## 4. 任务拆分

- [x] 补齐本规格并经审核通过。
- [x] 补后端规则校验、触发、通道、事件查询、确认/已读回归测试。
- [x] 按测试结果做后端最小修复，保持 API 向后兼容。
- [x] 补前端 helper、规则表单、通道表单、事件页 DOM 测试。
- [x] 实现前端目标选择、触发类型筛选、通道配置校验与统一通知联动。
- [x] 对齐 mock 告警端点筛选、分页、通道引用冲突和 seed。
- [x] 文档同步：PRD 状态、ARCHITECTURE、API、CHANGELOG。
- [x] 真机/浏览器验收：mock dev 中完成创建规则、筛选事件、确认/已读、通知中心联动；截图存于 `.tmp/fr085-alerts-rule-created.png`、`.tmp/fr085-alerts-event-filter.png`、`.tmp/fr085-alerts-acknowledged.png`。

## 5. 验收标准

- 多通道：Webhook、邮件、钉钉、企业微信、飞书、Discord、Telegram、站内通道可创建/编辑；凭证明文被拒；测试发送有成功/失败反馈；被引用通道删除返回 409。
- 多触发：指标阈值、节点离线、实例崩溃、日志关键字、玩家事件、备份失败均能产生正确 `triggerType` 的事件。
- 路由：规则可配置全局或指定节点/实例；触发后按规则 `channelIds` 路由到选中通道。
- 分级聚合：事件显示 `info` / `warn` / `critical`；去抖窗口内 `count` 累计；静默期不外发但事件入库；恢复通知遵循 `notifyRecover`。
- 确认历史：事件列表支持关键字、级别、触发类型、规则、恢复状态、确认状态、时间范围、分页；确认后记录确认人与时间并置已读；全部已读生效。
- 通知中心联动：在 `/alerts` 确认或全部已读后，统一通知铃铛未读数和通知中心列表及时刷新。
- 权限：未登录访问 `/api/v1/alerts/*` 返回 401；认证用户可访问告警事件与 `/alerts` 页面。
- 自动化验证：
  - `go test ./internal/controlplane/service -run 'Alert|Channel'`
  - `go test ./internal/controlplane/router -run Alert`
  - `npm --prefix web run test:node -- alert-helpers`
  - `npm --prefix web run test:dom -- AlertsPage RuleDialog ChannelDialog NotificationCenterPage`
- 真机/浏览器验收：mock dev 或本地 CP 中创建通道与规则，触发或使用 seed 事件，验证筛选、确认、全部已读与通知中心联动，截图存入 `.tmp/` 且不入库。

## 6. 风险 / 待定

- 告警事件当前是全局运维事件，所有认证用户可见；这与通知中心的“站内信按本人隔离”不同，文档与 UI 必须避免误导。
- 外部通道投递失败不会回滚事件落库，避免通知故障导致历史缺失；后续如需重试与投递状态表，应另立 FR。
- Email 真 SMTP 连通性依赖部署环境，本期以通道测试发送与自动化消息组装回归覆盖为主。
- `targetId` 为空表示目标类型全局匹配，前端必须明确展示“全部节点/全部实例”，避免被误解为未配置。
