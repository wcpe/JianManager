# 功能规格：Agent 能力策略 v2 与节点继承实例作用域

> 状态：开发中（spec 已审核）　·　关联 PRD：FR-395　·　优先级：P0　·　依赖：FR-384/389　·　下游：FR-396~399　·　关联 ADR：080

## 1. 背景与目标

FR-384/389 已建立专用 Agent Token、实例/节点 ID scope、`write_allowlist` 与 CP 内嵌 MCP，但现行策略只支持两个写权限，实例访问也只识别显式实例 ID。FR-396~399 要继续开放节点、实例、内容、Bot 与观测能力，因此必须先建立可扩展且默认拒绝的能力策略，同时保证既有 Token 升级后不扩大权限。

本 FR 的目标：

1. 用版本化能力分组替代 V1 的固定写白名单，Control Plane 仍是唯一策略真源。
2. 对 V2 Token 实现单向继承：节点 scope 自动覆盖该节点当前及未来实例。
3. 建立 action、capability、scope 类型与 MCP tool 的单一映射，未知项默认拒绝。
4. `tools/list` 按当前 Token 的能力和可用 scope 裁剪，`tools/call` 仍执行最终授权。
5. 调用流水记录本次授权能力、action 与目标。
6. 保持旧 Token、既有 Agent HTTP、jmagent 与 MCP 调用的权限不扩大和协议兼容。

## 2. 需求（要什么）

### 2.1 能力分组

V2 支持以下固定能力标识；签发时出现未知标识返回 `400 BAD_REQUEST`，运行时未知 action/capability 一律拒绝：

| 能力 | 用途 | FR-395 已映射行为 |
|---|---|---|
| `node.read` | 节点只读 | 节点列表 |
| `node.operate` | 节点非破坏性运维 | 维护模式进入/离开 |
| `node.destructive` | 节点破坏性运维 | 本 FR 仅定义能力，不新增工具 |
| `instance.read` | 实例元数据只读 | 实例列表、实例详情 |
| `instance.life` | 实例常规生命周期 | 启动、停止、重启 |
| `instance.command` | 实例命令执行 | 本 FR 仅定义能力，不新增工具 |
| `instance.provision` | 创建、搭建、导入、克隆、重建 | 本 FR 仅定义能力，不新增工具 |
| `instance.configure` | 实例结构化配置 | 本 FR 仅定义能力，不新增工具 |
| `instance.content` | 文件、配置文本、插件内容 | 本 FR 仅定义能力，不新增工具 |
| `instance.destructive` | 强杀、删除等破坏性实例操作 | 本 FR 仅定义能力，不新增工具 |
| `bot.read` | Bot 与压测只读 | 本 FR 仅定义能力，不新增工具 |
| `bot.manage` | 普通 Bot 管理 | 本 FR 仅定义能力，不新增工具 |
| `bot.load` | 压测模板与运行编排 | 本 FR 仅定义能力，不新增工具 |
| `observability.read` | 指标、日志、报告只读 | 实例指标、实例日志 |

`agent.whoami` 只要求 Token 有效，不要求业务能力。

能力分组是显式授权单元。后续 FR 向某一分组加入新 action 时，仅影响已明确持有该 V2 能力的 Token；V1 Token 不得因分组扩展获得新 action。

### 2.2 策略版本与旧 Token 无扩权

`agent_tokens` 新增：

- `policy_version`：整数，既有记录与旧请求为 `1`，V2 为 `2`。
- `capabilities`：JSON 字符串数组；仅 V2 使用，V1 保持空。

保留 `write_allowlist`、`scoped_instance_ids`、`scoped_node_ids` 字段，不改写既有记录。

签发请求兼容规则：

