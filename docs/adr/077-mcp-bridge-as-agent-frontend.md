# ADR-077: mcp-bridge 作为 Agent 协议前端

- **日期**: 2026-07-23
- **状态**: proposed（随 FR-386 落地转 accepted）
- **上下文**: IDE agent 需要 MCP；策略必须在 CP（ADR-076），且保持「单二进制起步」心智、不增常驻监听面。

## 决策

1. **独立进程** `apps/mcp-bridge`（Go），默认 **stdio** MCP。
2. 仅作 **protocol adapter**：持 Token 调 CP Agent API，不二次实现 scope/写白名单。
3. **不**默认暴露 SSE/HTTP MCP 端口；远程场景由 IDE 配置经 HTTPS 访问 CP。
4. 工具集与 `jm-agent` 子集对齐；硬拒绝操作不注册为 tool。

## 理由

- 与 CP 解耦便于单独升级 MCP SDK。
- stdio 符合本机 IDE 接入习惯；无新增网络攻击面。

## 后果

- 新发布物 mcp-bridge；依赖 FR-384 API 与 ADR-076。
- 若未来要远程 MCP 端口，须另开 ADR 评估暴露面。

## 关系

- ADR-076、FR-386、FR-385（工具集对齐）。
