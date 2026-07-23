# 功能规格：Agent 入口可证安全闸

> 状态：开发中　·　关联 PRD：FR-388　·　依赖：FR-384/385/386　·　契约：`contract.md`

## 1. 背景与目标

把「默认只读 + 写白名单 + scope + 硬拒绝」做成**可证**：三入口同策略、CI 闸、自动化矩阵。

## 2. 需求（要什么）

### 范围内
- 契约表：每个 action → 默认策略 / 写白名单 / 硬拒绝 / scope / 错误码（见 `contract.md` + `service.AgentOpsContract`）
- 三入口同策略：CLI ↔ MCP ↔ curl（共用 CP Agent API）
- 自动化断言规模 ≥30（scope 外、吊销、过期、写白名单外、硬拒绝、空 scope、并发 403、契约矩阵）
- CI job `agent-gate`
- 契约入 `docs/API.md`

### 不做
- 不替代 FR-384 实现策略
- 完整 docker-compose 真机 30 行手跑矩阵可后续补证据到 `.tmp/`（不入库）

## 3. 设计

- 策略真源：`ResolveAction` + 硬拒绝 map
- HTTP 闸：`router/agent_gate_test.go`（`TestAgentGate_*`）
- 入口：`apps/jmagent` / `apps/mcp-bridge` 单测 mock 403

## 4. 任务拆分

- [x] 契约表文档 + 代码导出 `AgentOpsContract` / `AgentHardDenyList`
- [x] 集成/API 测试 ≥30 规模
- [x] CI `agent-gate` job
- [x] API.md Agent 专节
- [ ] 可选：真机 docker-compose 证据（`.tmp/acceptance-agent-gate-*.md`）

## 5. 验收标准

1. `go test ... -run AgentGate` 全绿
2. 契约与运维路由/MCP 工具集一一对应
3. CI job 可拦截策略回归
4. API.md 含 Agent 端点与 401/403 语义

## 6. 风险

- 真机全链路仍依赖 Worker/实例；本 FR 以 CP 策略可证为主，真机证据可选
