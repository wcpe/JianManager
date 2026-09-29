# 功能规格：QQ 机器人告警通知通道

> 状态：开发中　·　关联 PRD：FR-494（增强 FR-085）　·　分支：feat/qqbot-notification-channel

## 1. 背景与目标

FR-085 已交付 8 种通知通道（站内 / 通用 Webhook / 邮件 / 钉钉 / 企业微信 / 飞书 / Discord / Telegram），但缺少 QQ 系出口。国内运维团队普遍以 QQ 群作为值班沟通阵地，把告警推进 QQ 群能显著缩短「告警产生 → 人看到」的链路。

本 FR 在既有通道体系内新增第 9 种通道类型 `qq`，把告警投递到 QQ 官方机器人（QQ 开放平台），由机器人转发到指定群聊或单聊。

目标：

- 新增通道类型 `qq`，复用既有通道 CRUD、启停、测试发送与规则路由能力，不新增 REST 端点。
- 走 QQ 开放平台**官方 HTTP 接口**，通过 `appId` + `appSecret` 换取 AccessToken 后**主动推送**消息。
- 保持现有投递架构：CP 纯出站，不监听端口、不接收 QQ 事件、无常驻长连接。
- 凭证（`appSecret`）沿用 `${ENV_VAR}` 引用机制，禁止明文落库。

## 2. 需求（要什么）

- 通道类型 `qq` 必须可创建、编辑、删除、启停，并被告警规则的 `channelIds` 路由。
- 通道配置必须支持以下字段：
  - `appId`：QQ 机器人 AppID，必填，明文。
  - `appSecret`：QQ 机器人密钥，必填，必须为 `${ENV_VAR}` 引用。
  - `targetType`：`group` 或 `c2c`，必填；`group` 表示群聊，`c2c` 表示单聊。
  - `targetId`：目标 `group_openid`（群）或 `user_openid`（单聊），必填，明文。
  - `baseUrl`：可选，开放平台 API 根地址；留空取默认值，用于适配沙箱或域名变更。
- 投递必须使用**主动推送**语义（不带 `msg_id`），即不依赖任何入站消息上下文。
- AccessToken 必须按 `appId` 缓存复用，并在过期前刷新；并发投递不得重复取 token。
- 投递失败必须记录日志并且不阻断告警事件落库（沿用 FR-085 既有语义）。
- 前端通道表单必须按类型展示上述字段，并在保存前拦截明文 `appSecret`。

### 范围内

- 后端：通道类型常量、`ChannelConfig` 字段、四处 switch 同步、QQ 投递实现与 token 缓存。
- 前端：通道类型下拉、表单字段与校验、API 类型、`alert-helpers` 判定函数、中英文案。
- 测试：通道配置校验、凭证 `${ENV}` 解析、投递请求形状（含 token 获取与刷新）、前端 helper 与表单 DOM 回归。
- 文档：PRD 新增 FR-494 条目与验收标准、API 通道枚举与 config 形态、ARCHITECTURE 通道类型枚举。

### 范围外

- **不实现 WebSocket 网关或 Webhook 事件接收**。本 FR 不监听 `GROUP_ADD_ROBOT`、不缓存 `msg_id`、不做被动回复。理由：CP 保持纯出站可避免常驻长连接与公网可达端点，而告警是低频单向推送，主动推送配额足以覆盖。
- **不实现 `group_openid` 自动发现**。开放平台没有「机器人已加入群列表」接口，openid 须由运维从开放平台后台或调试工具获取后手工填入；自动化获取需事件接收能力，属本 FR 范围外。
- 不实现富媒体（图片 / 文件 / Ark / Markdown 卡片）推送，本期只发纯文本（`msg_type=0`）。
- 不实现被动回复额度管理、`msg_seq` 递增与互动回调（`interaction`）处理。
- 不引入官方 Go SDK `botgo`（见 §6 风险）。
- 不修改 FR-085 已交付的 8 种通道行为。

## 3. 设计（怎么做）

### 3.1 后端

**类型与配置**

