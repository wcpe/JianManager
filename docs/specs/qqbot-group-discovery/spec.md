# 功能规格：QQ 机器人扫码接入与已发现群列表

> 状态：开发中（真机验收通过；**群聊路径已下线、控制台改为扫码绑定**，见 §5.1）　·　关联 PRD：FR-495（增强 FR-494）　·　分支：feat/qqbot-notification-channel

> **2026-09-29 后续变更**：平台拒绝群主动消息（`40034105`），前端群聊路径（目标类型选择、已发现群下拉）**已下线**；控制台新增**扫码绑定**——经 `q.qq.com` 绑定页自动取得 `appId`/`appSecret`/`user_openid`，密钥落 `<dataRoot>/etc/qq/`、前端只填 `${QQ-<appId>}`。本说明涉及的分享链接/网关/群发现端点保留（平台开放后可复用）。回落目录与 CP 主密钥的隔离见 FR-494 spec。

## 1. 背景与目标

FR-494 交付了 QQ 机器人通知通道，但 `targetId`（`group_openid`）只能手工填入——官方没有「获取已加入群列表」接口。运维流程断在「把 `group_openid` 抄进通道配置」这一步，出错率高且不可验证。

本 FR 让这个流程闭环：**运维在控制台点一下生成分享二维码 → 手机 QQ 扫码 → 把机器人拉进群 → CP 自动获知该群并出现在「已发现群」列表 → 建通道时从下拉选群**，不再手抄 openid。

产品决策（已由用户确认）：

- **手动选群**：后端只维护「已发现群」列表，建通道时从下拉选择。进群事件无归因字段，自动建通道会产生无人认领的垃圾通道。
- **单机器人共享**：一个机器人进多个群，所有 `qq` 通道共用 `appId`/`appSecret`，只换 `targetId`。个人主体机器人名额有限（5 个），共用最省配额；token 缓存也是按 `appId` 设计的。
- **WebSocket 事件接收**：官方「不依赖公网服务器部署」，符合不暴露入站端口的约束。代价是 CP 维护一条长连接（心跳 + 断线重连）。

目标：

- 后端可生成机器人分享链接并以前端二维码展示（二维码渲染在前端，后端只返回 URL）。
- CP 常驻一条 QQ 网关 WebSocket 连接，收到 `GROUP_ADD_ROBOT` 即落库 `group_openid`。
- 前端通道表单的「目标 openid」改为「已发现群」下拉 + 手工输入兜底。
- 保持 FR-494 的纯出站投递能力不变；事件链路故障不影响告警发送。

## 2. 需求（要什么）

- 分享链接：指定机器人（`appId`）可生成分享链接，`callback_data` 固定为连接归因用途（`jm:<连接ID>` 格式，最长 32 字符），用于区分多机器人/多连接场景。
- 二维码展示：前端把分享 URL 渲染为二维码；二维码需可刷新（重新生成链接）。
- 网关连接：CP 维护按 `appId` 的网关连接（单连接，不分片），订阅 `intents = 1<<25`（`GROUP_AND_C2C_EVENT`），自动重连，连接状态对外可见。
- 已发现群：收到 `GROUP_ADD_ROBOT` 落库（`group_openid`、`op_member_openid`、`timestamp`，按 `group_openid` 去重）；前端提供已发现群列表查询。
- 通道表单：`targetType=group` 时优先从已发现群下拉选择，仍保留手工输入（兼容单聊 `c2c` 与历史数据）。
- 连接与投递解耦：网关连接断开/未配置时，`qq` 通道的告警投递不受影响（投递只用 HTTP，本来就不依赖网关）。

### 范围内

- 后端：分享链接生成（`POST /v2/generate_url_link`）、网关连接管理（`GET /gateway` → WSS → Identify → 心跳 → 重连/Resume）、`GROUP_ADD_ROBOT` 事件落库、已发现群查询 API、网关连接状态查询 API。
- 前端：分享二维码展示与刷新、通道表单的群下拉（`group` 时）+ 手工输入兜底、已发现群管理视图（列表）。
- 测试：单测覆盖事件解析、去重落库、重连退避；DOM 回归覆盖表单下拉渲染。
- 文档：PRD 新增 FR-495 条目与验收标准、API 端点文档、ARCHITECTURE 事件链路章节。

