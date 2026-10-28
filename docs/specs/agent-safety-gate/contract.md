# Agent 入口策略契约表（FR-388 / FR-395 / FR-492）

> 真源：`service.AgentOpsContract()` / `service.AgentActionCatalog()` / `service.AgentHardDenyList()`。  
> 三入口：`curl`（本表 HTTP）↔ `jmagent` CLI ↔ CP 内嵌 MCP（Streamable HTTP/SSE，FR-389 / FR-395）。

## 0. 审批闸（FR-492：三入口同策略）

高危操作在真正执行前必须有一个**人类点过批准**。审批是独立于「能力/scope」的第二层闸：
能力判定回答「这个 Token 允不允许做」，审批回答「这件事要不要有人点头」。

**同策略的含义**：同一个 action 无论从哪条入口进来，都要过同一张登记表
（`service.approvalRequiredActions`，19 条），不会出现「MCP 要审、curl 不用审」的缺口。

| 入口 | 需审 action 的响应 | 零副作用 |
|---|---|---|
| MCP | HTTP 200 + `result.isError=false`，正文 `{status:"awaiting_approval", requestId, ticketId}` | 是 |
| curl | **202 Accepted** + `{status:"awaiting_approval", requestId, ticketId}` | 是 |
| CLI（`jmagent`） | 同 curl（CLI 只是 curl 的包装） | 是 |

- 出票后本次调用**不执行任何副作用**；批准后由执行器在服务端执行，调用方稍后重查目标状态确认结果。
  **注意**：`/approvals/own/*` 三端点挂在人类 JWT 会话组、按登录用户归属限域，
  MCP 工具面**没有**审批查询工具——Agent 目前无法自行轮询（见
  `docs/specs/approval-center/fr-490-493-approval.md` §8 未决 4）。
- 批准后由执行器在服务端执行；执行前重校验 payload 哈希与目标漂移（FR-491）。
- **审批服务未装配 → 失败关闭**：需审 action 一律拒绝（HTTP 503 `APPROVAL_UNAVAILABLE`、
  MCP `isError=true`）。放行一个该审的操作是安全事故，拒绝只是功能不可用。
- 先授权、再出票：越权请求**不**变成票据（否则批准人被安排成提权的最后一步）。
- action 名映射：HTTP/CLI 的 `agent.<name>` 去掉前缀后必须命中登记表才算需审
  （`service.AgentActionNeedsApproval`），不做模糊匹配。

**两条入口的实际交集只有 `instance_stop`**：agent 运维面 HTTP/CLI 只暴露 5 个写 action
（`instance_start` / `stop` / `restart`、`node_maintenance_enter` / `leave`），
其余 18 个需审 action 仅在 MCP 工具面可达（MCP 面见 §2）。

## 1. 运维面 action（默认可对 agent 暴露）

| Action | Kind | V2 capability | V1 兼容 | Scope | HTTP | Path |
|---|---|---|---|---|---|---|
| agent.whoami | read | — | 有效 Token | none | 200/401 | GET /api/v1/agent/whoami |
| agent.list_nodes | read | node.read | 节点 scope 非空 | node | 200/403 | GET /api/v1/agent/nodes |
| agent.list_instances | read | instance.read | 实例 scope 非空 | instance（V2：显式 ∪ 节点继承） | 200/403 | GET /api/v1/agent/instances |
| agent.get_instance | read | instance.read | 显式实例 scope | instance | 200/403 | GET /api/v1/agent/instances/:id |
| agent.get_instance_metrics | read | observability.read | 显式实例 scope | instance | 200/403 | GET /api/v1/agent/instances/:id/metrics |
| agent.instance_start | write | instance.life | writeAllow `instance.life` | instance | 200/403 | POST .../start |
| agent.instance_stop | write | instance.life | writeAllow `instance.life` | instance | **202**（需审）/403 | POST .../stop |
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

## 2.1 需审批的 MCP 工具（FR-492 登记表，共 19 条）

未在本表、且不属免审白名单的写类操作沿用既有「参数名精确确认」语义；
**新增写工具若名字含危险词根而漏登记，`mcp` 包的覆盖门禁会失败**（不是运行期静默放行）。

| MCP tool | 为什么必须审 |
|---|---|
| file_issue_transfer_ticket | 签票后走流式上传即可写盘，是绕过 `file_write_text` 的完整路径 |
| instance_send_command | 控制台命令等价任意代码执行 |
| instance_update_config | 改 `startCommand` 即改可执行命令 |
| instance_binary_upgrade / instance_binary_rollback | 换掉要执行的二进制 |
| instance_provision_server | 支持远程 URL 二进制来源，等价下载并执行任意二进制 |
| instance_snapshot_rollback | 覆盖当前工作目录 |
| instance_stop | 停服会赶走在线玩家（`start` / `restart` 免审） |
| instance_delete | 不可逆，连带工作目录与文件 |
| node_purge_archived | 永久删除已归档节点，不可恢复 |
| file_write_text / file_delete / file_rollback | 直接改/删实例内文件并覆盖内容 |
| config_write_text / config_rollback | 改配置即可改启动命令等执行上下文 |
| bot_send_command | Bot 在服务器内的行为，等价代为操作 |
| registration_delete | 把后端从代理摘掉，影响线上流量 |
| instance_group_remove_members / network_remove_member | 影响调度与可见范围 |

免审直执的写操作（PRD 明确要求）：`instance_start` / `instance_restart` / 配置热重载。
## 3. CLI 子命令对齐

| jmagent | Path |
|---|---|
| whoami | GET /api/v1/agent/whoami |
| list nodes | GET /api/v1/agent/nodes |
| list instances | GET /api/v1/agent/instances |
| instance status \| metrics | GET instances/:id[ /metrics] |
| instance start\|restart | POST ...（直执） |
| instance stop | POST ...（**需审批**，202 + requestId） |
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

# FR-492 审批闸：三入口同策略 + 出票零副作用 + 目标漂移 + 登记表覆盖
go test ./internal/controlplane/router/ -run 'AgentOps_' -count=1
go test ./internal/controlplane/mcp/ -run 'Approval' -count=1
go test ./internal/controlplane/service/ -run 'Approval|TargetDrift' -count=1
```

**登记表覆盖门禁**（`mcp` 包 `TestRegistryCoversAllDangerousTools`）：新增一个名字含
`delete` / `remove` / `purge` / `rollback` / `upgrade` / `provision` / `send_command` / `transfer`
等词根的写工具而未进审批名单，该用例即失败——把「漏登记」从运行期静默放行变成 CI 阶段的确定性失败。

CI job：`agent-gate`（见 `.github/workflows/ci.yml`）。
