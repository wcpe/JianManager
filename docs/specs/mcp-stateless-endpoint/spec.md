# 功能规格：MCP 端点无状态化与 Token 维度运维视图

> 状态：开发中 · 关联 PRD：FR-489 · 关联 ADR：**ADR-096**（部分取代 ADR-077 决策 5、修订 ADR-080 决策 6）· 修订：FR-389 / FR-391 · 作废：FR-439 · 数据源：FR-390

## 1. 背景与目标

CP 内嵌 MCP 网关（FR-389 / ADR-077）把「会话」做成了协议一等公民：`initialize` 创建内存会话并以响应头 `Mcp-Session-Id: mcps_<hex>` 下发，后续请求必须携带；会话承载归属校验、空闲/绝对超时、全局与每 Token 并发上限、管理员列表/踢线，以及**初始化时的 principal 快照**。该模型在两个方向上都已构成实际障碍：

1. **通道脆弱**。会话是「服务端必须记得客户端」的契约：CP 进程重启（部署、崩溃、滚动升级）或会话空闲到期后，客户端手里的 `mcp-session-id` 即失效，后续请求得到 `404 SESSION_GONE`。要恢复客户端必须重新 `initialize`，而主流 MCP 客户端收到该错误后不会自动重发 `initialize`——通道因此**永久卡死**，只能人工重连。
2. **与规范方向相悖**。MCP 的会话无状态化提案 **SEP-2567**《Sessionless MCP via Explicit State Handles》已合并，官方 Go SDK v1.8.0 已提供对齐实现（`StreamableHTTPOptions.Stateless`）。继续加深会话耦合是逆流。

同时会话快照引入一处**授权缺口**：`tools/list` / `tools/call` 的能力判定取的是初始化时写进会话的 principal 快照，而每请求到达时其实已经由 `middleware.AgentAuth` → `AgentTokenService.Authenticate` 重新解析过一次（该链路校验吊销与过期）。二者不一致时以快照为准，意味着 **Token 被吊销、scope 被下调后既有会话仍按旧能力放行**，直到客户端重新 `initialize` 才收敛。

**目标**：Streamable HTTP 路径去会话化（对齐 SEP-2567），授权一律取每请求重建的 principal；SSE 兼容路径的连接态收敛为「SSE 传输连接登记」；运维视图随之由「会话维度」改为「Token 维度」，数据源改为既有的 `agent_call_logs`（FR-390）——两者技术耦合一次交付：删协议会话必然让会话页失去数据源。

**阶段**：P1 · Agent 远程入口的可用性与授权正确性加固。

## 2. 需求（要什么）

### 范围内

- `POST /api/v1/mcp` 去会话：不读也不写 `Mcp-Session-Id`；`initialize` 直接返回 `initializeResult()`；每个请求独立鉴权、独立处理。
- **授权一律取每请求重建的 principal**，不再有 principal 快照；鉴权链不变（仍只认 Agent Token `jmat_`），策略仍唯一落在 CP（ADR-076）。
- 移除协议会话的全部运维语义：空闲/绝对超时、超时巡检、全局与每 Token 并发上限、会话列表与踢线、`SESSION_GONE` 与超限 429，以及 `mcp.session.open/close/kick` 流水。
- `GET /api/v1/mcp` 改为 **405** + `Allow: POST`；`DELETE /api/v1/mcp` 同样返回 **405** + `Allow: POST`（无会话可终止；按 Streamable HTTP 规范建议显式回 405，而非落到通用 404）。
- **保留 SSE 兼容路径**（`GET /api/v1/mcp/sse` + `POST /api/v1/mcp/message`），连接态收敛为 SSE 传输连接登记（连接 id / principal / IP / 回推通道 / 取消），不承载超时、并发上限、能力快照等协议会话语义，连接断开即注销。
- 移除 `GET/DELETE /api/v1/agent/mcp/sessions`，新增 `GET /api/v1/agent/mcp/activity?window=`（按 Token 聚合的活动视图，契约见 [api.md](api.md)）。
- 移除配置项 `mcp.idle_timeout`、`mcp.absolute_timeout`、`mcp.max_global_sessions`、`mcp.max_sessions_per_token`，同步配置结构与示例配置 `configs/control-plane.yml`（全仓已无这四项残留；`docker-compose.yml` 本就未配置这些键，无需改动）。
- 前端「MCP 会话」页改为按 Token 聚合的活动视图：页面/组件 `McpActivityPage`、路由 `/mcp-activity`，旧路径 `/mcp-sessions` 保留重定向。