1. `policyVersion` 缺省或为 `1`：按 V1 处理；`capabilities` 不得提交；`writeAllowlist` 语义保持现状，包括缺省时默认 `instance.life` 与 `node.maintenance`。
2. `policyVersion=2`：必须提交 `capabilities`（允许显式空数组）；不得同时提交 `writeAllowlist`。
3. V2 服务端不隐式增加任何能力；管理 UI 可默认勾选只读能力 `node.read`、`instance.read`、`observability.read`，但最终以请求数组为准。
4. V1 Token 始终走兼容解释器：
   - `instance.life` 仅允许既有 start/stop/restart；
   - `node.maintenance` 仅允许既有 enter/leave；
   - 既有只读 action 与显式实例/节点 scope 行为保持不变；
   - 不启用节点 scope 继承实例；
   - 不获得 FR-395 后新增到任何 V2 能力组的 action。
5. Token 不提供在线编辑能力；能力或 scope 调整继续通过吊销并重新签发完成，因此既有 MCP 会话中的 principal 快照不会出现策略热更新漂移。

管理 API、Token 列表、HTTP `whoami` 与 MCP `agent_whoami` 新增返回：

- `policyVersion`
- `capabilities`（数组）

旧字段 `writeAllowlist` 继续返回，旧客户端可继续工作。

### 2.3 节点 scope 单向继承实例

仅 V2 Token 使用以下规则：

```text
可访问实例 = scopedInstanceIds 显式实例
          ∪ node_id 属于 scopedNodeIds 的当前实例
```

约束：

1. 节点 scope 覆盖该节点当前和未来创建的实例，无需把实例 ID 回填到 Token。
2. 实例移出授权节点后，下一次授权立即失效；实例移入授权节点后，下一次授权生效。
3. 继承只从节点指向实例；实例 scope 不反向授予节点读取或节点运维权限。
4. capability 与 scope 必须同时满足。例如持有 `instance.life` 但没有显式实例或节点 scope 时，不能启动任何实例。
5. 实例归属必须从 CP 可信数据读取，禁止接受客户端传入的 `nodeId` 作为授权依据。
6. 列表查询应使用集合查询实现显式实例与节点实例的并集，不在 handler/tool 中逐实例拼装授权规则。
7. 单实例写操作在派发前必须用当前实例归属重验；执行入口携带已验证的目标节点或等价版本条件，实例归属并发变化时拒绝本次操作，不允许用陈旧归属继续派发。
8. scope 外和不存在的资源保持收敛错误，不向 Agent 泄露资源是否存在。

### 2.4 统一 action 目录与授权器

在 `internal/controlplane/service` 建立唯一 action 目录。每个 action 描述至少包含：

- action 名；
- 所需 V2 capability；
- 资源类型：`none|node|instance|bot|...`；
- 操作类型：`read|write|destructive`；
- V1 兼容规则；
- 可选 HTTP 契约投影。

策略服务提供两级判断：

1. **发现判断**：判断 Token 是否具有 action 所需能力，以及是否至少存在对应类型的可用 scope；用于 HTTP 契约枚举和 MCP `tools/list`。
2. **执行判断**：在可信目标解析后校验 action、能力、scope、目标归属与必要的 destructive 条件；用于 HTTP 与 MCP 最终调用。

禁止：

- router、MCP、jmagent 各自维护 capability 规则；
- 按 action/tool 名字前缀猜测能力；
- 只依赖 `tools/list` 隐藏而跳过 `tools/call` 授权；
- 未登记 action 默认放行。

### 2.5 MCP 工具裁剪

MCP 保留协议层 `ToolSpec` 目录：工具名称、描述、JSON Schema、对应 action、目标参数提取器与执行器。capability 与 scope 规则从 service action 目录读取，不在 MCP 重复定义。

`tools/list`：

1. `agent_whoami` 对所有有效 Token 可见。
2. V2 Token 仅看到其 capability 允许且有潜在可用 scope 的工具。
3. V1 Token 仅看到兼容解释器当前确实可能允许的工具；不得因 V2 分组出现新工具。
4. 永久禁区工具不注册；其他未实现 action 也不注册。

`tools/call`：

1. 无论工具是否曾出现在 `tools/list`，调用时都重新执行 action 与目标授权。
2. 策略拒绝继续返回 HTTP 200 + MCP `isError=true` + 中文原因，不伪装为 5xx。
3. scope 外目标不得进入 Worker RPC 或业务 service 写路径。

