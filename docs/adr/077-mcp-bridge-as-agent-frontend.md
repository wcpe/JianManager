# ADR-077: CP 内嵌 MCP 网关（取代 stdio mcp-bridge）

- **日期**: 2026-07-23
- **状态**: accepted（随 FR-389 开发落地；supersedes 原「stdio 独立 bridge」决策）
- **上下文**: 远程 IDE / 运维需要可长连的 MCP，且会话可运维（可见、可踢、有超时）。首版独立进程 `apps/mcp-bridge`（FR-386 / 原 ADR-077：stdio 协议适配）无法支撑会话运维与远程 Streamable HTTP/SSE。策略仍须 100% 落在 CP（ADR-076）。

## 决策

1. **在 Control Plane 内嵌 MCP 网关**（模块 `internal/controlplane/mcp/`），暴露：
   - Streamable HTTP 主路径：`POST/GET /api/v1/mcp`
   - SSE 兼容：`GET /api/v1/mcp/sse` + `POST /api/v1/mcp/message`
2. **鉴权**：仅 Agent Token（`jmat_`）；复用 `middleware.AgentAuth` / `AgentTokenService.Authenticate`。人类 JWT **不得**充当 MCP 会话凭证。
3. **协议适配 only**：工具调用内部只调既有 service / 统一 action 授权器，**禁止**在 MCP 层复制 scope / 写白名单 / capability / 硬拒绝策略。
4. **工具集**由 service action 目录投影；`tools/list` 按 Token 能力与可用 scope 动态裁剪（ADR-080），永久禁区与未登记 action **不注册**为 tool。
5. **会话内存可运维**：sessionId、token 元数据、IP、传输类型、连接/活动时间、最近 tool、空闲/绝对超时；全局与每 Token 并发上限；管理员 JWT API 列表 / 踢线。
6. **FR-386 stdio mcp-bridge**：标记废弃；删除 `apps/mcp-bridge` 属 **FR-392**（本 ADR 不强制本批删仓，但不得再作为推荐接入路径）。

## 理由

- 会话列表/踢线/超时需要与 CP 进程同址的内存态与管理员面；独立 stdio 进程无法自然提供。
- Streamable HTTP/SSE 是 MCP 远程推荐传输；CP 已有 HTTPS + Agent Token，无需新增常驻监听二进制。
- 策略仍唯一落在 CP（ADR-076），避免 curl 与 MCP 分叉。

## 后果

- 新增配置项：空闲/绝对超时、全局/每 Token 并发（默认 30m / 24h / 32 / 4）。
- CP 重启会话全丢：文档写明，可接受（单进程内存，HA 粘滞不在本期）。
- FR-386 文档与发布物路径由 FR-392 清理；FR-388 矩阵入口改为 CP-MCP。
- 依赖：若引入官方 MCP Go SDK 须单独审批；MVP 允许最小 JSON-RPC over HTTP 自实现。

## 关系

- ADR-076（策略真源）、FR-389（本决策落地）、FR-390（调用流水可选）、FR-392（删除 mcp-bridge）、FR-386（废弃）。
- **修订于 ADR-080**：工具发现改为按 capability/scope 动态裁剪；MCP 仅保留 ToolSpec 协议适配，能力与 scope 规则仍只落在 CP service action 目录。