### 不做

- **不删 SSE 兼容路径**：其连接态是传输固有（`/message` 必须把工具结果回推到 `/sse` 已建立的 HTTP 响应流上），删除它削减 FR-389 已承诺的能力，与 SEP-2567 无关。
- 不改权限 ID `agent.mcp.read`（仅改 Label / 文案）。
- 不在 MCP 层再造策略，不新增策略分支（沿用 FR-395 的 action 目录与能力闸）。
- 不在本 PR 内做与 MCP 无关的重构，不改 `agent_call_logs` 的表定义与保留策略（仍默认 14 天）。
- 不为兼容旧客户端保留会话模式的开关或双轨路径。

## 3. 端点与协议语义

### 3.1 路由

| 方法 | 路径 | 语义 |
|---|---|---|
| POST | `/api/v1/mcp` | Streamable HTTP，**无状态**：不读也不写 `Mcp-Session-Id`；每请求独立鉴权 |
| GET | `/api/v1/mcp` | **405** + `Allow: POST`（无会话可保活） |
| DELETE | `/api/v1/mcp` | **405** + `Allow: POST`（无会话可终止） |
| GET | `/api/v1/mcp/sse` | SSE 兼容路径，**保留**（传输层连接态） |
| POST | `/api/v1/mcp/message?sessionId=` | SSE 兼容路径，**保留** |
| GET | `/api/v1/agent/mcp/activity?window=24h` | **新增**活动视图（按 Token 聚合） |
| ~~GET/DELETE~~ | ~~`/api/v1/agent/mcp/sessions`~~ | **移除** |

### 3.2 行为要点

- **鉴权**：仍只认 Agent Token（`Authorization: Bearer jmat_...`）。人类 JWT **不能**充当 MCP 凭据（401）——无状态化不改变这一点，只把「建立会话的凭据」变成「每个请求的凭据」。
- **不再有会话响应头**：响应不下发 `Mcp-Session-Id`；请求若携带旧 `mcp-session-id`（头或 SSE `/message` 的 `sessionId` 之外的形态）**被忽略而非拒绝**，故存量客户端在会话失效后不再卡死，而是自动恢复正常。
- **能力判定**：`tools/list` 按**本请求**的 principal 动态裁剪，`tools/call` 无论是否出现在 list 均做最终授权；策略拒绝仍是 HTTP **200** + `result.isError=true` + 中文 message（**不得 5xx**）。
- **错误语义变化**：`404 SESSION_GONE` 与超限 `429` 不再出现（无会话可失效、无配额可超）；无凭据仍 **401**；参数/协议错误行为不变。SSE `/message` 携带无法识别的连接 id 时仍回 404 `CONN_GONE`（**传输**连接不存在，须重新连 `/sse`），与协议会话无关。
- **SSE 兼容路径**：`GET /api/v1/mcp/sse` 建立 SSE 响应流并推送 `endpoint` 事件；`POST /api/v1/mcp/message?sessionId=`（或 `Mcp-Session-Id` 头）接收 JSON-RPC 并把响应经该 SSE 流回推；该 id 只校验「连接存在且属于当前 Token」（不存在 404、不属于当前 Token 403）。连接 id 沿用 `mcps_<32hex>` 形态，`sessionId` 参数名与取值形态不变（它是传输连接标识，不是协议会话）。

## 4. 生命周期