### 2.6 调用流水

`agent_call_logs` 新增可空字段 `capability`，表示本次 action 实际使用的授权能力：

- V2：记录 action 目录中对应能力，例如 `instance.life`。
- V1 写操作：记录 `legacy.instance.life` 或 `legacy.node.maintenance`。
- V1 只读：记录 `legacy.read`。
- 会话建立/关闭等无业务能力事件留空。

既有 `action`、`targetType`、`targetId`、成功/失败、错误与耗时保持不变。历史记录 capability 为空属于合法兼容状态。

### 2.7 永久禁区与默认拒绝

以下能力不进入 V2 action 目录，也不注册 MCP tool：

- 用户、组与 RBAC 管理；
- Agent Token 管理；
- 密钥或准入凭据明文读取/签发；
- 数据库浏览或任意查询；
- 自更新；
- 平台设置。

审计/调用流水删除在 FR-395 中继续不提供 action/tool。原 ADR-076 中对实例删除、节点删除、强杀、制品管理的“永久硬拒绝”由 ADR-080 取代：这些操作不会在 FR-395 自动开放，仍因未知 action 默认拒绝；只有后续 FR 以强类型 action、独立 destructive capability、精确确认和服务端守卫明确登记后才可能开放。

### 2.8 范围外

- 不在本 FR 新增节点、实例、文件、插件、Bot 或压测业务工具；这些分别属于 FR-396~399。
- 不提供任意 `method/path/body` MCP 工具。
- 不新增 Token 在线编辑 API，不做能力审批流。
- 不复用管理员 JWT，不改变 jmctl 应急边界。
- 不让 MCP 承载大型文件。
- 不实现 destructive 操作的具体确认协议；本 FR 只提供能力与 action 元数据基础。

## 3. 设计（怎么做）

### 3.1 数据模型

`model.AgentToken` 增加：

```text
policy_version INTEGER NOT NULL DEFAULT 1
capabilities TEXT NULL
```

GORM `AutoMigrate` 增列。既有行因默认值保持 V1，无需批量把旧 allowlist 改写成能力数组。

`model.AgentCallLog` 增加：

```text
capability VARCHAR(64) NULL
```

历史行保持空。`AgentCallRecord` 与显式 `Select(...)` 同步加入字段。

### 3.2 服务职责

建议拆分为：

- `agent_capability.go`：能力常量、action descriptor、目录校验、V1/V2 能力解析。
- `agent_scope.go`：实例/节点 scope 判断与可访问实例集合查询。
- `agent_token.go`：Token 签发、认证、兼容字段解析；不继续扩大单文件中的策略 switch。

核心接口语义：

```text
DescribeAction(action) -> descriptor | unknown
CanDiscover(principal, action) -> allow/deny + resolved capability
Authorize(principal, action, trustedTarget) -> allow/deny + resolved capability
ListAccessibleInstances(principal, optionalNodeFilter) -> instances
```

`trustedTarget` 的实例归属由 CP service/数据库解析，不从 MCP/HTTP 参数接收。

### 3.3 HTTP 与 MCP 投影

- `AgentOpsContract()` 从 action 目录投影，保持既有 HTTP 路径与错误语义。
- `mcp.ToolSpec` 持有 action 引用，替代 `toolActionMap` 与 `toolTarget` 两套独立映射。
- `tools/list` 调用 `CanDiscover`；`CallTool` 调用 `Authorize`。
- jmagent 继续只调用 Agent HTTP API，不内置策略。

### 3.4 兼容与错误

- 无效、吊销、过期 Token：401。
- 未知 capability、V1/V2 字段混用：签发时 400。
- 能力不足、scope 外、未知 action：403；MCP 对应 `isError=true`。
- scope 外与目标不存在对 Agent 返回相同的收敛语义。
- Token API 原有字段不删除、不改名；新增字段为向后兼容扩展。

架构决策见 `docs/adr/080-agent-capability-policy-v2.md`。

