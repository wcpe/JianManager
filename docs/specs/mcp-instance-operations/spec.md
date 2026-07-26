# 功能规格：MCP 节点与实例全生命周期强类型工具

> 状态：草拟　·　关联 PRD：FR-396　·　优先级：P0　·　依赖：FR-395（已实现）　·　关联 ADR：080（能力策略真源）、039（破坏性确认先例）　·　分支：feature/fr-396-mcp-instance-ops

## 1. 背景与目标

FR-395 已建立 Agent 能力策略 v2：统一 action 目录（`internal/controlplane/service/agent_capability.go`）、`CanDiscover`/`Authorize` 两级授权、节点 scope 单向继承实例、MCP `tools/list` 动态裁剪与 `tools/call` 最终授权。但 MCP 现在只有 11 个工具（whoami、节点/实例只读、start/stop/restart、维护进出）。

FR-396 在 FR-395 策略闸内把节点与实例的全生命周期开放为强类型 MCP 工具，使 scoped Agent 能独立完成「选节点 → 建实例/搭建/导入/克隆 → 配置 → 启动 → 观测 → 命令 → 批量 → 强杀/删除」闭环，为 FR-398/399 的 500 Bot 容量战役提供实例准备能力。

原则：

1. MCP 工具直接复用 CP service（`InstanceService`/`NodeService`/`ProvisionService` 等），不经本机 HTTP 回环。
2. 一切授权只经 action 目录（`CanDiscover`/`Authorize`），MCP 不自带策略。
3. 既有状态机、启动预检、内存水位闸、在途任务闸（`acquireInstanceOperation`）原样生效，不为 Agent 开旁路。
4. 破坏性操作（强杀、删除、归档清理）要求独立 destructive 能力 + 服务端精确确认参数。

## 2. 需求（要什么）

### 2.1 新增 action 与工具清单

所有 action 登记入 `agentActionCatalog`，`V1Allowed=false`（V1 Token 永不获得，未知 action 默认拒绝路径已有）。表中「确认」列指服务端精确确认参数（见 2.3）。

#### 节点（resource=node）

| MCP 工具 | action | capability | 操作 | 复用 service | 确认 |
|---|---|---|---|---|---|
| `node_get` | `agent.node_get` | `node.read` | read | `NodeService.GetByID` | — |
| `node_get_metrics` | `agent.node_get_metrics` | `observability.read` | read | `NodeService.GetMetrics` | — |
| `node_check_docker` | `agent.node_check_docker` | `node.read` | read | `DockerImageService.CheckDocker` | — |
| `node_drain` | `agent.node_drain` | `node.operate` | write | `NodeService.Drain` | — |
| `node_list_archived` | `agent.node_list_archived` | `node.read` | read | `NodeService.ListArchived`（过滤到 scope 内节点） | — |
| `node_purge_archived` | `agent.node_purge_archived` | `node.destructive` | destructive | `NodeService.Purge` | `confirmNodeName` |

维护进出已在 FR-395 交付，不重复。节点删除（在线下线 `NodeService.Delete`）**不开放**——比归档清理影响面更大且涉及在线节点，本 FR 保持未知 action 默认拒绝；节点 enrollment token 签发/明文准入凭据**永久不注册**（明文禁区）。

#### 实例只读（resource=instance）

| MCP 工具 | action | capability | 复用 service |
|---|---|---|---|
| `instance_search` | `agent.instance_search` | `instance.read` | `AgentTokenService.ListAccessibleInstances` + 关键字/状态过滤（分页，pageSize≤100） |
| `instance_get_env` | `agent.instance_get_env` | `instance.read` | `InstanceService.GetInstanceEnv` |
| `instance_list_crash_snapshots` | `agent.instance_list_crash_snapshots` | `observability.read` | `CrashSnapshotService.ListByInstance` |

既有 `agent_get_instance`/`agent_get_instance_metrics`/`agent_get_instance_logs` 不动。

#### 实例创建与搭建（resource=instance，目标节点须在 scope）

| MCP 工具 | action | capability | 复用 service | 说明 |
|---|---|---|---|---|
| `instance_create` | `agent.instance_create` | `instance.provision` | `InstanceService.Create` | 通用创建（自带 jar/docker 场景） |
| `instance_provision_server` | `agent.instance_provision_server` | `instance.provision` | `ProvisionService.ProvisionServerAsync` | 一键搭建后端子服；返回 taskId |
| `instance_import_inspect` | `agent.instance_import_inspect` | `instance.provision` | `ImportServerService.Inspect` | 导入前探测目录 |
| `instance_import` | `agent.instance_import` | `instance.provision` | `ImportServerService.Import` | 导入现成服务器 |
| `instance_clone` | `agent.instance_clone` | `instance.provision` | `CloneService.Clone` | 源实例须在 scope；支持 dryRun |
| `instance_rebuild` | `agent.instance_rebuild` | `instance.provision` | `ProvisionService` Rebuild 链路 | 损坏重建 |
| `instance_update_config` | `agent.instance_update_config` | `instance.configure` | `InstanceService.Update` | 结构化字段更新（内存/JVM/自启等） |
| `task_get` | `agent.task_get` | `instance.read` | `TaskService` 按 taskId 查询 | 异步搭建/克隆任务进度跟踪 |

