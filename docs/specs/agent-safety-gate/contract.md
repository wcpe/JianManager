# Agent 入口策略契约表（FR-388 / FR-395）

> 真源：`service.AgentOpsContract()` / `service.AgentActionCatalog()` / `service.AgentHardDenyList()`。  
> 三入口：`curl`（本表 HTTP）↔ `jmagent` CLI ↔ CP 内嵌 MCP（Streamable HTTP/SSE，FR-389 / FR-395）。

## 1. 运维面 action（默认可对 agent 暴露）

| Action | Kind | V2 capability | V1 兼容 | Scope | HTTP | Path |
|---|---|---|---|---|---|---|
| agent.whoami | read | — | 有效 Token | none | 200/401 | GET /api/v1/agent/whoami |
| agent.list_nodes | read | node.read | 节点 scope 非空 | node | 200/403 | GET /api/v1/agent/nodes |
| agent.list_instances | read | instance.read | 实例 scope 非空 | instance（V2：显式 ∪ 节点继承） | 200/403 | GET /api/v1/agent/instances |
| agent.get_instance | read | instance.read | 显式实例 scope | instance | 200/403 | GET /api/v1/agent/instances/:id |
| agent.get_instance_metrics | read | observability.read | 显式实例 scope | instance | 200/403 | GET /api/v1/agent/instances/:id/metrics |
| agent.instance_start | write | instance.life | writeAllow `instance.life` | instance | 200/403 | POST .../start |
| agent.instance_stop | write | instance.life | writeAllow `instance.life` | instance | 200/403 | POST .../stop |
| agent.instance_restart | write | instance.life | writeAllow `instance.life` | instance | 200/403 | POST .../restart |
| agent.node_maintenance_enter | write | node.operate | writeAllow `node.maintenance` | node | 200/403 | POST .../maintenance/enter |
| agent.node_maintenance_leave | write | node.operate | writeAllow `node.maintenance` | node | 200/403 | POST .../maintenance/leave |

策略拒绝统一 **403** `{"error":"FORBIDDEN",...}`；Token 无效/吊销/过期 **401**。  
V1 不启用节点→实例继承；V2 节点 scope 覆盖该节点当前与未来实例。scope 外与不存在对 Agent 收敛为同一 403。

## 2. MCP 工具名对齐

| MCP tool | Action |
|---|---|
| agent_whoami | agent.whoami |
| agent_list_nodes | agent.list_nodes |
| agent_list_instances | agent.list_instances |
| agent_get_instance | agent.get_instance |
| agent_get_instance_metrics | agent.get_instance_metrics |
| agent_get_instance_logs | agent.get_instance_logs（仅 MCP；需 observability.read） |
| instance_start | agent.instance_start |
| instance_stop | agent.instance_stop |
| instance_restart | agent.instance_restart |
| node_maintenance_enter | agent.node_maintenance_enter |
| node_maintenance_leave | agent.node_maintenance_leave |

- `tools/list` 按 Token 能力与潜在 scope **动态裁剪**；空能力 V2 仅见 `agent_whoami`。  
- `tools/call` 始终最终授权；永久禁区与未登记 action **永不**注册为 MCP tool。  
- 策略拒绝：HTTP 200 + `result.isError=true` + 中文原因。

## 3. CLI 子命令对齐

| jmagent | Path |
|---|---|
| whoami | GET /api/v1/agent/whoami |
| list nodes | GET /api/v1/agent/nodes |
| list instances | GET /api/v1/agent/instances |
| instance status \| metrics | GET instances/:id[ /metrics] |
| instance start\|stop\|restart | POST ... |
| node maintenance enter\|leave | POST ... |

## 4. 永久禁区与默认拒绝

**永久禁区**（永不进入 action 目录 / MCP tool）：用户/组/RBAC、Agent Token 管理、密钥或准入凭据明文、数据库浏览、自更新、平台设置、审计/调用流水删除。

**未登记默认拒绝**（FR-395 不自动开放；后续 FR 须以强类型 action + destructive 能力 + 精确确认登记）：`instance.delete`、`instance.kill`、`node.delete` 及制品/内容破坏性操作等。

兼容枚举接口：`AgentHardDenyList()` / `IsAgentHardDeny()` 仍供 FR-388 矩阵测试使用。

## 5. 管理员 Token API（JWT，非 agent 运维面）

| Method | Path | 权限 |
|---|---|---|
| POST | /api/v1/agent/tokens | 平台管理员 |
| GET | /api/v1/agent/tokens | 平台管理员 |
| DELETE | /api/v1/agent/tokens/:id | 平台管理员 |

明文 Token 仅创建响应 `plaintext` 字段返回一次。  
响应新增 `policyVersion`、`capabilities`（数组）；旧字段 `writeAllowlist` 保留。

## 6. 可证门禁

```bash
go test ./internal/controlplane/router/ -run 'AgentGate|AgentCapability|MCP' -count=1
go test ./internal/controlplane/service/ -run 'AgentToken|AgentCapability|AgentScope|AgentCallLog' -count=1
go test ./internal/controlplane/mcp/ -count=1
go test ./apps/jmagent/... -count=1
```

CI job：`agent-gate`（见 `.github/workflows/ci.yml`）。
