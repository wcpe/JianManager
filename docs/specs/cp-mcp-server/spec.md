# 功能规格：CP 内嵌 MCP 长连接服务

> 状态：已交付@v0.20.0　·　关联 PRD：FR-389（**会话运维模型已由 FR-489 / ADR-096 取代**，见 [../mcp-stateless-endpoint/spec.md](../mcp-stateless-endpoint/spec.md)）　·　依赖：FR-384　·　ADR：ADR-077（决策 5 与两条后果被 ADR-096 部分取代）　·　取代：FR-386

## 1. 背景与目标

远程 IDE / 运维需要可远程接入的 MCP；首版 stdio `mcp-bridge`（FR-386 / 原 ADR-077）无法支撑远程 Streamable HTTP/SSE。  

**目标**：在 Control Plane **内嵌** MCP 网关（Streamable HTTP 主 + 兼容 SSE），鉴权与工具策略仍 **100% 走 FR-384 Agent Token**。首版的「会话内存可运维」模型已由 FR-489 / ADR-096 取代：Streamable HTTP 路径无状态、每请求重建 principal 授权，SSE 兼容路径的连接态收敛为传输连接登记。

**阶段**：P0 · Agent 远程入口真源。

## 2. 需求（要什么）

### 范围内

- CP 暴露 MCP 端点（路径在实现时定稿并写入 `docs/API.md`，建议：
  - Streamable HTTP：`POST/GET /mcp`（或 `/api/v1/mcp`，**二选一写死**，避免双轨）
  - SSE 兼容：`GET /mcp/sse` + 消息 `POST` 配套（与 MCP SSE 传输约定对齐）
- **鉴权**：`Authorization: Bearer <jmat_…>` 或等价 MCP 初始化参数中的 token → `AgentAuth` / 同一 `Authenticate`
- **工具集**（与 jm-agent / 既有 Agent Ops API 对齐，硬拒绝面**不注册**为 tool；FR-395 起由 ToolSpec → action 目录投影，`tools/list` 按 Token 能力与潜在 scope 动态裁剪）：
  - 读取：`agent_whoami`、`agent_list_nodes`、`agent_list_instances`、`agent_get_instance`、`agent_get_instance_metrics`、`agent_get_instance_logs`
  - 写：`instance_start`、`instance_stop`、`instance_restart`、`node_maintenance_enter`、`node_maintenance_leave`
  - 节点扩展（FR-396）：`node_get`、`node_get_metrics`、`node_check_docker`、`node_drain`、`node_list_archived`、`node_purge_archived`（须 confirmNodeName）
  - 实例扩展（FR-396）：`instance_search`、`instance_get_env`、`instance_list_crash_snapshots`、`instance_create`、`instance_provision_server`、`instance_import_inspect`、`instance_import`、`instance_clone`、`instance_rebuild`、`instance_update_config`、`task_get`、`instance_send_command`、`instance_batch`、`instance_kill`/`instance_delete`（须 confirmInstanceName）
  - 内容运维读（FR-397，`instance.read`）：`file_list`、`file_check_access`、`file_read_text`、`file_versions`、`file_diff`、`config_discover`、`config_read`、`config_cross_check`、`config_versions`、`config_diff`、`plugin_list`
  - 内容运维写（FR-397，`instance.content` / `instance.configure`）：`file_write_text`、`file_rename`、`file_chmod`、`file_rollback`、`file_issue_transfer_ticket`、`config_write_text`、`config_write_fields`、`config_rollback`、`plugin_deploy_from_asset`、`plugin_toggle`
  - 内容运维破坏性（FR-397，须精确确认参数）：`file_delete`（`confirmPath`）、`plugin_delete`（`confirmName`）
- **Bot 舰队与压测编排（FR-398 追加，全部 `V1Allowed=false`、不进 HTTP 契约投影）**：

    | 域 | 工具 | capability |
    |---|---|---|
    | 普通 Bot | `bot_list`、`bot_get` | `bot.read` |
    | 普通 Bot | `bot_create`、`bot_set_behavior`、`bot_send_command`、`bot_delete`（需 `confirmBotName`） | `bot.manage` |
    | 压测模板 | `loadtest_template_list`、`loadtest_template_get` | `bot.read` |
    | 压测模板 | `loadtest_template_create`、`loadtest_template_update`、`loadtest_template_delete`（需 `confirmTemplateName`） | `bot.load` |
    | 运行编排 | `loadtest_run_list`、`loadtest_run_get`、`loadtest_node_capacity` | `bot.read` |
    | 运行编排 | `loadtest_run_create`、`loadtest_run_preflight`、`loadtest_run_start`、`loadtest_run_stop`、`loadtest_run_retry_failed` | `bot.load` |
    | 观测报告 | `loadtest_run_bots`、`loadtest_run_failures`、`loadtest_run_events`、`loadtest_run_report` | `bot.read` |
    | 观测报告 | `loadtest_run_metrics` | `observability.read` |

    这些工具引入 `bot` / `botrun` 两个资源类型：授权均由 CP 数据库解析归属，运行类操作在目标实例之外还要逐一校验 executor 节点（启动方向任一越界整体拒绝，停止方向执行但列出越界节点）。`planToken` 对 MCP 完全不透明（不解析、不重签、不缓存）；`bot_send_command` 的成功语义严格限定为「已发送（`bot.chat` 调用成功）」（ADR-075）；模板按平台级视角管理（ADR-080 附注）。SSE 流式观测不开放，用分页轮询代替。