**Streamable HTTP 路径：无会话生命周期。** 每个请求独立开始与结束——到达时鉴权 → 重建 principal → 处理 JSON-RPC → 返回。没有「服务端记得客户端」的状态，因此不存在会话失效、超时、并发配额或踢线；CP 重启与长时间空闲都不再能中断通道。

**SSE 路径：传输连接登记。** 连接在 `GET /sse` 建立、在 `/message` 回推结果、在断开时注销；**进行中的 tool call 绑定在该连接上，连接被关闭/注销即随之取消**（与无状态化前同语义）。不再有超时巡检 goroutine、不再有 `LastTool` / `Count` / `List` 等会话视角字段。

**移除的语义**（连同其代码一并消失，不留空壳）：空闲/绝对超时、超时巡检、全局与每 Token 并发上限、会话列表与踢线、`SESSION_GONE` 与超限 429、`mcp.session.open/close/kick` 流水，以及四个 `mcp.*` 配置项。

**管理员干预方式的变化**：无会话可踢，需要立即切断某凭据的访问时改为**吊销该 Token**——下一个请求即失效（这正是每请求授权带来的性质）。

## 5. 与既有模块的关系

- **FR-389（CP 内嵌 MCP 网关）**：本 FR 修订其会话运维模型；主路径形态（CP 内嵌、Streamable HTTP 主 + SSE 兼容、工具集与动态裁剪）不变。见 [../cp-mcp-server/spec.md](../cp-mcp-server/spec.md)。
- **FR-390（Agent 调用流水）**：`agent_call_logs` 是活动视图的**唯一数据源**——它已是每 Token 每次调用的权威流水，按 Token 聚合即可回答「谁在用 MCP、最近何时用、用了什么、失败多少」，且**不依赖任何进程内状态**（跨重启连续，而进程内会话列表在 CP 重启后即为空）。本 FR 不改其表定义，只去掉会话类 action 的产生点。见 [../agent-call-log/spec.md](../agent-call-log/spec.md)。
- **FR-391（Agent 观测 UI）**：MCP 会话页改为 Token 活动视图，页面/路由随之改名。
- **FR-439（会话并发上限造成永久 429）**：随本 FR 整体作废——协议会话与并发上限均已移除，该缺陷的载体不存在。
- **ADR-077**：决策 5（会话内存可运维）与「后果」中的会话超时/并发上限、「CP 重启会话全丢可接受」被 ADR-096 取代；决策 1/2/3/4/6 仍有效。
- **ADR-080**：修订其决策 6 的论证——不再需要「MCP 会话中的 principal 快照不会出现策略热更新漂移」，因为不再有快照，每请求重新解析 principal 天然一致。见 [../agent-capability-policy-v2/spec.md](../agent-capability-policy-v2/spec.md)。
- **FR-388（Agent 入口可证安全闸）**：契约表中「会话踢线后 401」一类断言随会话消失；鉴权/scope/策略拒绝断言不变。

## 6. 模块设计

