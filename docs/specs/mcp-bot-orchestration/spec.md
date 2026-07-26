# 功能规格：MCP Bot 舰队与压测编排全工具

> 状态：草拟　·　关联 PRD：FR-398　·　优先级：P0　·　依赖：FR-395（已实现）　·　下游：FR-399　·　关联 ADR：080（能力真源）、075（命令成功边界）　·　分支：feature/fr-398-mcp-bot-orchestration

## 1. 背景与目标

FR-362/369~372 已建成完整 Bot 舰队与分布式压测面：普通 Bot 管理（`BotService`）、压测模板 CRUD（`BotLoadTemplateService`）、会话与运行状态机（`BotStressSessionService`/`BotLoadExecutionService`）、容量预检与不透明 `planToken`（`BotLoadPreflightService` + `BotLoadPlanTokenSigner`，HMAC + 60s TTL + allocationHash/容量世代绑定）、失败子集重试（`RetryFailed`，请求级幂等）、分页投影（`BotLoadProjectionService`，pageSize≤100）与 JSON/CSV 报告（`BotLoadReportService`）。

FR-398 把这套能力以强类型 MCP 工具开放给 scoped Agent，使其独立完成「模板 → 运行 → 容量 → 预检 → 启动 → 观测 → 停止/重试 → 报告」闭环，是 FR-399 资源采样与 TEST-500 真机战役的操作入口。

## 2. 需求（要什么）

### 2.1 资源类型扩展

action 目录当前只有 `none|node|instance`。本 FR 新增 **`bot`** 与 **`botrun`** 资源类型：

- `AgentResourceBot`：目标为单个普通 Bot；授权 = Bot 所属实例（`bot.instance_id`）过实例 scope 规则。
- `AgentResourceBotRun`：目标为压测会话/运行；授权 = 会话目标实例过实例 scope **且** 该运行涉及的每个 executor node 均过节点 scope（见 2.4）。

`principalHasPotentialScope` 对两者复用实例分支（有显式实例 scope 或 V2 节点 scope 即可发现）；`Authorize` 走新增的可信目标解析路径。

模板资源不引入新类型：模板不绑定实例，按 §2.3 的所有权规则处理，`ResourceType` 用 `none` + 能力校验。

### 2.2 新增 action 与工具清单

全部 `V1Allowed=false`、`HTTPInContract=false`。

#### 普通 Bot（resource=bot / instance）

| MCP 工具 | action | capability | 操作 | 复用 service |
|---|---|---|---|---|
| `bot_list` | `agent.bot_list` | `bot.read` | read | `BotService.ListPaged`（scope=可访问实例集合） |
| `bot_get` | `agent.bot_get` | `bot.read` | read | `BotService.GetByID` |
| `bot_create` | `agent.bot_create` | `bot.manage` | write | `BotService.Create`（目标实例过 scope） |
| `bot_set_behavior` | `agent.bot_set_behavior` | `bot.manage` | write | `BotService.UpdateBehavior` |
| `bot_send_command` | `agent.bot_send_command` | `bot.manage` | write | `BotService.SendCommand` |
| `bot_delete` | `agent.bot_delete` | `bot.manage` | destructive | `BotService.Delete` + 精确确认 `confirmBotName` |

`bot_send_command` 的成功语义严格遵循 ADR-075：工具返回文本明确写「已发送（`bot.chat` 调用成功）」，**不宣称**服务器已接受或业务已生效；工具描述同样写明，防 Agent 误报。

#### 压测模板（resource=none）

| MCP 工具 | action | capability | 操作 | 复用 service |
|---|---|---|---|---|
| `loadtest_template_list` | `agent.loadtest_template_list` | `bot.read` | read | `BotLoadTemplateService.List` |
| `loadtest_template_get` | `agent.loadtest_template_get` | `bot.read` | read | `BotLoadTemplateService.Get` |
| `loadtest_template_create` | `agent.loadtest_template_create` | `bot.load` | write | `BotLoadTemplateService.Create` |
| `loadtest_template_update` | `agent.loadtest_template_update` | `bot.load` | write | `BotLoadTemplateService.Update` |
| `loadtest_template_delete` | `agent.loadtest_template_delete` | `bot.load` | destructive | `BotLoadTemplateService.Delete` + 确认 `confirmTemplateName` |