## 4. 任务拆分

- [x] 审核并接受本 spec 与 ADR-080；将 ADR-076 标记为被取代，并在 ADR-077 补充动态工具裁剪约束。
- [x] PRD FR-395 状态切换为开发中。
- [x] 先补失败测试：V1 无扩权、V2 capability、未知默认拒绝、节点继承、移入/移出节点。
- [x] 新增 policy_version/capabilities 与调用流水 capability 字段、迁移兼容测试。
- [x] 实现 action 目录、V1 兼容解释器与 V2 授权器。
- [x] 实现动态实例 scope 查询与写操作归属重验。
- [x] 改造 Agent HTTP：签发、列表、whoami、实例列表/单实例授权。
- [x] 改造 MCP ToolSpec、按能力 `tools/list`、最终 `tools/call` 授权与流水。
- [x] 更新 Agent Token 管理 UI：V2 能力选择、节点继承说明、旧 Token 兼容展示。
- [x] 更新 AgentGate、MCP、调用流水与前端测试。
- [x] 文档同步：PRD、ARCHITECTURE、API、FR-388 契约、FR-389/390 specs、CHANGELOG。
- [x] 自动化验证与本机真链路：V1 Token、V2 Token、HTTP、MCP、吊销。

## 5. 验收标准

1. **旧 Token 不扩权**：升级前已存在的 V1 Token 仍只拥有原只读行为和原 `write_allowlist` 精确行为；节点 scope 不自动获得实例权限；后续加入 V2 分组的新 action 对其不可用。
2. **V2 能力生效**：签发时只能选择已知能力；空能力 Token 除 whoami 外无业务工具；未知 capability/action 默认拒绝。
3. **节点继承**：V2 Token 持有实例能力与节点 scope 后，可访问该节点已有实例和签发后新建实例；实例移出节点后失权，移入后获权；实例 scope 不反向授权节点。
4. **双重门禁**：MCP `tools/list` 按能力与可用 scope 裁剪；手工调用未列出工具、能力不足工具或 scope 外目标仍由 `tools/call` 拒绝。
5. **永久禁区**：用户/组/RBAC、Agent Token、密钥明文、数据库浏览、自更新、平台设置不在 action 目录和 MCP tool 中。
6. **调用流水**：HTTP 与 MCP 调用均记录 capability、action、targetType/targetId、成功/失败；历史空 capability 可正常查询。
7. **兼容契约**：既有 Agent HTTP 路径、jmagent 命令、MCP 传输、401/403、MCP HTTP 200 + `isError=true` 语义不变；旧客户端仍能解析 Token API。
8. **工具真源**：action→capability→scope 映射只有 service action 目录一处；MCP 不再维护独立 capability/action/target switch。
9. **自动化测试**：service、router AgentGate、MCP、调用流水、数据库兼容与前端相关测试全部通过；AgentGate 新增 V1/V2/继承矩阵。
10. **真链路验收**：本机 CP 上分别签发 V1 与 V2 Token，完成 HTTP 和 MCP 的 `whoami`、动态 `tools/list`、节点继承实例读取、scope 外拒绝、吊销后 401；保存命令和结果证据。

## 6. 风险与处理

- **旧请求与新请求歧义**：使用 `policyVersion` 明确区分；V2 必须显式提交 capabilities，禁止与 writeAllowlist 混用。
- **节点继承导致旧 Token 扩权**：只对 policyVersion=2 启用继承，V1 永久走兼容解释器。
- **实例归属并发变化**：不缓存实例→节点关系；写操作派发前重验并携带预期归属，变化则拒绝。
- **规则散落再次漂移**：action 目录为唯一能力真源；ToolSpec 只做协议映射。
- **能力组未来扩展的授权含义**：V2 Token 持有的是分组授权，组内新增 action 可被其获得；高危 action 必须放入独立 destructive 组，永久禁区永不进入目录。
- **前端默认值误授权**：服务端不增加默认能力；UI 默认只读并明确展示，最终创建请求可见且可取消。