- **`internal/controlplane/mcp/`**：删除 `SessionManager` / `Session`，替换为 **`SSEConnRegistry` / `SSEConn`**（仅服务 SSE 传输）：`SSEConn` 持 `ID`、`Principal`、`ClientIP`、`ConnectedAt`，并提供回推通道、`SendSSE`、`Context`、`Close`；`SSEConnRegistry` 提供 `Register` / `Get` / `Unregister` / `Stop`。ID 生成保留 `mcps_<32hex>` 前缀，使 `/message?sessionId=` 继续可用。
- **`dispatch` 入参**由 `*Session` 改为 `*service.AgentPrincipal`：`tools/list`、`tools/call` 的能力判定改用每请求重建的 principal。删除 `Handler.Sessions()`（既有死代码）。
- **配置**：`MCPConfig` 四个字段与默认值移除；`internal/controlplane/mcp/config.go` 若整体失去意义则删文件。**不得留下「配置里有但代码不读」的项**。
- **`service.AgentCallLogService.ActivityByToken(window)`**：窗口内按 `token_id` 聚合，返回 `TokenActivity`（tokenId / tokenName / tokenPrefix / lastActivityAt / lastAction / callCount / failureCount / clientIPs / clients），按 `lastActivityAt` 降序。字段契约见 [api.md](api.md)。
- **`router`**：`/mcp` 组注册 `POST` + `GET`(405) + `/sse` + `/message`；管理员组由 `/agent/mcp/sessions`（list/kick）换为 `/agent/mcp/activity`。
- **前端**：`McpSessionsPage.tsx` → `McpActivityPage.tsx`（组件名 `McpActivityPage`），路由 `/mcp-activity`，旧 `/mcp-sessions` 保留 `<Navigate to="/mcp-activity" replace />`；hook `useMcpActivity(window)`，删除 `useMcpSessions` 与 `useKickMcpSession` 及对应类型；表格列改为 Token ｜ 客户端 ｜ 最近活动 ｜ 最近操作 ｜ 调用数 ｜ 失败数 ｜ 来源 IP；原「踢线」按钮改为「吊销 Token」跳转 `/agent-tokens`（无会话可踢）。i18n `mcpSessions.*` → `mcpActivity.*`、`nav.mcpSessions` → `nav.mcpActivity`，zh/en 双语同步。

架构决策见 **ADR-096**，本文不重复决策正文。

## 7. 测试与验收

1. 无 `Mcp-Session-Id` 时可连续调用 `initialize` / `tools/list` / `tools/call`，且响应不含该头。
2. 携带陈旧或伪造的 session id **被忽略而非拒绝**（仍正常服务）。
3. **重启 CP 后同一客户端无需重新 `initialize` 即可继续调用**（旧行为必失败）。
4. `GET /api/v1/mcp` 与 `DELETE /api/v1/mcp` 均返回 **405** 且带 `Allow: POST`。
5. SSE 兼容路径 `/sse` + `/message` 行为不变，连接断开即注销。
6. `GET /api/v1/agent/mcp/activity` 按 Token 正确聚合：多 Token、窗口边界（`window` 越界 400）、失败计数、无数据时返回空数组、`lastActivityAt` 降序。
7. Token 吊销或 scope 下调在**下一个请求**即生效（不再有快照窗口）。
8. 无凭据仍 401；策略拒绝仍 HTTP 200 + `result.isError=true`。
9. 单测覆盖：SSE 连接登记（注册/取用/断开注销）、无状态 `tools/list` 与 `tools/call` 的动态裁剪与最终授权、聚合查询的窗口与排序、管理员端点权限门。
10. 配置/文档一致性：仓库内不再出现四个已移除配置项；示例配置与 `docker-compose.yml` 不含「有配置无代码」的项。
11. **真机**（需用户环境或既有验收 CP）：HTTPS 远程走通至少一条 Streamable HTTP 或 SSE 全路径，且重启控制面后通道不中断。

## 8. 风险 / 待定

- **SSE 的 `sessionId` 易被误读为协议会话**：它现在只是传输连接标识（同名参数保留是为不破 `/message` 的既有形态）。文档与页面文案需明确这一区别，避免后续被当成协议会话重新引入超时/配额语义。
- **失去「踢线」这一即时手段**：正在进行的 tool call 无法被管理员中途切断，只能吊销 Token 让后续请求失效。对绝大多数场景足够（调用都很短），但长耗时调用的现场处置能力弱于改造前。
- **活动视图依赖流水保留期**：窗口上限 168h 落在默认保留期（14 天）内，故聚合不落空；若某部署把保留期调到 7 天以下，需同步收紧窗口上限或接受窗口内数据不完整。
- **存量客户端行为差异面**：符合 Streamable HTTP 的客户端无需改造即可受益；把 `Mcp-Session-Id` 当作必带凭据的自研脚本，其 header 会被忽略而非报错，异常时排障线索比 404 少。
- **Gin 与长连接/流式响应的缓冲与超时中间件是否截断 SSE**：改造前遗留的验证点，SSE 路径保留后仍须成立。