### 范围外

- **自动建通道**：收到入群事件不自动创建通道，须运维手动选群建通道（已确认的产品决策）。
- **被动回复 / 交互**：不处理群消息事件（`GROUP_AT_MESSAGE_CREATE` 等）与按钮回调；CP 仍不「读」群聊内容，只记录「机器人进了哪些群」。
- **`FRIEND_ADD` 处理**：分享链接的文档定位是「加好友」，`FRIEND_ADD` 归因链完整但本 FR 的主路径是进群；是否需要好友引导流程待真机实测扫码行为后再定，另立 FR。
- **Webhook 事件链路**：本期只做 WebSocket；传输无关的事件处理抽象视实现复杂度决定是否预留。
- **`GROUP_DEL_ROBOT`（机器人退群）处理**：本期只记录入群；退群后群条目是否标失效待定（事件存在，见官方 `group_del_robot` 页，但本期不处理）。
- **多机器人管理**：连接按 `appId` 建模，预留多连接能力，但本期前端只展示「默认机器人」的连接（默认 = 第一个配了 `qq` 通道的 `appId`，或独立配置——实现时二选一定死）。
- 不改变 FR-494 已交付的投递行为。

## 3. 设计（怎么做）

### 3.1 后端

**数据模型**

- 新增表 `qq_discovered_groups`：`group_openid`（唯一）、`op_member_openid`、`first_seen_at`、`last_seen_at`、`source_app_id`。无其它外键，不与通道表硬关联（通道存的是 openid 字符串副本，群条目删除不级联）。
- 新增表 `qq_gateway_connections`（或内存 + 持久状态二选一，实现时定）：`app_id`（唯一）、`status`（connected/connecting/disconnected/error）、`last_event_at`、`last_error`、`session_id`。

**分享链接**

- 复用 `ChannelNotifier` 的 token 能力：`POST {baseUrl}/v2/generate_url_link`，头 `Authorization: QQBot {token}`，body `{"callback_data": "jm:<连接ID>"}`。
- 返回 `{"data": {"url": "..."}}`；失败走 `checkQQResponse` 同款判定（HTTP 状态 + `code`）。限频 50 QPS，控制台触发频率可忽略。
- `callback_data` 取 `jm-<appId>`（连接按 `appId` 建模，故归因标识就是 appId）。**分隔符必须是连字符而非冒号**：官方对该参数做字符校验，含冒号会被直接拒绝——实测返回 `invalid GetCustomShareJumpUrlReq.CallbackData: value contains invalid strings`（retcode=51），即分享链接 100% 生成失败。字母、数字与连字符可用。总长受官方 32 字符上限约束，`jm-` + appId 超过 32 字符时直接回报配置错误，不截断（截断会归因到错误的机器人）。

**网关连接管理**

- 启动流程：`GET /gateway`（同样 `QQBot` 鉴权）拿 `wss://…` 地址 → 拨 WSS → 收 Op10 Hello 取 `heartbeat_interval` → 发 Op2 Identify（`token: "QQBot {AccessToken}"`、`intents: 33554432`、`shard: [0,1]`）→ 收 READY 存 `session_id`。
- 心跳：按 `heartbeat_interval` 发 Op1（`d` = 最新 `s`），收 Op11 ACK；ACK 超时视为断线。
- 重连：指数退避（初值 1s，上限 5min，加抖动）；短时断线发 Op6 Resume（`token` + `session_id` + `seq`），收 RESUMED 继续；`session_start_limit.remaining` 耗尽或 4014（intent 无权限）/ 4914（下架）/ 4915（封禁）时停止重连并置 error 状态（这些是人工介入信号，重试无意义）。
- 生命周期：连接管理挂 CP 进程（与 `AlertEvaluator.Start()` 同类后台服务），随 CP 启停；**不阻塞 CP 启动**（网关不可达时后台重试，前台 API 照常服务）。
- 失败可见性：所有可重试的建连失败（凭证错误、网关地址获取失败、拨号失败、Hello 异常、Resume / Identify 发送失败）都必须把原因写入 `last_error` 并保持 `connecting` 态，供 API 与前端展示——**不得只落日志**。退避前也不得清空 `last_error`，否则「机器人不存在 / 密钥错误 / 网关不可达」这类最常见的配置错误在 UI 上只表现为永远「连接中」、无任何原因。（该盲区由真机冒烟验证以错误凭证起真实 CP 实测复现，已修复并加回归用例。）
- WSS 客户端直接用 `github.com/gorilla/websocket v1.5.3`（已是直接依赖，终端代理与 Worker WS 桥都在用，零新增依赖）。