- tool 调用内部 **只调 CP 本地 service / 统一 action 授权器**，不二次实现 scope/写白名单/capability
- **无状态协议语义（FR-489 / ADR-096 取代原会话运维模型）**：
  - Streamable HTTP（`POST /api/v1/mcp`）不下发也不要求 `Mcp-Session-Id`；`initialize` 直接返回结果，后续请求独立鉴权、独立处理；请求携带旧 session id 被**忽略而非拒绝**
  - 授权一律取**每请求重建的 principal**，不再有会话内的 principal 快照——Token 吊销、scope 或能力调整在下一个请求即生效
  - `GET /api/v1/mcp` 与 `DELETE /api/v1/mcp` 均返回 **405** + `Allow: POST`（无会话可保活 / 无会话可终止；DELETE 由「不注册 → 通用 404」改为显式 405）
  - 原会话字段（sessionId/tokenId/tokenPrefix/clientIP/transport/connectedAt/lastActivityAt/lastTool/idleTimeout/absoluteTimeout）、空闲与绝对超时、全局与每 Token 并发上限、会话列表与踢线、`SESSION_GONE` 与超限 429 **全部移除**；配置项 `mcp.idle_timeout`/`absolute_timeout`/`max_global_sessions`/`max_sessions_per_token` 一并移除
  - SSE 兼容路径保留，连接态为**传输连接登记**（连接 id / principal / IP / 回推通道 / 取消），不承载超时、并发上限或能力快照，断开即注销
- 管理员 API（JWT + 平台管理员）：
  - `GET /api/v1/agent/mcp/activity?window=`：按 Token 聚合的 MCP 活动视图，数据源 `agent_call_logs`（FR-390）；完整契约见 [../mcp-stateless-endpoint/api.md](../mcp-stateless-endpoint/api.md)
  - ~~`GET /api/v1/agent/mcp/sessions`~~、~~`DELETE /api/v1/agent/mcp/sessions/:id`~~ 已移除（无会话可列、可踢）
- 调用流水（与 FR-390 协调）：tool call 记对应 Agent action 与 `capability`；`mcp.session.open/close/kick` 不再产生
- 改写 **ADR-077**：从「stdio 独立 bridge」改为「CP 内嵌 MCP 网关」；注明 FR-386 废弃

### 不做

- 不保留 `apps/mcp-bridge` stdio（删除属 **FR-392**；本 FR 实现期可先并存但不得作为推荐路径）
- 不做 mTLS、写操作面板审批；不做多 CP 进程的会话共享 / sticky 以外的 HA——无状态化后协议侧已无跨进程状态，SSE 连接态仍只存于单进程（FR-489）
- 不在 MCP 层再造策略（禁止本地 allowlist）
- 不做统计大盘（FR-391/后续）

## 3. 设计（怎么做）

### 3.1 模块

- `internal/controlplane/mcp/`：JSON-RPC 与工具契约、无状态 Streamable HTTP 处理、**SSE 传输连接登记**（`SSEConnRegistry`/`SSEConn`：注册 / 取用 / 断开注销 / 回推通道）、传输适配
- `router`：注册 MCP 传输路由（`POST` + `GET`(405) + `/sse` + `/message`）与管理员活动视图 API（`/agent/mcp/activity`）
- 工具调用：复用 `AgentOpsHandler` / `AgentTokenService.CanDiscover|Authorize` + Instance/Node service，**禁止**复制策略分支；MCP 仅持 ToolSpec（name/schema/action/执行器）

### 3.2 生命周期（无协议会话，FR-489）

```
POST /api/v1/mcp → AgentAuth 鉴权 → 重建 principal → 处理 JSON-RPC → 返回
```

Streamable HTTP **没有会话生命周期**：每个请求独立开始与结束，服务端不保存「谁连着」的状态，因此不存在会话失效、超时、并发配额或踢线；CP 重启与长时间空闲都不再中断通道。

SSE 路径：连接在 `GET /sse` 建立、在 `/message` 回推结果、断开时注销；无超时巡检 goroutine，无会话视角字段（LastTool / Count / List 等）。

需要立即切断某凭据的访问时改为**吊销该 Token**——下一个请求即失效（无会话可踢）。

### 3.3 传输

