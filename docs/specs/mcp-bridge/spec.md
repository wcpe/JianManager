# 功能规格：mcp-bridge 独立进程

> 状态：开发中　·　关联 PRD：FR-386　·　依赖：FR-384　·　ADR：077

## 1. 背景与目标

为 Cursor / Claude Code 等 IDE agent 提供 MCP 入口：stdio 默认，远端经 HTTPS 连 CP。  
**bridge 不持有策略**——只做 protocol adapter。

## 2. 需求（要什么）

### 范围内
- 二进制 `apps/mcp-bridge/`
- 默认 **stdio** MCP server；Token/`cp-url` 经 env 或启动参数
- 工具集（与 jm-agent 对齐）：
  - 读：`agent_whoami`、`agent_list_nodes`、`agent_list_instances`、`agent_get_instance`、`agent_get_instance_metrics`、`agent_get_instance_logs`
  - 写：`instance_start`、`instance_stop`、`instance_restart`、`node_maintenance_enter`、`node_maintenance_leave`
- 硬拒绝面的操作**不注册为 tool**
- CP 403 → MCP `isError=true` + 中文原因

### 不做
- 不暴露独立 SSE/HTTP MCP 监听端口（本期）
- 不二次实现 scope/写白名单
- 不链 gRPC/DB/Worker

## 3. 设计（怎么做）

- MCP Go SDK（或等价）+ 与 jm-agent 共用的 HTTP 客户端包（可抽 `internal/agentclient`）
- 见 **ADR-077**

## 4. 任务拆分

- [x] 脚手架 + stdio MCP 循环
- [x] tools/list 与 tools/call 映射
- [x] 错误结构
- [x] 单测（mock CP）
- [ ] 构建目标 + README Agent 接入节草稿（发版前补）

## 5. 验收标准

1. `tools/list` 仅含约定工具
2. 同 Token：CLI / MCP / curl 行为一致
3. stdio 模式无额外监听端口
4. 依赖闭包轻量

## 6. 风险 / 待定

- MCP SDK 选型需确认不引入过重新依赖；若需新依赖先征得用户确认