创建类工具的目标授权语义：请求里的 `nodeId`（create/provision/import）经 `Authorize(p, action, target{NodeID})` 校验节点 scope（含 V2 实例能力 + 节点 scope 组合，规则复用 `principalCanAccessInstance` 的节点分支——即节点在 scope 内才可在其上创建）；clone/rebuild 的源实例走实例授权，目标节点继承源实例归属。`task_get` 只允许查询本 Token 发起的任务（按 taskId 关联的实例归属重验 scope）。

#### 实例生命周期扩展与命令（resource=instance）

| MCP 工具 | action | capability | 操作 | 复用 service | 确认 |
|---|---|---|---|---|---|
| `instance_send_command` | `agent.instance_send_command` | `instance.command` | write | `InstanceService.SendCommand` | — |
| `instance_batch` | `agent.instance_batch` | `instance.life` | write | `InstanceBatchService.Batch`（scope=可访问实例集合） | — |
| `instance_kill` | `agent.instance_kill` | `instance.destructive` | destructive | `InstanceService.Kill` | `confirmInstanceName` |
| `instance_delete` | `agent.instance_delete` | `instance.destructive` | destructive | `InstanceService.Delete` | `confirmInstanceName` |

`instance_batch` 仅允许 start/stop/restart 三种 op（不含 kill/delete——破坏性不进批量），目标列表逐一过 scope，越界目标整体拒绝（不部分执行，避免静默缩范围）。

### 2.2 单实例写操作归属重验

FR-395 已给 start/stop/restart 建立 `*ForExpectedNode` 模式。本 FR 对新增写操作沿用相同语义：

- `instance_send_command`、`instance_kill`、`instance_delete`：授权后以 `AuthorizeInstanceAction` 返回的实例快照 `NodeID` 为 expected node；service 层派发前在操作锁内重验归属（复用/扩展 `startInternal` 的模式，为 `Kill`/`Delete`/`SendCommand` 增加 expectedNode 变体或等价校验）。
- 归属并发变化 → 拒绝并返回中文错误，不用陈旧归属派发。

### 2.3 破坏性确认协议（本 FR 定义并实现）

ADR-080 预留了「精确确认」占位；本 FR 落地为**服务端精确确认参数**：

- destructive 工具的 InputSchema 含必填确认字段：`instance_kill`/`instance_delete` 要求 `confirmInstanceName`，`node_purge_archived` 要求 `confirmNodeName`。
- 服务端将确认值与 CP 数据库中目标当前名称做精确比对（区分大小写，不 trim 后模糊匹配）；不匹配 → `isError=true` + 中文提示「确认名称与目标不符」，且不进入 service 写路径。
- 确认失败同样写调用流水（失败原因中文）。
- 确认参数由服务端校验，MCP 客户端无法用 ID 复述绕过（防 LLM 幻觉误删）。

### 2.4 流水与审计

- 所有新 action 经既有 MCP `CallTool` 流水路径记录 capability、action、targetType/targetId、成功/失败与中文原因（FR-390 机制，不新建）。
- 复用的 service 内部已有的审计（如节点 purge、实例删除的 audit log）保持原样——MCP 调用以 Agent Token 身份记录（沿用 FR-389 的 actor 语义）。

### 2.5 范围外

- 文件/配置/插件工具（FR-397）、Bot/压测工具（FR-398）。
- 节点在线删除、节点 enrollment token 签发、明文准入凭据（不注册）。
- 代理搭建（ProvisionProxy）、端口目录、代理注册管理——500 Bot 战役不需要，YAGNI。
- 崩溃快照正文下载（列表即可，正文属文件域）。
- destructive 确认协议不做多步挑战-应答（单参数精确确认已满足防误删）。

## 3. 设计（怎么做）

### 3.1 action 目录扩展

`agent_capability.go` 的 `agentActionCatalog` 增加 §2.1 全部 action：

- 新 action 一律 `V1Allowed: false`、无 `V1WriteAllow`、`HTTPInContract: false`（MCP 专属，不投影 Agent HTTP 契约，避免本 FR 扩 HTTP 面）。
- `Operation` 按表：read/write/destructive。
- 新增 `RequiresConfirm bool` 描述字段（destructive 工具置 true），供 MCP 层统一执行确认校验，不逐工具手写。

### 3.2 MCP 工具拆分（防上帝文件）

`tools.go` 已 500+ 行，继续堆会成上帝文件。拆分：

```text
internal/controlplane/mcp/
  tools.go          # ToolDef/ToolResult/ToolDeps/注册表聚合 + CallTool 分发骨架（保留）
  tools_node.go     # 节点域 toolSpec 声明 + 执行器
  tools_instance.go # 实例生命周期/命令/批量/破坏性 toolSpec + 执行器
  tools_provision.go# 创建/搭建/导入/克隆/重建/配置更新 + task_get
```

