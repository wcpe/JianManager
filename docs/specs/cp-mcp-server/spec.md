# 功能规格：CP 内嵌 MCP 长连接服务

> 状态：开发中　·　关联 PRD：FR-389　·　依赖：FR-384　·　ADR：改写 077　·　取代：FR-386

## 1. 背景与目标

远程 IDE / 运维需要可长连的 MCP，且会话可运维（可见、可踢、有超时）。  
首版 stdio `mcp-bridge`（FR-386 / 原 ADR-077）无法支撑会话运维与远程 Streamable HTTP/SSE。  

**目标**：在 Control Plane **内嵌** MCP 网关（Streamable HTTP 主 + 兼容 SSE），鉴权与工具策略仍 **100% 走 FR-384 Agent Token**；会话内存可运维。

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
- tool 调用内部 **只调 CP 本地 service / 统一 action 授权器**，不二次实现 scope/写白名单/capability
- **会话运维（内存）**：
  - 字段：sessionId、tokenId、tokenName、tokenPrefix、clientIP、transport（`streamable_http`|`sse`）、connectedAt、lastActivityAt、lastTool、idleTimeout、absoluteTimeout
  - 列表 / 按 id 踢线（管理员 JWT）
  - **空闲超时**、**绝对超时**（默认值可配置，建议空闲 30m、绝对 24h；写进 yml/env）
  - **并发上限**：全局 + 每 Token（默认建议全局 32、每 Token 4；超限拒绝新会话，中文原因）
- 管理员 API（JWT + 平台管理员）：
  - `GET /api/v1/agent/mcp/sessions`
  - `DELETE /api/v1/agent/mcp/sessions/:id`（踢线）
- 连接建立/断开可写审计或调用流水（与 FR-390 协调：优先由 390 统一记 `mcp.session.open/close` 类 action；本 FR 至少保证踢线有审计）
- 改写 **ADR-077**：从「stdio 独立 bridge」改为「CP 内嵌 MCP 网关」；注明 FR-386 废弃

### 不做

- 不保留 `apps/mcp-bridge` stdio（删除属 **FR-392**；本 FR 实现期可先并存但不得作为推荐路径）
- 不做 mTLS、写操作面板审批、多 CP 进程会话共享 / sticky 以外的 HA
- 不在 MCP 层再造策略（禁止本地 allowlist）
- 不做统计大盘（FR-391/后续）

## 3. 设计（怎么做）

### 3.1 模块

- `internal/controlplane/mcp/`（或 `service/mcp_session.go` + `router/mcp.go`）：会话表（内存 `sync.Map`/`map+mutex`）、超时清理 goroutine、传输适配
- `router`：注册 MCP 传输路由 + 管理员 sessions API
- 工具调用：复用 `AgentOpsHandler` / `AgentTokenService.CanDiscover|Authorize` + Instance/Node service，**禁止**复制策略分支；MCP 仅持 ToolSpec（name/schema/action/执行器）

### 3.2 会话生命周期

```
initialize(auth) → Authenticate → 检查并发 → 创建 session → tools/list|call
  → 每次 call 刷新 lastActivityAt
  → idle/absolute 到期或 DELETE session → 取消 context → 连接关闭
```

踢线：管理员 DELETE → 取消 session context → 进行中 tool call 失败并返回可识别错误。

### 3.3 传输

- **Streamable HTTP** 为主路径（MCP 现行远程推荐）
- **SSE** 兼容旧客户端；两传输共享同一会话登记与鉴权
- 依赖：若引入官方/社区 MCP Go SDK，**须在实现前征得用户确认**（全局依赖管理规则）；无合适依赖时可用最小 JSON-RPC over HTTP 自实现 MVP（spec 允许，须单测对齐工具契约）

### 3.4 安全

- 仅 Agent Token；人类 JWT **不能**充当 MCP 会话凭证
- 不在日志打印明文 Token
- 超限 / 鉴权失败返回稳定错误码与中文 message（与 FR-388 契约可对齐）

架构决策见 **ADR-077（改写）**。

## 4. 任务拆分

- [x] 改写 ADR-077 + 本 spec 状态开发中
- [x] 会话管理器：创建/踢线/超时/并发 + 单测
- [x] Streamable HTTP MCP 端点 + tools 映射 + 单测（mock service）
- [x] SSE 兼容端点 + 单测（路由已挂；传输与 streamable 共享会话表）
- [x] 管理员 sessions list/kick API + 单测
- [x] 接入 router / 配置项（超时、并发）
- [x] 文档：API.md、ARCHITECTURE 一节、PRD FR-389→开发中
- [ ] **真机**：远程持 Token 完成 initialize + tools/list + 一次 tool call；踢线后失败

## 5. 验收标准

1. 有效 Token：Streamable HTTP 与 SSE 均可建立会话；`tools/list` 按当前 Token 能力与可用 scope 动态返回工具（空能力 V2 仅 whoami）  
2. 无效/吊销/过期 Token：无法建立会话（401/等价）  
3. scope 外 tool / 能力不足 / 永久禁区：不注册或 call 返回 isError + 中文原因，且 **HTTP 层不得 5xx 当策略拒绝**；未列出的工具手工 call 仍须最终授权拒绝  
4. 会话列表可见 token 名/前缀、IP、时长、最近 tool、传输类型；踢线后客户端无法继续 call  
5. 空闲超时、绝对超时、全局/每 Token 并发上限行为符合配置；超限中文原因  
6. 单测：会话并发、超时、踢线、鉴权失败、动态 tools/list  
7. **真机**（需用户环境或既有 `103.45.143.199`）：HTTPS 远程走通至少一条 Streamable HTTP 或 SSE 全路径  

## 6. 风险 / 待定

- MCP Go SDK 选型与依赖审批  
- Gin 与长连接/流式响应的缓冲与超时中间件是否截断 SSE——实现时验证  
- CP 重启会话全丢：文档写明，可接受  
- 与 FR-390 的 action 命名对齐：`mcp.tool.<name>` vs 既有 `agent.instance_start`——**推荐 tool 内部仍记 Agent action 名**，另加 `client=mcp`（见 FR-390）

## 7. UI 契约摘录（供 FR-391）

- 平台管理 → **MCP 会话**：表格 + 踢线按钮；展示传输类型与超时说明  
- Token 创建成功弹窗 / 详情：复制 MCP 基址（如 `https://<cp>/mcp`）+ Token 环境变量说明  