模板结构（命令计划 / 负载曲线 / 阈值）以既有 `BotLoadTemplateInput` 的 JSON 为准，工具接受结构化 JSON 对象参数（不接受 YAML 文本——YAML 是管理台便利层；MCP 用 JSON 更严格，避免解析歧义）。校验完全复用 service 内既有 `normalizeInput` 与场景校验。

#### 运行编排（resource=botrun）

| MCP 工具 | action | capability | 操作 | 复用 service |
|---|---|---|---|---|
| `loadtest_run_create` | `agent.loadtest_run_create` | `bot.load` | write | `BotStressSessionService.Create` 或 `BotLoadTemplateService.CreateRunFromTemplate` |
| `loadtest_run_list` | `agent.loadtest_run_list` | `bot.read` | read | `BotStressSessionService.List`（scope 传入） |
| `loadtest_run_get` | `agent.loadtest_run_get` | `bot.read` | read | `BotStressSessionService.Get` |
| `loadtest_node_capacity` | `agent.loadtest_node_capacity` | `bot.read` | read | `BotLoadHandler.LoadNodes` 对应的容量目录查询（scope 内节点） |
| `loadtest_run_preflight` | `agent.loadtest_run_preflight` | `bot.load` | write | `BotLoadPreflightService.Preflight` |
| `loadtest_run_start` | `agent.loadtest_run_start` | `bot.load` | write | `BotLoadExecutionService.Start`（消费 planToken） |
| `loadtest_run_stop` | `agent.loadtest_run_stop` | `bot.load` | write | `BotLoadExecutionService.Stop` |
| `loadtest_run_retry_failed` | `agent.loadtest_run_retry_failed` | `bot.load` | write | `BotLoadExecutionService.RetryFailed` |

#### 观测与报告（resource=botrun，capability=`bot.read`；指标类 `observability.read`）

| MCP 工具 | action | capability | 复用 service |
|---|---|---|---|
| `loadtest_run_bots` | `agent.loadtest_run_bots` | `bot.read` | `BotLoadProjectionService.ListBots` |
| `loadtest_run_failures` | `agent.loadtest_run_failures` | `bot.read` | `BotLoadProjectionService.ListFailures` |
| `loadtest_run_events` | `agent.loadtest_run_events` | `bot.read` | `BotLoadProjectionService.ListEvents` |
| `loadtest_run_metrics` | `agent.loadtest_run_metrics` | `observability.read` | 会话指标查询（复用 router Metrics 对应 service 路径） |
| `loadtest_run_report` | `agent.loadtest_run_report` | `bot.read` | `BotLoadReportService.BuildJSON` / `BuildCSV` |

分页：全部沿用 `normalizeProjectionPage`（默认 20、上限 100）。5000 条明细必须分页遍历，工具描述写明「单次最多 100 条，用 page 递增」。CSV 报告体量护栏：超过 512KiB 时返回中文提示与摘要，引导按分页明细查询（不返回巨型 tool 响应）。SSE 流（`/stream`）**不开放**——MCP 是请求-响应模型，流式观测用轮询代替。

### 2.3 模板所有权

`BotLoadTemplateService` 的 CRUD 以 `userID` + `isAdmin` 做所有权判断。Agent Token 不是用户，需明确语义：

- Agent 模板操作以**平台级视角**执行（等价 `isAdmin=true`、`userID=0`），因为 Token 授权边界已由 capability 表达，再叠加用户所有权会产生「Agent 创建的模板归谁」的悖论。
- 该决策写入工具描述与 ADR 附注：持有 `bot.load` 即可管理全部模板（包含管理台用户创建的）。若后续需要隔离，另开 FR。
- `loadtest_template_delete` 的精确确认参数是防误删护栏，弥补无所有权隔离的风险。

### 2.4 运行的双重 scope（目标实例 + 全部 executor 节点）

`loadtest_run_preflight`/`start` 的授权在 capability 之后追加：