**事件处理**

- 只处理 `GROUP_ADD_ROBOT`：解析 `group_openid` / `op_member_openid` / `timestamp`，按 `group_openid` upsert（存在则刷新 `last_seen_at`）。
- 其余事件类型收下即丢（debug 日志），不做消息内容处理。
- 去重：`group_openid` 唯一约束 + upsert，无需 `msg_seq` 去重（入群事件是状态型，非消息型）。

**REST 端点（挂 `protected` 分组，`alert.manage` 权限，与通道 CRUD 一致）**

- `POST /api/v1/alerts/qq/share-link`：body `{"appId": "..."}`（或空 = 默认机器人）→ 返回 `{"url": "..."}`。`appSecret` 从服务端配置解析（见下），**不接受前端传入密钥**。
- `GET /api/v1/alerts/qq/groups`：已发现群列表（分页，含 `group_openid`、`op_member_openid`、`first_seen_at`、`last_seen_at`）。
- `GET /api/v1/alerts/qq/gateway/status`：连接状态（`app_id`、`status`、`last_event_at`、`last_error`）。

**机器人密钥来源（待定，实现时二选一并写进 ADR 或 spec 修订）**

- 方案 A：复用已配置的 `qq` 通道——默认机器人 = 第一个启用 `qq` 通道的 `appId`，`appSecret` 从其 config 的 `${ENV}` 解析。优点：零新配置；缺点：删掉最后一个 qq 通道后分享与网关跟着失效。
- 方案 B：独立配置项（如环境变量 `JM_QQ_APP_ID` / `JM_QQ_APP_SECRET`）。优点：与通道解耦；缺点：多一处配置。
- 无论哪种，`appSecret` 永不经 API 返回前端。

### 3.2 前端

- 通道表单（`targetType=group` 时）：「目标群」下拉（数据来自 `GET /qq/groups`，展示 `group_openid` + 发现时间）+ 「手工输入 openid」切换；`c2c` 时保持纯手工输入。
- 新增「QQ 接入」视图（挂在通道页内或独立小节，实现时定）：分享二维码展示（前端用 `qrcode` 相关轻量库或 `<canvas>` 自绘，**不要手写二维码编码**）、刷新按钮、网关连接状态徽标、已发现群列表。
- 二维码库选型：项目原本零二维码依赖，实际新增 **`react-qr-code@^2.2.0`**（`dist.unpackedSize` 约 19.9 KB，含 `qrcode-generator` 依赖；对比 `qrcode.react` 约 115 KB 小约 5.8 倍）。渲染为 SVG，无 canvas/浏览器 API 依赖，jsdom 中可直接断言。

### 3.3 Mock 与测试数据

- devmock：`/qq/groups` 返回两条假群；`/qq/share-link` 返回固定假 URL；`/qq/gateway/status` 返回 connected。前端 DOM 回归不依赖真实网关。

## 4. 任务拆分