- `model.AlertChannel` 新增常量 `ChannelTypeQQ = "qq"`；`Type` 字段为 `varchar(32)`，无 DB 约束，无需迁移。
- `service.ChannelConfig` 新增字段：`appId` / `appSecret` / `targetType` / `targetId` / `baseUrl`（均带 `omitempty`）。`Config` 是 JSON blob，无迁移。
- 四处 switch 同步（`channel_notifier.go`）：
  - `credentialFields`：`qq` 返回 `[]string{cfg.AppSecret}`（仅密钥视为凭证；`appId`、`targetId` 是公开标识，按明文）。
  - `validateChannelConfig`：`qq` 校验 `appId` / `appSecret` / `targetType` / `targetId` 非空，且 `targetType ∈ {group, c2c}`；末尾统一跑 `${ENV}` 校验。`appSecret` 必须显式判空——通用凭证循环对空值放行（用于兼容无凭证通道），但 QQ 没有密钥就取不到 AccessToken，漏判会导致「创建成功、首次投递才失败」。此外 `appSecret` 在所有通道类型下都做一次**类型无关**的 `${ENV}` 兜底校验：前端把类型从 `qq` 切走时会连带提交残留字段，只按当前类型挑凭证字段拦不住明文密钥落库。
  - `resolveChannelConfig`：`qq` 解析 `AppSecret`。
  - `ChannelNotifier.Send`：`qq` 分支调用 `sendQQ`。

**Token 管理**

- `ChannelNotifier` 新增按 `appId` 的 token 缓存（`map[string]cachedToken` + `sync.Mutex`），字段与 `telegramAPIBase` 同样可在测试中注入。
- 取 token：`POST {baseUrl}/app/getAppAccessToken`，body `{"appId": "...", "clientSecret": "..."}`，头 `Content-Type: application/json`。
- 响应 `{"access_token": "...", "expires_in": 7200}`。`expires_in` 必须**同时接受数字与数字字符串**两种形态——官方参数表标注为 number，但同一页的返回示例给出的是字符串 `"7200"`；只认一种会让整个响应反序列化失败，而取不到 token 意味着该通道**全部**投递失败，且只落一条 Warn 日志。缺省/`null`/空串按 7200 秒。
- 缓存判定：刷新窗口取 `min(5 分钟, 总有效期/3)`，剩余有效期小于该窗口时触发刷新。默认 7200 秒 token 即「剩余不足 5 分钟时刷新」；短寿命 token 按总有效期的 1/3 收缩窗口，避免因窗口大于寿命而每次投递都重新取 token。
- 并发取 token 必须去重：同一 `appId` 同时只能有一次在途请求，其余调用等待结果（单飞）。
- 失败时 HTTP 状态可能为 200 而 body 含 `code` 非 0，必须同时判 HTTP 状态与 `code`。

**投递**

- 路径：`targetType=group` → `POST {baseUrl}/v2/groups/{targetId}/messages`；`c2c` → `POST {baseUrl}/v2/users/{targetId}/messages`。
- 请求头：`Authorization: QQBot {accessToken}`、`Content-Type: application/json`。
- 请求体：`{"content": "<纯文本>", "msg_type": 0}`，正文由既有 `plainText(note)` 生成，保证与其它通道文案一致。
- 响应：HTTP 非 2xx 报错；HTTP 2xx 但 body 含非 0 `code` 同样报错，错误信息带上 `code` 与 `message`，便于在通道测试发送中定位（如频控 `40034100`、内容含 URL `40054010`）。
- 默认 `baseUrl` 取 `https://api.bot.qq.com`（现行官方文档口径）。`baseUrl` **只接受 https**（回环地址 `localhost` / `127.x` / `::1` 除外，便于本地调试与测试）：取 token 与发消息都会把 `appSecret` / AccessToken 发往该地址，明文 http 等同于凭证外泄。
- 令牌失效自愈：消息端点返回 HTTP 401，或业务码 `11242`（校验 token 失败）/ `11243`（校验 token 未通过）时，丢弃该 `appId` 的令牌缓存、重新获取一次并重试投递（**仅重试一次**）。否则缓存的令牌被平台提前作废后，该通道会一直失败到刷新窗口才自愈，期间告警静默丢失。非令牌类业务失败（如频控 `40034100`）不重试。

**不变更的部分**

- `AlertChannelService` 的 CRUD、删除引用校验、`TestSend` 无需按类型改动，注册新类型后自动可用。
- `AlertDispatcher.notify` 无需改动：QQ 通道与其它外部通道同样扇出，失败仅 `slog.Warn`。
- 路由与权限不变（仍为 `alert.manage`）。

### 3.2 前端

