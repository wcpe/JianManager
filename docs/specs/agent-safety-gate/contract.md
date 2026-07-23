# Agent 入口策略契约表（FR-388）

> 真源：`service.AgentOpsContract()` / `service.AgentHardDenyList()`。  
> 三入口：`curl`（本表 HTTP）↔ `jmagent` CLI ↔ `mcp-bridge` tools。

## 1. 运维面 action（默认可对 agent 暴露）

| Action | Kind | Write allow | Scope | HTTP | Path |
|---|---|---|---|---|---|
| agent.whoami | read | — | none | 200/401 | GET /api/v1/agent/whoami |
| agent.list_nodes | read | — | node 非空 | 200/403 | GET /api/v1/agent/nodes |
| agent.list_instances | read | — | instance 非空 | 200/403 | GET /api/v1/agent/instances |
| agent.get_instance | read | — | instance ID | 200/403 | GET /api/v1/agent/instances/:id |
| agent.get_instance_metrics | read | — | instance ID | 200/403 | GET /api/v1/agent/instances/:id/metrics |
| agent.instance_start | write | instance.life | instance ID | 200/403 | POST .../start |
| agent.instance_stop | write | instance.life | instance ID | 200/403 | POST .../stop |
| agent.instance_restart | write | instance.life | instance ID | 200/403 | POST .../restart |
| agent.node_maintenance_enter | write | node.maintenance | node ID | 200/403 | POST .../maintenance/enter |
| agent.node_maintenance_leave | write | node.maintenance | node ID | 200/403 | POST .../maintenance/leave |

策略拒绝统一 **403** `{"error":"FORBIDDEN",...}`；Token 无效/吊销/过期 **401**。

## 2. MCP 工具名对齐

| MCP tool | Action |
|---|---|
| agent_whoami | agent.whoami |
| agent_list_nodes | agent.list_nodes |
| agent_list_instances | agent.list_instances |
| agent_get_instance | agent.get_instance |
| agent_get_instance_metrics | agent.get_instance_metrics |
| instance_start | agent.instance_start |
| instance_stop | agent.instance_stop |
| instance_restart | agent.instance_restart |
| node_maintenance_enter | agent.node_maintenance_enter |
| node_maintenance_leave | agent.node_maintenance_leave |

硬拒绝 action **永不**注册为 MCP tool。

## 3. CLI 子命令对齐

| jmagent | Path |
|---|---|
| whoami | GET /api/v1/agent/whoami |
| list nodes | GET /api/v1/agent/nodes |
| list instances | GET /api/v1/agent/instances |
| instance status \| metrics | GET instances/:id[ /metrics] |
| instance start\|stop\|restart | POST ... |
| node maintenance enter\|leave | POST ... |

## 4. 硬拒绝面（永不 allow）

含且不限于：`user.write` / `user.create|update|delete`、`group.*`、`platform.settings` / `settings.write`、`db.browse` / `db.query`、`selfupdate` / `system.update`、`instance.delete`、`instance.kill`、`node.delete`、`audit.delete`。

## 5. 管理员 Token API（JWT，非 agent 运维面）

| Method | Path | 权限 |
|---|---|---|
| POST | /api/v1/agent/tokens | 平台管理员 |
| GET | /api/v1/agent/tokens | 平台管理员 |
| DELETE | /api/v1/agent/tokens/:id | 平台管理员 |

明文 Token 仅创建响应 `plaintext` 字段返回一次。

## 6. 可证门禁

```bash
go test ./internal/controlplane/router/ -run AgentGate -count=1
go test ./internal/controlplane/service/ -run AgentToken -count=1
go test ./apps/jmagent/... ./apps/mcp-bridge/... -count=1
```

CI job：`agent-gate`（见 `.github/workflows/ci.yml`）。