- [x] 补齐本规格并经审核通过。**密钥来源拍板：方案 A**（复用首个启用 `qq` 通道的 `appSecret`）——零新配置契约，且与「单机器人共享 + token 按 appId 缓存」的决策一致。
- [x] 后端：数据模型 + 分享链接生成 + 网关连接管理 + 事件落库 + 三个 REST 端点。
- [x] 后端测试：事件解析与去重、重连退避状态机（用假 WSS 服务端）、端点鉴权与参数校验。
- [x] 交叉复核并修复：心跳 goroutine 每次重连泄漏、Op9 会话失效未处理（会导致「连上但永不再收事件」的假绿灯）、退避档位不复位、ACK 超时不断开、`Stop()` 抹掉人工介入诊断；补齐 Resume / Op9 / ACK 超时 / goroutine 不泄漏的回归用例（含变异验证）。
- [x] 前端：二维码展示与刷新、群下拉 + 手工兜底、接入视图与状态徽标；接入小节按 `alert.manage` 门禁（只读用户不再看到必然失败的写请求）。
- [x] 前端测试：helper 与表单 DOM 回归（devmock 覆盖三端点，且信封形状与真实后端一致）。
- [x] 真机冒烟验证（真实二进制，非单测）：CP 启动不被网关阻塞、两张新表与唯一索引建成、三个端点鉴权生效（401）、未配置 `qq` 通道时安静待机不写库。**并据此抓到并修复了测试覆盖不到的盲区**——建连失败只落日志、退避前清空 `last_error`，导致配置错误在 UI 上表现为永远「连接中」。
- [x] 文档同步：PRD（FR-495 条目 + 验收标准 + 索引）、API.md、ARCHITECTURE.md。
- [ ] 真机验收：测试号扫码 → 进群 → 后端出现该群 → 选群建通道 → 测试发送到达。

## 5. 验收标准

- 分享：调分享端点可拿到分享 URL（**实测返回短链 `https://bot.q.qq.com/s/<码>`**，非文档示例的旧格式），前端原样渲染为可扫二维码；刷新后重新生成。
- 网关：CP 启动后网关连接状态为 connected（需机器人已申请 `1<<25` 权限；无权限时状态为 error 且错误信息明确指向 intent 权限）。
- 发现：测试号把机器人拉进群后 1 分钟内，已发现群列表出现该 `group_openid`（附操作人 openid 与时间）；重复进群不产生重复条目。
- 建通道：通道表单 `group` 类型可从下拉选群提交，提交的 `targetId` 与所选群一致；手工输入仍可用。
- 解耦：网关断开时 `qq` 通道测试发送仍成功（投递走 HTTP，不依赖网关）。
- 密钥安全：分享/状态/群列表三个端点的响应均不含 `appSecret`；前端任何位置不出现密钥明文。
- 自动化验证：
  - `go test ./internal/controlplane/service/ -run 'QQ'`
  - `cd apps/control-plane-web && pnpm test src/pages/alerts`

## 5.1 真机验收记录（2026-09-29，真实机器人）

> 本节只记结论，**不记任何真实标识**（appId / openid / group_openid 一律不入库）。

凭据经官方 `@tencent-connect/qqbot-connector` 扫码绑定获得（非手工抄录）。逐项结果：

| 验证项 | 结果 | 证据 |
|---|---|---|
| 扫码绑定取凭据 | 通过 | 返回 `{appId, appSecret, userOpenid}`；二维码有效期约 3 分钟，过期自动刷新 |
| 网关连接 | 通过 | `status=connected`，说明 `1<<25 (GROUP_AND_C2C_EVENT)` 权限已生效 |
| 入群事件自动落库 | 通过 | `group_openid` 由事件自动获得（非手工填入）；`op_member_openid` 与扫码用户一致；`first_seen_at` 与平台侧 `joined_at` 秒级吻合 |
| 单聊主动消息投递 | 通过 | 走生产代码路径 `ChannelNotifier.Send`，用户实际收到消息 |
| **群聊主动消息投递** | **失败** | `40034105 主动消息失败, 无权限`；`bot_state.allow_proactive_msg=false` |
| 分享链接生成 | 通过（修 bug 后） | 见下 |

真机暴露并修复的缺陷：

- **`callback_data` 不接受冒号**：原设计 `jm:<appId>` 被官方拒绝（`retcode=51 invalid ... value contains invalid strings`），分享链接 100% 生成失败。已改 `jm-<appId>`（代码、测试、spec 同步）。该缺陷不会被单元测试发现——假服务端不做字符校验。
- **建连失败原因不写状态**：只落 slog，且退避前清空 `last_error`，导致「机器人不存在 / 密钥错误」在 API 与前端只表现为永远「连接中」。已修复（后端写状态 + 前端 `connecting` 态也展示）。
- **接口实际频控远严于文档**：`generate_url_link` 文档标 50 QPS，实测约 9 次即 `100017 接口调用超过频率限制`。

**群主动消息的硬约束（对本 FR 与 FR-494 均构成产品级限制）**：