- **Streamable HTTP** 为主路径（MCP 现行远程推荐）
- **SSE** 兼容旧客户端；两传输各自处理——Streamable HTTP 无状态逐请求、SSE 走传输连接登记，鉴权链共用（FR-489）
- 依赖：若引入官方/社区 MCP Go SDK，**须在实现前征得用户确认**（全局依赖管理规则）；无合适依赖时可用最小 JSON-RPC over HTTP 自实现 MVP（spec 允许，须单测对齐工具契约）

### 3.4 安全

- 仅 Agent Token；人类 JWT **不能**充当 MCP 凭据（无论何种传输）
- 不在日志打印明文 Token
- 鉴权失败返回稳定错误码与中文 message（与 FR-388 契约可对齐）；策略拒绝仍 HTTP 200 + `result.isError=true`（超限 429 已随无状态化移除）

架构决策见 **ADR-077（改写）**；会话运维模型的现任决策为 **ADR-096**（无状态化）。

## 4. 任务拆分

- [x] 改写 ADR-077 + 本 spec 状态开发中
- [x] ~~会话管理器：创建/踢线/超时/并发 + 单测~~（会话模型已随 FR-489 移除，改为无状态 Streamable HTTP + SSE 传输连接登记）
- [x] Streamable HTTP MCP 端点 + tools 映射 + 单测（mock service）
- [x] SSE 兼容端点 + 单测
- [x] ~~管理员 sessions list/kick API + 单测~~ → 已由 `GET /api/v1/agent/mcp/activity` 取代（FR-489）
- [x] ~~接入 router / 配置项（超时、并发）~~ → 配置项已随 FR-489 移除
- [x] 文档：API.md、ARCHITECTURE 一节、PRD FR-389→开发中
- [ ] **真机**：远程持 Token 完成 initialize + tools/list + 一次 tool call（会话踢线项已随无状态化作废；无状态化的真机验收转 FR-489）

## 5. 验收标准

1. 有效 Token：Streamable HTTP 与 SSE 均可完成调用；`tools/list` 按当前 Token 能力与可用 scope 动态返回工具（空能力 V2 仅 whoami）  
2. 无效/吊销/过期 Token：调用被拒（401/等价）  
3. scope 外 tool / 能力不足 / 永久禁区：不注册或 call 返回 isError + 中文原因，且 **HTTP 层不得 5xx 当策略拒绝**；未列出的工具手工 call 仍须最终授权拒绝  
4. 会话维度验收项已随 FR-489 作废（无会话列表 / 踢线 / 超时 / 并发上限）；Token 维度活动视图的验收见 [../mcp-stateless-endpoint/spec.md](../mcp-stateless-endpoint/spec.md)  
5. 单测：无状态调用链（无 session id 亦可连续 initialize / tools/list / tools/call）、动态 tools/list、最终授权、SSE 传输连接登记、鉴权失败  
6. **真机**（需用户环境或既有验收 CP）：HTTPS 远程走通至少一条 Streamable HTTP 或 SSE 全路径；重启 CP 后同一客户端无需重新 `initialize` 即可继续调用（FR-489 主验收项）

## 6. 风险 / 待定

- ~~MCP Go SDK 选型与依赖审批~~（已闭：采用最小 JSON-RPC over HTTP 自实现，无第三方 MCP SDK）
- Gin 与长连接/流式响应的缓冲与超时中间件是否截断 SSE——实现时验证（SSE 路径保留后仍须成立）
- SSE 的 `sessionId` 易被误读为协议会话：它现在只是传输连接标识（参数名保留是为不破 `/message` 既有形态），文档与文案须明确区别（FR-489）
- 失去「踢线」这一即时手段：正在进行的 tool call 无法被中途切断，只能吊销 Token 让后续请求失效；长耗时调用的现场处置能力弱于改造前
- 与 FR-390 的 action 命名对齐：**tool 内部仍记 Agent action 名**，`client` 取本次请求归一化后的 `X-JM-Agent-Client`（缺省或不在白名单时归 `unknown`）（见 FR-390）

## 7. UI 契约摘录（FR-391；**已随 FR-489 改为 Token 活动视图**）

- 平台管理 → **MCP 活动**（页面/组件 `McpActivityPage`，路由 `/mcp-activity`；旧路径 `/mcp-sessions` 保留 `<Navigate to="/mcp-activity" replace />`）：按 Token 聚合的表格——Token ｜ 客户端 ｜ 最近活动 ｜ 最近操作 ｜ 调用数 ｜ 失败数 ｜ 来源 IP；**无踢线按钮**，改为「吊销 Token」跳转 `/agent-tokens`（无会话可踢）
- 保留：说明面板（端点、鉴权说明、协议版本）、复制 MCP URL、跳转调用流水、刷新、10s 自动刷新
- Token 创建成功弹窗 / 详情：复制 MCP 基址（如 `https://<cp>/mcp`）+ Token 环境变量说明  