- `CHANNEL_TYPES` 增加 `'qq'`，下拉项文案走 `alerts.channel_qq`。
- 表单按 `channelIsQQ(type)` 渲染：AppID、AppSecret（带 `${ENV_VAR}` 提示与明文拦截）、目标类型（群聊 / 单聊）、目标 openid、可选 API 地址。
- 保存前校验：`appId` / `targetType` / `targetId` 非空；`appSecret` 非空时必须匹配 `${ENV_VAR}` 形式，否则禁用保存并提示。
- `api/alerts.ts` 的 `ChannelConfig` 接口补字段；`alert-helpers.ts` 补 `ChannelType` 联合成员与 `channelIsQQ`。
- 中英文案成对补齐（`zh.json` / `en.json`），key 沿用 `channel_<type>` 与 camelCase 扁平风格。

### 3.3 Mock 与测试数据

- devmock 的 `/alerts/channels` 透传 `type`，功能上无需改动。
- 端到端验证优先用真实 CP + 通道「测试发送」，mock 仅覆盖前端表单回归。

## 4. 任务拆分

- [ ] 补齐本规格并经审核通过。
- [ ] 后端：类型常量、`ChannelConfig` 字段、四处 switch、token 缓存与 `sendQQ` 投递。
- [ ] 后端测试：配置校验表、`${ENV}` 解析、投递请求形状（token 获取、路径、鉴权头、body）、token 缓存复用、HTTP 200 + 业务错误码失败。
- [ ] 前端：类型下拉、表单字段与校验、API 类型、helper、i18n 中英。
- [ ] 前端测试：helper 单测、通道对话框 DOM 用例。
- [ ] 文档同步：PRD（FR-494 条目 + 验收标准 + 索引）、API.md、ARCHITECTURE.md。
- [ ] 运行 `go test ./internal/controlplane/service/` 与前端 `pnpm test src/pages/alerts` 并记录结果。

## 5. 验收标准

- 通道管理：`qq` 类型可创建 / 编辑 / 启停 / 删除；被规则引用的通道删除返回 409。
- 凭证约束：明文 `appSecret` 在前端被拦、后端返回 `ErrCredentialNotEnvRef`；`${ENV_VAR}` 形式通过。
- 配置校验：缺 `appId` / `targetType` / `targetId` 或 `targetType` 非法时报错；`baseUrl` 留空取默认值。
- 投递：给定合法配置可发出 `POST /v2/groups/{openid}/messages`（或 `/v2/users/{openid}/messages`），头部含 `Authorization: QQBot <token>`，body 为 `{"content": ..., "msg_type": 0}`。
  - **真机实测补充**：单聊路径**已实测送达**；群聊路径接口形态正确但**平台侧拒绝**（`40034105`，详见 §6）。故本项对 group 目标只验证「请求正确发出」，不验证「平台接受」。
- Token：同一 `appId` 在有效期内复用缓存，不重复请求；过期前触发刷新；并发投递不重复取 token。
- 令牌形态兼容：`expires_in` 以数字或数字字符串返回都能取到 token；非数字形态报明确错误而非静默失败。
- 传输安全：`baseUrl` 为非回环 `http://` 时配置即被拒；`https` 与回环地址放行。
- 令牌失效自愈：消息端点返回 401 或 `11242` / `11243` 时清缓存重取并重试一次，该次投递成功；频控等业务失败不触发重试。
- 凭证兜底：无论通道当前类型为何，config 中出现非 `${ENV}` 形态的 `appSecret` 一律被拒。
- 失败语义：开放平台返回 HTTP 200 但 `code` 非 0 时判定投递失败并回报错误；投递失败不影响告警事件落库。
- 前端：通道对话框选择 `qq` 时展示对应字段，明文密钥拦截生效；中英文案无缺失（不出现裸 key）。
- 自动化验证：
  - `go test ./internal/controlplane/service/ -run 'Channel'`
  - `cd apps/control-plane-web && pnpm test src/pages/alerts`

## 6. 风险 / 待定