- `toolSpec` 增加 `Exec func(ctx, ToolDeps, *AgentPrincipal, args) ToolResult` 字段，`CallTool` 的巨型 switch 改为查注册表分发（既有 11 个工具迁移到新形态，行为不变）。
- `ToolDeps` 增加：`Provision *ProvisionService`、`Import *ImportServerService`、`Clone *CloneService`、`Batch *InstanceBatchService`、`Docker *DockerImageService`、`Crash *CrashSnapshotService`、`Task *TaskService`；nil 时对应工具返回中文「服务不可用」（既有模式）。
- 破坏性确认：`CallTool` 骨架在 `Authorize` 通过后、执行器之前统一执行 `RequiresConfirm` 校验（从 args 取确认字段与可信目标名称比对）。

### 3.3 service 层改动（最小）

- `InstanceService`：为 `Kill`/`Delete`/`SendCommand` 增加 expectedNode 校验变体（模式照抄 `startInternal` 的 `expectedNodeID` 参数），原方法保持签名不变（管理面走 0=不校验）。
- `InstanceBatchService.Batch` 已支持 `scopeIDs []uint, scope bool`——直接以 `ListAccessibleInstances` 结果为 scope 传入，越界目标在 resolve 阶段即拒绝。
- `NodeService`/`ProvisionService`/`CloneService`/`ImportServerService`/`DockerImageService`/`CrashSnapshotService`：无签名改动，直接复用。
- main.go 装配：把新依赖注入 MCP `ToolDeps`。

### 3.4 错误与收敛语义

- scope 外与不存在统一 `ErrAgentForbidden` → `isError=true` 中文「无权限或目标不存在」（沿用 FR-395 收敛语义，不泄露存在性）。
- service 业务错误（状态机拒绝、内存闸、预检失败）原样转中文文本返回，`isError=true`。
- 异步任务（搭建/克隆）返回 `taskId`，Agent 用 `task_get` 轮询；不在 MCP 内阻塞等待。

## 4. 任务拆分

- [ ] 失败测试先行：action 目录新条目（V1 不可见/V2 能力矩阵/未知拒绝）、destructive 确认缺失或不匹配拒绝、批量越界整体拒绝、命令/强杀/删除归属重验。
- [ ] `agent_capability.go`：登记新 action + `RequiresConfirm` 字段。
- [ ] `InstanceService`：Kill/Delete/SendCommand expectedNode 变体 + 单测。
- [ ] MCP `toolSpec.Exec` 注册表重构（既有 11 工具迁移，行为不变，测试全绿）。
- [ ] `tools_node.go`/`tools_instance.go`/`tools_provision.go` 实现 + InputSchema。
- [ ] main.go 装配 ToolDeps 新依赖。
- [ ] MCP 契约测试：每个新工具 tools/list 可见性矩阵 + tools/call 授权/确认/成功路径。
- [ ] 文档同步：PRD 状态、ARCHITECTURE（MCP 工具清单）、API（如涉及）、`docs/specs/cp-mcp-server/spec.md` 工具表、CHANGELOG 末尾追加。
- [ ] 真机链路：本机 CP 签发 V2 Token（instance.provision+life+command+destructive+node.read+observability.read，节点 scope），完成「搭建→配置→启动→命令→日志→停止→删除（精确确认）」闭环并保存证据到 `.tmp/acceptance-fr396-*.txt`。

## 5. 验收标准

1. **闭环**：scoped V2 Agent 仅经 MCP 即可从 scoped 节点搭建/导入实例，创建后继续配置、启动、发命令、观测、停止、删除。
2. **不经 HTTP 回环**：所有工具直接调 service（代码审查 + 无 localhost HTTP 调用）。
3. **闸门不可绕过**：状态机非法转换、启动预检失败、内存水位、在途任务闸在 MCP 路径拒绝行为与管理面一致（测试覆盖）。
4. **破坏性双闸**：kill/delete/purge 须 destructive 能力 + 精确确认参数；缺一即拒且写流水。
5. **scope 硬边界**：scope 外目标不进入 Worker RPC（测试断言 service 未被调用）；批量含越界目标整体拒绝。
6. **契约**：每个工具有严格 InputSchema、稳定中文错误；`tools/list` 按能力/scope 裁剪矩阵测试通过。
7. **流水**：每次写调用记录 capability/action/target/结果；确认失败也留痕。
8. **回归**：既有 11 工具行为与测试不变；`go test ./internal/controlplane/...` 全绿。
9. **真机**（需用户确认通过）：§4 最后一项闭环证据。

## 6. 风险 / 待定

- **工具数量膨胀**：注册表 + 按域拆文件；执行器只做参数解析+授权+调 service，禁止业务逻辑内聚到 MCP。
- **异步任务的 scope 泄露**：`task_get` 必须重验任务关联实例归属，防止跨 Token 窥探任务详情。
- **确认参数的名称漂移**：实例改名后确认值以当前名称为准（实时查库），文档写明。
- **InstanceBatch 部分成功语义**：本 FR 选择「越界整体拒绝」，与管理面「尽力而为」不同——差异写入工具描述，避免 Agent 误解。