1. 会话目标实例（`session.instance_id`）经实例 scope 规则授权（含 V2 节点继承）。
2. 预检输入的 `executorNodeIDs`（或运行已持久化的 executor 节点集合）中**每一个**都必须过节点 scope；任一越界 → 整体拒绝，不缩减节点集合静默降级。
3. `loadtest_run_stop`/`retry_failed`/观测类：以运行已关联的 executor 节点集合（`loadSessionExecutorNodes` 等价查询）逐一校验。停止操作**允许**在部分节点已不在 scope 时仍执行停止（安全方向），但须在返回中列出越界节点；启动方向严格拒绝。
4. 归属并发变化：启动前重验目标实例归属（沿用 FR-395 expected-node 语义思路）。

### 2.5 planToken 不透明

- `loadtest_run_preflight` 原样返回 service 生成的 planToken 字符串（base64 payload.signature）与 `expiresAt`，工具描述写明「不透明、60 秒内有效、只能整串回传给 loadtest_run_start」。
- `loadtest_run_start` 只接受该字符串；MCP 不解析、不重签、不缓存。
- 过期/容量世代变化 → service 既有错误原样中文返回，引导重新预检。

### 2.6 幂等与容量不足

- `loadtest_run_start`/`stop` 沿用 service 既有幂等（`claimBotLoadStartState`、stop intent 记录）；重复调用不产生副作用，返回当前状态。
- `retry_failed` 沿用 `requestId` 幂等；MCP 要求 Agent 传 `requestId`（工具描述说明重试同一批必须复用同一 requestId）。
- 容量不足：预检返回 blockers 与缺口明细（service 已有），MCP 原样投影；**不物化任何 Bot**（service 保证，测试断言）。

### 2.7 范围外

- SSE/流式观测、WebSocket。
- YAML 模板文本入口（JSON 结构化即可）。
- Bot 批量操作（`BotService.Batch`）——压测运行已覆盖规模化场景。
- 场景引擎 V2 的独立编辑工具（模板 JSON 内含即可）。
- 资源采样与容量报告字段（FR-399）。
- 模板所有权隔离、Agent 专属模板命名空间。

## 3. 设计（怎么做）

### 3.1 agent_capability.go 扩展

- 新增 `AgentResourceBot = "bot"`、`AgentResourceBotRun = "botrun"` 常量并纳入 `principalHasPotentialScope`（复用实例分支逻辑）。
- `Authorize` 的 switch 增加两个分支：委托新的可信目标解析（见 3.2），不在 `Authorize` 内查库——保持该函数纯判断。
- 登记 §2.2 全部 action；destructive 项置 `RequiresConfirm`（复用 FR-396 机制，并行期自带等价实现）。

### 3.2 可信目标解析（agent_scope.go 扩展）

`AgentTokenService` 增加：

```text
AuthorizeBotAction(p, action, botID) (AgentAuthorization, *model.Bot, error)
    // 查 bot → 取 instance_id → 查实例归属 → Authorize(target{instance})

AuthorizeBotRunAction(p, action, sessionID) (AgentAuthorization, *model.BotStressSession, []uint, error)
    // 查会话 → 目标实例授权 → 收集 executor 节点集合 → 逐一校验节点 scope
    // 返回越界节点列表供 stop 方向的告知语义
```

均从 CP 数据库读取归属，不接受客户端传入的 instanceId/nodeId 作为授权依据。

### 3.3 MCP 工具文件

新增（依赖 FR-396 的 `toolSpec.Exec` 注册表；并行期自带等价实现，集成时对齐）：

```text
internal/controlplane/mcp/
  tools_bot.go            # 普通 Bot 域
  tools_loadtest.go       # 模板 + 运行编排
  tools_loadtest_query.go # 观测/报告投影
```

`ToolDeps` 增：`Bot *BotService`、`LoadTemplate *BotLoadTemplateService`、`StressSession *BotStressSessionService`、`Preflight *BotLoadPreflightService`、`Execution *BotLoadExecutionService`、`Projection *BotLoadProjectionService`、`Report *BotLoadReportService`、`Capacity`（容量目录接口）。nil → 中文「服务不可用」。

### 3.4 错误语义