- **【真机实测 · 产品级限制】群聊主动消息发不出去（本 FR 的 group 目标在平台上不可用）**。2026-09-29 用真实机器人（`1<<25` 权限已生效、机器人确已在群内）实测：`POST /v2/groups/{group_openid}/messages` 发主动消息（不带 `msg_id`）返回 `40034105 主动消息失败, 无权限`；`GET /v2/groups/{openid}/bot_state` 显示 `allow_proactive_msg=false`。
  - 官方对该码只写「请检查机器人权限设置」，**未提供任何可配置项、后台开关或申请入口**；
  - 官方 `tencent-connect` 组织自有仓库维护者明确表态：「**群内的主动消息平台侧不支持，暂时无法实现**」「不推荐使用群聊方式接入……**推荐使用私聊**」（2026-02，两条独立评论），2026-09 仍有用户复现同样问题且无人给出可解方案；
  - 同期官方文档的群主动消息频控表（未认证 30qpm、每群 1000 条/天）与实测行为**互相矛盾**，属文档超前于平台实现；
  - **同一机器人的单聊主动消息实测投递成功**（用户实际收到），说明并非「未上架一律禁发」。
  - → 影响：`targetType=group` 在真机上不可用，**纯群告警场景不能作为交付承诺**；当前唯一稳定的主动通道是**单聊（c2c）**。群内消息仅在有 `msg_id`（成员 @ 后 5 分钟 / 最多 5 次）或 `event_id`（如 `GROUP_ADD_ROBOT`）时可做**被动回复**。
  - → 处置：通道类型与投递实现保留（代码与接口形态均正确，平台开放后即可用），但**文档与 UI 必须如实标注该限制**，不得让运维误以为配好群通道就能收告警。是否改造为「单聊为主 + 群内被动回复为辅」需用户决策，另立 FR。
- **前端已下线群聊路径（2026-09-29）**：因平台拒绝群主动消息（见上一条），通道表单不再暴露 `targetType` 选择，一律按单聊提交；存量 `group` 通道给出提示但不阻塞保存。后端 `group` 分支按平台开放后可复用的前提保留。
- **扫码绑定入口（FR-495 交付）**：通道表单内置「扫码绑定」——经 `q.qq.com` 绑定页取得 `appId` / `appSecret` / `user_openid`，前端只填引用名 `${QQ-<appId>}`，**明文密钥不经前端**；密钥落盘 `<dataRoot>/etc/qq/qq-<appId>.key`（0600），解析时由 `resolveEnvRef` 在环境变量未命中后回落读取。
- **密钥回落目录必须与 CP 主密钥隔离**：回落查找是「变量名小写 + .key」的通用映射，若目录指向 `etc/` 本身，`${WS-TOKEN-SECRET}` 会命中 CP 主密钥文件（越权读取）。实现上回落目录**固定派生**为 `<dataRoot>/etc/qq`，且不提供「直接指定目录」的接口以避免误配；`backup_storage_test.go` 有守门用例（含变异验证）。
- **群消息禁止含 URL**。开放平台对群消息返回 `40054010 不允许发送URL`。告警标题或消息里若带面板 / 日志链接会被直接拒投。本期不做短链改写，在通道说明中提示运维避免在 QQ 群告警文本中使用 URL；如需改写应另立 FR。
- **接收方可单方关闭主动推送**。群管理员或用户关闭后，投递失败（`GROUP_MSG_REJECT` / `C2C_MSG_REJECT` / `40054013`），且 CP 无法感知开关状态（`bot_state.allow_proactive_msg` 仅白名单机器人可查）。表现为通道静默失效，需靠测试发送排查。
- **`group_openid` 需手工获取**。无官方群列表接口，运维须从开放平台后台或调试工具取得 openid 后填入；填错时投递失败但不影响其它通道。
- **主动消息配额与频控**。现行官方口径为每群 / 每好友 1000 条/天，同一关系 20 qpm，Bot 级 60 qpm（未认证 30）。告警场景远低于此，但告警风暴时仍可能撞频控；沿用 FR-085 既有去抖与静默窗口缓解。
- **不引入官方 Go SDK `botgo`**：该 SDK 最后提交为 2024-12-18，其内置域名常量（`api.sgroup.qq.com` / `bots.qq.com`）未跟进现行 `api.bot.qq.com`，且本 FR 只需「取 token + 发一条文本消息」，引入完整 SDK（含 WS 网关、事件模型）与收益不成比例。实现自带最小 HTTP 客户端，`baseUrl` 可配置以适配域名变更。
- **域名口径存在新老差异**。官方现行文档统一 `https://api.bot.qq.com`，历史文档与 SDK 用 `bots.qq.com`（token）+ `api.sgroup.qq.com`（业务）。默认取现行口径，`baseUrl` 可覆盖；若线上回归发现默认域名不可用，改配置项而非改代码。
- **单聊通道需先有好友关系**（`40054004`），且主体认证级别影响机器人的可服务范围；本 FR 不做资质校验，仅在文档提示。
- **无 ADR**。本 FR 复用既有通道模式，未引入新的进程、协议或数据模型；token 缓存是 `ChannelNotifier` 内部实现细节，判定为不需要 ADR。