- `bot_state` 显示 `allow_proactive_msg=false`，官方 [发送群聊消息](https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_groups_group_openid_messages.post.html) 页对该码只写「请检查机器人权限设置」，**未给出任何可配置项或申请入口**。
- 官方 `tencent-connect` 组织自有仓库维护者表态：「群内的主动消息平台侧不支持，暂时无法实现」「不推荐使用群聊方式接入，后续群聊不会支持完整功能，推荐使用私聊」。
- 官方文档的频控表（未认证 30qpm、每群 1000 条/天）与实测行为**相互矛盾**，属文档描述超前于平台实现。
- 结论：**群聊主动推送不能作为交付承诺**；可行的主动通道是**单聊**，群内消息只能在有 `msg_id`（成员 @ 后 5 分钟 / 5 次）或 `event_id`（如 `GROUP_ADD_ROBOT`）时做**被动回复**。

## 6. 风险 / 待定

- **扫码真实行为待实测**：官方文档把分享链接定位为「邀请用户添加机器人为好友」，扫码后客户端能否直接进群、还是先加好友再由用户手动拉群，**必须拿测试号实测**。若只能加好友，则产品流程变为「扫码加好友 → 单聊引导拉群」，`FRIEND_ADD` 的归因链反而完整——这是好消息，不是坏消息，但流程图要改。
- **进群事件无归因字段**：`GROUP_ADD_ROBOT` 只有 `group_openid` / `op_member_openid` / `timestamp`，多码投放无法区分来源。按「手动选群」决策，归因只用于 `callback_data` 区分多机器人，不阻塞主流程。
- **离线窗口永久丢失**：无群列表接口补偿，CP 离线/未订阅期间的入群无法事后找回。Resume 只补短时断线。生产要求网关连接高可用；本期不做补偿同步（如需，评论区有替代：让运维在群里 @ 机器人一次，用 `GROUP_AT_MESSAGE_CREATE` 的 `group_openid` 补录——可作为后手，不在本期范围）。
- **`1<<25` 需申请权限**：无权限 Identify 直接被关连接（4014）。接入视图的状态徽标必须把这个错误翻译成人话。
- **个人认证进群上限 500**：单机器人共享策略下天花板明确，够用但要写进文档。
- **落库在读循环内同步执行**：事件 `GROUP_ADD_ROBOT` 的 DB 写入发生在 WS 读循环里，若慢查询持续超过 `heartbeat_interval`，会推迟 ACK 消费并被判心跳超时，触发一次多余重连（Resume 补发事件，不丢数据）。入群事件日均个位数，本期不做队列化；事件量上升时应改为异步入队。
- **WS 库选型已定**：`gorilla/websocket v1.5.3` 现成可用（终端代理与 Worker WS 桥已在用），零新增依赖。前端二维码库为新增依赖 `react-qr-code`（见 §3.2）。
- **未配置 `qq` 通道时网关空转**：凭证解析失败会每秒重试一次（仅一条 `SELECT`，不写库、不刷日志），状态显示「未配置启用的 QQ 通知通道」。已记入已知行为，后续可改为指数退避。
- **分享链接接口的实际频控远严于文档**：官方文档标注 `generate_url_link` 为 50 QPS，但实测连续调用约 9 次即返回 `100017 接口调用超过频率限制`（`err_code=40023001`）。因此「刷新二维码」按钮必须做前端节流或退避，`TestSend` 类连点也不宜密集触发该接口。
- **分享链接返回短链而非旧格式**：实测返回 `https://bot.q.qq.com/s/<短码>`（文档示例里的 `qun.qq.com/qunpro/robot/qunshare?...` 已不是现行形态）。前端只需把返回的 URL 原样渲染成二维码，不要对 URL 形态做任何假设或拼接。
- **机器人密钥来源二选一**（§3.1 待定）：实现前必须拍板，写死后改不动（涉及配置契约）。
- **botgo 的 WS 下线告示已过时**（时间表过期近两年未执行，现行文档称 WS/Webhook 并存），不作为决策依据；但事件处理建议写成传输无关，为未来切 Webhook 留余地。
- 无 ADR（复用 FR-494 的「无 ADR」判定逻辑：未引入新进程/协议——WSS 是出站客户端连接，不是新服务；若评审认为网关连接管理算架构决策，则补 ADR）。