- scope 外/不存在：收敛 `ErrAgentForbidden`。
- 容量不足、预检 blockers、planToken 失效、状态机拒绝：service 中文错误原样 + `isError=true`（业务失败不伪装成权限问题）。
- 定位信息：失败返回尽可能带 Worker/节点、Bot、步骤或 checkpoint 标识（service 已有的错误明细原样投影，不吞）。

## 4. 任务拆分

- [ ] 失败测试先行：bot/botrun 资源授权矩阵（V1 不可见、V2 能力、实例 scope、executor 节点全覆盖/部分越界）、planToken 不解析、分页上限、确认参数、幂等重复调用。
- [ ] `agent_capability.go`：新资源类型 + action 登记。
- [ ] `agent_scope.go`：`AuthorizeBotAction` / `AuthorizeBotRunAction` + 单测。
- [ ] `tools_bot.go` 实现 + InputSchema + ADR-075 措辞。
- [ ] `tools_loadtest.go`（模板 + 运行编排，含 planToken 透传）。
- [ ] `tools_loadtest_query.go`（分页投影 + 报告体量护栏）。
- [ ] main.go 装配 ToolDeps。
- [ ] MCP 契约测试：tools/list 裁剪矩阵 + 每工具 tools/call 授权与成功路径 + 容量不足不物化 Bot。
- [ ] 文档同步：PRD 状态、ARCHITECTURE、`docs/specs/cp-mcp-server/spec.md` 工具表、CHANGELOG 末尾追加；ADR 附注模板所有权决策。
- [ ] 真机链路（小规模，非 500）：V2 Token（bot.read+manage+load+instance.read+observability.read）走通「建模板→建运行→查容量→预检→启动（≤10 Bot）→查 bots/events→停止→报告」，证据存 `.tmp/acceptance-fr398-*.txt`。

## 5. 验收标准

1. **闭环**：仅经 MCP 完成模板→运行→预检→启动→观测→停止/重试→报告全流程。
2. **planToken 不透明**：MCP 不解析不重签；只能整串回传；过期/世代变化拒绝并引导重预检（测试）。
3. **双重 scope**：目标实例 + 每个 executor 节点均校验；启动方向任一越界整体拒绝；停止方向执行但告知越界节点（测试矩阵）。
4. **容量不足不物化**：预检返回缺口，数据库无新增 Bot/批次（测试断言）。
5. **分页护栏**：明细单次 ≤100 条；报告超限有摘要引导；无超大 tool 响应。
6. **ADR-075 边界**：`bot_send_command` 返回与描述均不宣称服务器业务执行成功（措辞审查 + 测试断言文案）。
7. **幂等**：start/stop 重复调用无副作用；retry 同 requestId 幂等（测试）。
8. **可定位失败**：失败返回含 Worker/节点、Bot、步骤或 checkpoint 信息。
9. **tools/list 裁剪**：`bot.read`/`bot.manage`/`bot.load` 能力矩阵正确。
10. **回归**：`go test ./internal/controlplane/...` 全绿；管理面 Bot/压测行为零变化。
11. **真机**（需用户确认通过）：§4 最后一项小规模闭环证据；500 Bot 规模属 FR-399/TEST-500，本 FR 不宣称。

## 6. 风险 / 待定

- **与 FR-396/397 并行冲突**：三分支都动 `agent_capability.go` 目录与 `tools.go` 骨架/`ToolDeps`。缓解：FR-396 拥有骨架重构，本 FR 只追加注册与目录条目；集成顺序建议 396 → 397 → 398，冲突集中在 catalog map 与 ToolDeps 结构体（追加式，易解）。
- **模板所有权语义**：本 FR 选定「持 `bot.load` 即管理全部模板」，须用户认可；否则需先做隔离设计（会阻塞本 FR）。
- **executor 节点集合的时点**：预检输入 vs 运行已持久化集合可能不同；本 FR 以「预检校验输入、后续校验已持久化集合」为准，并在启动前重验。
- **报告体量**：CSV 全量可能很大；护栏返回摘要而非截断正文，避免 Agent 误当完整数据。
- **500 Bot 真机不在本 FR**：本 FR 只保证工具与授权正确，规模验证由 FR-399 + TEST-500 承担，不得在此宣称 500 实测。
