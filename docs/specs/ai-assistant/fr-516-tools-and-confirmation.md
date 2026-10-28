# 功能规格：受限系统工具与逐调用写入确认（FR-516）

> 状态：**已实现（2026-10-23）：执行层 + 契约内工具覆盖度全部接通，真机验收通过**　·　关联 PRD：FR-516　·　范围：仅 FR-516
>
> 已落地：工具登记表（从 `agentActionCatalog` 派生 + 收窄）、用户委托执行器
> （执行前重跑完整权限判定，不构造 `AgentPrincipal`、不复用 `/api/v1/mcp`）、
> 确认票据与回执两张表、三个确认端点、CAS 消费 + `in_flight` 回执同事务、
> 启动回收 `in_flight → unverified`、逐调用确认卡（前端）、
> 三类受限只读工具（集群概览 / 任务 / 告警，**均按调用者可见范围收敛**）、
> **契约内 10 个既有 action 的领域接线**（5 读 + 5 写，见下）。
> 测试：22 项（写必确认、禁止动作不在目录、契约内筛选、三类受限读存在、
> 工具顺序稳定、参数哈希键序无关、冻结参数往返、TTL=10 分钟；
> 新增：登记即实现、schema 与解析器参数一致、无包裹式 schema、
> 领域服务未装配不下发、写要求能力位、无目标类型放行、whoami 计数口径、
> 确认端点 502 映射）。
>
> **工具覆盖度的真实规模（纠正早期估计）**：`agentActionCatalog` 里
> `HTTPInContract=true` 的只有 **11 条**；按 spec §4.1 只收契约内 + 排除禁止项后，
> 目录是 **10 个 catalog 工具 + 3 个受限只读 = 13 个**。早期记的「40+ 读 / 50 写」
> 误把 `internal/controlplane/mcp` 包的 108 个 MCP 专属工具算了进来——那些不在
> HTTP 契约内，按 §4.1 本来就不收录。因此覆盖度的完成判据是
> **「目录里暴露给模型的每个工具都能执行」**，已由 `TestAssistantTools_EveryRegisteredToolImplemented` 守门。
>
> **未做（如实标注，不要当成已完成）**：
> 1. **审计未附 conversationId/runId/callId**：现有 `AuditLog` 模型没有这三列，
>    扩展它属于跨领域改动，需单独评估；当前 `assistant_receipts` 已能按
>    `(run_id, call_id)` 串起一次完整调用（回执表本身就是调用审计）。
> 2. 确认端的并发上限（「同 run 同时最多 1 张 pending」）**已强制**（见下）；
>    但需注意：当前 `executeToolCalls` 在第一个写调用出票后即 `return`，
>    同轮后续调用根本不会被处理，因此这条约束在行为层面本就成立——
>    数据库唯一索引是**兜底**，防止将来改成「继续处理同轮其余调用」时静默失效。
> 3. 写操作的真机闭环依赖 Worker 在线：本机验收时无 Worker，
>    `instance_start` 批准后如实返回 `502 TOOL_EXEC_FAILED`（"节点未连接"）；
>    `node_maintenance_enter/leave` 不依赖 Worker，已双向验到状态真实变更。

### 审计串联（§9 实现说明）

助手执行的**写操作**会写平台审计，`action` 为 `assistant.tool.<工具名>`，
前缀让审计页能区分「用户直接点的启停」与「助手代办的启停」——两者追责路径不同。

`audit_logs` 新增三列 `conversation_id` / `run_id` / `call_id`（均可空、均带索引）。
**为什么不复用 `Detail` JSON**：验收要求「按这三个 ID 串起一次完整调用」，
放进 JSON 就只能 `LIKE '%"runId":123%'` 全表扫描，而审计表正是最容易变大的那张表。
三列可空是因为绝大多数审计行来自 HTTP 端点，没有会话/运行概念。

`Detail` 只存参数**摘要**（`argsHash`）而非原文：审计可见面比票据更广，
而工具参数可能含路径、命令等敏感值。原文保留在票据 `args_frozen`，按 `confirmationId` 取。

失败也留审计：只记成功的写，会让「模型反复尝试但一直被拒」在审计里完全隐形，
而那恰是安全审查最想看到的行为。

IP 记空串：执行发生在后台或确认请求内，service 层拿不到客户端 IP。
刻意不塞占位值（如 `"assistant"`）——那会让人误以为是真实来源地址。
> 5. 既有 `/api/v1/mcp` 与 jmat 策略**零改动**（已跑原有 MCP 测试确认）。
> 共享契约见 [shared-contracts.md](shared-contracts.md)（§2 授权、§4.2 工具描述、§5 执行与确认），本文不复述。
> 前置：FR-515（运行时与 run 状态机）。

## 1. 背景与目标

FR-515 能跑对话循环，但还没有任何工具可调。FR-516 交付**工具执行层**：把平台既有业务能力以「当前登录用户自己的权限」暴露给模型，并且**所有写入都必须由用户逐次确认、且确认不提权**。

核心矛盾（ADR-105 上下文）：现有 MCP 工具面是为「持有显式 scope 的自动化凭证」设计的（`AgentPrincipal` 带 `ScopedInstanceIDs / ScopedNodeIDs / WriteAllowlist`，见 [internal/controlplane/service/agent_token.go](../../../internal/controlplane/service/agent_token.go#L275-L283)）。人类 JWT 不是 MCP 凭据，直接复用会让「模型说什么就做什么」变成新的提权面。因此本 FR 要新建一条**用户委托执行链**，与 jmat 通道完全隔离。

范围内：工具登记表（读/写风险分类）、用户委托执行器、受限只读工具补齐、逐调用确认票据与回执、执行前重验、审计留痕。

范围外：会话与 run 状态机（FR-515）、外部 MCP 工具（FR-517）、页面上下文（FR-518）、聊天 UI（FR-519）、平台级审批中心（FR-490～493，仍为计划）。

## 2. 现状核实（只读结论）

- **action 目录已是唯一真源**：`agentActionCatalog` 与各 `frNNNDomainActions` 登记了 `Action / V2Capability / ResourceType / Operation / V1Allowed / V1WriteAllow / HTTPMethod / HTTPPath / HTTPInContract / RequiresConfirm`（[internal/controlplane/service/agent_capability.go](../../../internal/controlplane/service/agent_capability.go#L46-L57)）。FR-516 **复用这张表**，不新建平行目录。
- **`RequiresConfirm` 目前只是目录标记，没有执行骨架**：注释明写「FR-396：destructive 操作须服务端精确确认参数」（同文件 L57），而 `mcp/tools_bot.go` L14、`mcp/tools_content.go` L88 都注明「待 FR-396 的骨架合入后可整体收敛为一份」。即：**当前危险操作靠逐个工具的参数名精确确认**（如 `confirmBotName`、`confirmPath`），不是统一票据机制。
- **capability 分类已足够细**：`node.read/operate/destructive`、`instance.read/write/life/command/provision/configure/content/destructive`、`bot.read/manage/load`、`observability.read`（能力常量在 [agent_capability.go L13-L27](../../../internal/controlplane/service/agent_capability.go#L13-L27)，完整已知能力集合在 L72-L88）。读写分界无需重新发明。
- **已有可信目标解析**：`AgentTrustedTarget`（`ResourceType / InstanceID / NodeID`）由 CP 侧数据解析，客户端不得伪造归属（[agent_capability.go L65-L70](../../../internal/controlplane/service/agent_capability.go#L65-L70)）。注意 L59-L63 是 `AgentAuthorization`，不是 TrustedTarget。用户委托执行器沿用同一解析路径。
- **`UserAccess` 是授权真源**（[internal/controlplane/service/authz.go](../../../internal/controlplane/service/authz.go#L59-L69)），每轮重新装载，不缓存。
- **无平台审批中心**：FR-490～493 仍为计划，本 FR **不得伪称已存在**，也不改变既有 jmat 调用策略。

## 3. 冻结的共同选择

| 项 | 冻结值 | 理由 |
|---|---|---|
| 授权来源 | 每次执行前重新装载 `UserAccess`，校验账号状态 + `AuthVersion` + action 权限 + 资源归属 | 不引入「会话内权限快照」（ADR-105 决策 2） |
| 身份构造 | **不构造** `AgentPrincipal`，不复用 `/api/v1/mcp` | 两条授权链保持独立（ADR-105 决策 2） |
| 工具目录 | 复用 `agentActionCatalog`，仅**收窄**不扩张 | 单一真源，避免两套 action 说法 |
| 风险分类 | 读 = 自动执行；写 = 一律逐调用确认（不看模型自述、不看 MCP annotations） | 用户已确认「读取自动/写入确认」 |
| 确认有效期 | 10 分钟 | 与平台审批的 24 小时区分（shared-contracts §5.2） |
| 确认绑定 | `ownerUserId + conversationId + runId + callId + 工具 schema 指纹 + 策略版本 + 规范化参数哈希 + 目标指纹 + expiresAt` | 任一变化即使旧确认失效 |
| 确认接口 | 只接受 `approve` / `reject`，**不接受新参数或新目标** | 防止「确认即改参」 |
| 消费语义 | CAS 消费，执行前重验权限与目标状态 | 一次性、不可重放 |
| 结果不确定 | 标 `unverified`，**不自动重试** | 跨 Worker/外部网络无事务保证 |
| 审计 | 复用既有业务审计，附 `conversationId / runId / callId` | 不新造审计体系 |
| 明确不做 | 通用 Shell、CP 任意文件读写、平台治理、秘密明文、数据库浏览、助手自身配置、自更新 | shared-contracts §2 第 6 条 |

## 4. 工具登记

### 4.1 复用与收窄

从 `agentActionCatalog` 派生助手工具目录，规则：

1. 只收录 `HTTPInContract` 为真的 action（契约内，路径与参数有据可查）。
2. 只收录 `Operation` 属于 `read` 或 `write`/`destructive` 的 action；`platform_governance` 类**永久排除**。
3. 工具对外名用稳定命名空间，避免与未来外部 MCP 连接的工具名冲突（FR-517）。
   **分隔符只能用下划线**：`system_<action>`，且 action 内的点号一并转下划线
   （`agent.list_nodes` → `system_agent_list_nodes`）。
   原因（2026-10-24 真实上游联调发现）：OpenAI 工具名字符集是 `^[a-zA-Z0-9_-]{1,64}$`，
   点号不在其中，上游对**任何带工具的请求**直接 400
   （`{"code":11152,"msg":"the tool name is invalid or duplicated"}`）。
   假上游不校验字符集，所以「只跑模拟」永远发现不了；`TestAssistantToolNames_SatisfyUpstreamCharset`
   已把这条变成 CI 门禁。**不得改回点号。**
4. `read` 类：`RequiresConfirm=false`，自动执行。
5. `write` / `destructive` 类：**强制** `RequiresConfirm=true`。即使目录里标记为 false 也不放行——写必确认是本 FR 的不变量。

### 4.2 需补齐的只读工具

现有目录偏向运维动作，AI 首页与排查场景还缺三类受限读（shared-contracts §2 第 5 条要求「尚无受限读路径的工具先不暴露」）：

| 缺口 | 说明 | 约束 |
|---|---|---|
| 集群概览摘要 | 节点在线数、实例状态分布、资源水位 | 只回聚合值，不回逐台明细；非管理员按可见范围聚合 |
| 任务列表（受限） | 当前用户有权看到的任务 | 按创建者/关联资源收敛；不可见即 404（沿原服务语义） |
| 告警事件（受限） | 未解决告警 | 目标按规则维度识别 node/instance，**不得把 targetId 一概当实例**（shared-contracts §2 第 5 条） |

这三类若在 FR-515 之前无法给出受限读路径，则**不进工具目录**，而不是「先暴露再前端过滤」。

## 5. 用户委托执行器

### 5.1 执行链

```
模型 tool_call
  → 参数 JSON 解析 + schema 校验（失败即拒，不进入下一步）
  → 工具登记查询（不存在/未收录 → 拒）
  → 风险分类
      ├─ read  → 直接执行
      └─ write → 建确认票据 → run 进入 waiting_confirmation → 等用户决定
  → 执行前重验：装载 UserAccess、**重跑完整有效权限判定**（不只比 `AuthVersion`，理由见 §6.1 的 `auth_version` 行）、动作权限、目标归属与当前状态
  → CAS 消费票据 + 创建 `in_flight` 回执（write 路径，**同一事务**，提交后才调 service）
  → 调用**原领域 service**（不复制业务逻辑）
  → 回执更新为终态 + 审计
```

### 5.2 禁止事项

- 不得为人类用户签发 `jmat_` Token（ADR-105 被否备选二）。
- 不得构造 `(userID=0, isAdmin=true)` 之类的隐式全平台身份。
- 不得把「发现阶段按权限裁剪」当作授权——发现只是减噪，执行必重验。
- 不得让模型或 Agent 调用确认端点（确认只接受人类 JWT）。

## 6. 确认票据与回执

### 6.1 `assistant_confirmations`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint PK | |
| `source` | string | 票据来源枚举：本 FR 只写 `chat`；预留 `jmat`（见 §10.1）。**受限枚举，不是自由文本**——新增来源须同时改枚举与校验 |
| `owner_user_id` | uint，索引 | |
| `conversation_id` / `run_id` | uint，索引 | |
| `call_id` | string | 与事件里的 callId 对应 |
| `tool_name` | string | 命名空间名 |
| `schema_fingerprint` | string | 工具 schema 的指纹。内置工具用版本号，**外部 MCP 工具用其 `schema_hash`**（FR-517 §4.2 只存 hash，没有整数版本）；两者统一按「字符串指纹不等即失效」处理，不假设存在单调递增的版本号 |
| `policy_version` | int | 执行策略版本变化使旧确认失效 |
| `args_hash` | string | 规范化参数的 SHA-256（键排序后序列化） |
| `args_frozen` | text(JSON) | **冻结参数正文**（票据创建时的原样快照）。`args_hash` 只用于比对，核对 UI 与执行都以本字段为权威来源；不含敏感明文（凭据类参数在冻结前已由工具 schema 声明为 secret 并脱敏） |
| `auth_version` | uint | 出票时的用户 `AuthVersion` 快照。**它只是必要不充分的一道闸**：现有 `AuthVersion` 仅在改密/改角色/改状态/重置时递增（`service/user.go`、`service/auth_reset.go`），**角色模板改权、角色绑定变更、组成员移除都不会递增它**。因此执行前**必须重跑完整的有效权限判定**（见 §5.1），本字段只用于快速挡掉明显失效的场景，不能单独作为「权限未变」的证明 |
| `policy` | string | 执行策略枚举：本 FR 只写 `self_confirm`；预留 `platform_gate`（见 shared-contracts §5.2） |
| `target_fingerprint` | string | 真实目标指纹（resourceType + ID） |
| `status` | string | `pending` / `approved` / `rejected` / `expired` / `consumed` / `invalidated` |
| `decided_at` / `expires_at` | datetime | 有效期内未决定即 `expired` |
| `consumed_at` | datetime，可空 | CAS 消费时间 |

`(run_id, call_id)` 唯一：同一调用不重复出票。

### 6.2 `assistant_receipts`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint PK | |
| `confirmation_id` | uint，**可空**，唯一（非空时） | 写路径必填；**只读调用为 null**——只读不出票，也就没有票据可关联 |
| `run_id` / `call_id` | uint / string | 只读与写路径都必填，用于串联审计（`(run_id, call_id)` 唯一） |
| `outcome` | string | `in_flight` / `ok` / `failed` / `rejected` / `expired` / `unverified` |
| `error_code` / `error_message` | string | 已脱敏 |
| `result_summary` | text | 有界摘要（上限见 §8） |
| `started_at` / `finished_at` | datetime | `in_flight` 时 `finished_at` 为空 |

**`in_flight` 必须先落库再派发**（这是重启恢复判据存在的前提，见下方"崩溃窗口"）：

1. 校验票据 → 消费票据、创建 `outcome=in_flight` 回执行、置票据 `consumed`——**三步同一事务**；
2. 事务提交后才真正调用领域 service；
3. 拿到确定结果后，把该回执更新为 `ok` / `failed` 并写 `finished_at`。

**崩溃窗口与恢复规则**（与 FR-515 §8 对齐）：

| 崩溃位置 | 落库状态 | 重启后处置 |
|---|---|---|
| 事务提交前 | 票据未消费、无回执 | 票据仍可用（未执行过），无害 |
| 事务提交后、调用返回前 | 票据 `consumed`、回执 `in_flight` | 标 `unverified`：**已可能产生副作用，不得重放** |
| 调用返回后、更新回执前 | 同上 | 同上（结果已丢失，如实标未知） |
| 更新回执后 | 回执为终态 | **不动**，已确定的结果不改写 |

`unverified` 语义：已发出写调用但未取得确定结果（连接断开、超时、进程崩溃等）。**禁止**把它当失败重试，也禁止当成功；只能由用户人工核实后另行处置。

### 6.3 失效条件

以下任一变化使**未消费**的票据（含 `pending` 与已批准但未执行）不可复用：

- **有效权限发生变化**：包括 `AuthVersion` 递增（改密、改角色、改状态、重置），以及 `AuthVersion` **不会**反映的那几类——角色模板改权、角色绑定变更、组成员移除。后者必须由执行期的完整重验（§5.1）捕获，不能假设票据里的版本号已经覆盖。
- 工具 schema 指纹或策略版本变化
- 工具被停用（含外部 MCP 工具 `enabled=false`）或连接失效
- 参数哈希或目标指纹不匹配
- 超过 `expires_at`
- run 已终止

**已消费（`consumed_at` 非空）的票据保持不可重放**——它是一次执行的凭证，不是可刷新对象。

> 此前版本写成「使 `pending` 之外的旧票据失效」，恰好把最需要失效的 `pending` 排除在外，
> 与同文件验收项（等待确认期间撤权、改参后旧票据必须失效）直接冲突。已修正。

## 7. 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/assistant/runs/:runId/confirmations` | 该 run 的待确认列表（仅所有者） |
| `POST` | `/api/v1/assistant/confirmations/:id/decide` | body `{decision: "approve"\|"reject"}`，**只这两个值** |
| `GET` | `/api/v1/assistant/confirmations/:id` | 票据详情（含参数摘要与目标，供用户核对） |

错误码：`CONFIRMATION_EXPIRED`（410）、`CONFIRMATION_CONSUMED`（409）、`CONFIRMATION_STALE`（409，参数/目标/策略/权限已变）、`FORBIDDEN`（403，非所有者或非人类身份）。

## 8. 上限

| 项 | 默认值 | 语义 |
|---|---|---|
| `CONFIRM_TTL` | 10 分钟 | 逾期自动 `expired` |
| `RESULT_SUMMARY_MAX` | 8 KiB | 回执摘要上限，超出截断并标记 |
| `TOOL_TIMEOUT` | 30 秒 | 单次工具执行超时 → `failed`（写路径超时可能转 `unverified`） |
| 并发确认 | 同 run 同时最多 1 张 `pending` | 避免用户在多个待确认间迷失 |

## 9. 验收

- [x] 组外用户调用越界工具：执行前重验拒绝，且**不产生任何副作用**
      （`TestInstanceLifecycle_RejectsInvisibleInstance`、`TestNodeMaintenance_RejectsInvisibleNode`）
- [x] 实例读权不推导为整机写权：只读用户对节点级写工具被拒
      （`TestInstanceLifecycle_RequiresLifeCapability`、`TestNodeMaintenance_RequiresNodeOperate`）
- [x] 写工具在未确认前零副作用；确认后按原参数执行成功
      （真机：`node_maintenance_enter` 出票 → approve → `consumed`，节点 `maintenance` 0→1；
      再 `node_maintenance_leave` → approve → 1→0）
- [x] 确认过期（>10 分钟）后 approve 被拒且不执行
      （真机：`expires_at` 改到过去 → 410 `CONFIRMATION_EXPIRED`，票据 `expired`，副作用未发生）
- [x] 确认后参数被改（模拟模型二次提交不同参数）→ 旧票据失效，返回 `CONFIRMATION_STALE`
      （真机：改 `args_frozen` → 409 `CONFIRMATION_STALE`，票据 `invalidated`，未执行）
- [x] 同一票据重复 approve 只执行一次（CAS 生效）
      （真机：首次 200、二次 409 `CONFIRMATION_CONSUMED`；`maintenance` 0→1→1 只翻一次；审计仅 1 条）
- [x] 用户权限在等待确认期间被撤销 → approve 被拒，不执行
      （真机：等待期把用户角色从平台管理员降为成员 → 409 `CONFIRMATION_STALE`，票据 `invalidated`，未执行）
- [x] 模型/Agent 身份调用决策端点被拒（403）——`TestAgentToken_CannotReachConfirmationDecide`（Agent Token 打不到 `protected` 组的确认端点，断言 401/403 而非 404，避免「已越过鉴权层」的假绿）+ 对照组 `TestHumanJWT_CanReachConfirmationDecide`（人类 JWT 得 404 = 确实穿过了鉴权层）
- [x] 写调用超时 → 回执 `unverified`，不自动重试——`TestApproveAndRun_TimeoutMarksUnverifiedAndDoesNotRetry`（断言 outcome=`unverified`、error_code=`TOOL_TIMEOUT`、执行器**只调用 1 次**）+ 对照组 `TestApproveAndRun_NonTimeoutFailureMarksFailed`（非超时失败标 `failed`，两类必须可区分）；反向验证：让超时标 failed、让超时后重试，两例各自转红。前端「结果待核实」展示沿用既有回执渲染（未单独补 DOM 用例）
- [x] 审计中能按 `conversationId / runId / callId` 串起一次完整调用
      （三列为 `audit_logs` 独立索引列；真机：一次 `node_maintenance_enter` 批准后，
      按 runId 精确查到 1 行，三 ID 齐全、`action=assistant.tool.*`、详情只含参数摘要）
- [x] 同 run 最多 1 张 pending（`ux_assistant_confirmations_pending` 部分唯一索引；
      真机：一轮两个写调用只产生 1 张票据）
- [x] 平台治理、秘密明文、任意文件、助手自身配置不在工具目录（`TestAssistantToolRegistry_ForbiddenActionsExcluded`）
- [x] 三类受限只读工具（概览/任务/告警）在无受限路径时**不出现在目录**中
- [x] 既有 `/api/v1/mcp` 契约与 jmat 调用策略**零改动**（`router` 包全量通过）
- [x] **目录里每个内置工具都能执行**（`TestAssistantTools_EveryRegisteredToolImplemented`）
- [x] **工具 schema 与执行器参数解析一致**（`TestAssistantTools_TargetArgsMatchSchema`）
- [x] 真机：8 个只读工具各调用一次并返回真实数据
      （whoami / list_nodes / list_instances / get_instance / cluster_overview / list_tasks / list_alerts
      拿到真实值；get_instance_metrics 在无 Worker 时如实报「节点未连接」）

### 9.1 本轮真机验收中修复的缺陷（都是「看起来对」的实现错误）

| 缺陷 | 表现 | 修法 |
|---|---|---|
| run 循环用 `svc.toolExecutor()` 判空 | 聊天路径从不 `SetToolExecutor`，该判定恒真 → 模型**每一次**工具调用都被静默降级成普通回复：不执行、不报错、assistant 消息为空 | 改判 `ex.toolExec`（本 run 的执行器） |
| `resolveToolTarget` 漏 `AgentResourceNone` | 所有无目标工具（whoami / 概览类）一律报「不支持的目标类型 none」 | 加该 case，并加未知类型仍拒绝的反向断言 |
| 工具 schema 为 `{action, params}` 包裹 | 执行器从顶层读 `instanceId`，模型照 schema 传参必然落空 | 改为逐动作扁平 schema + 必填目标参数 |
| whoami 用 `len(access.Nodes)` 当节点数 | `UserAccess.Nodes` 是**权限节点**字符串集合，报出「可见 57 台节点」而库里只有 1 台 | 查真实节点表；全量/收敛两种口径分别计算 |
| `AccessibleInstanceIDs` 第二返回值被当成「全量可见」 | 真实语义相反：管理员得 `(nil,false)`、无组用户得 `([],true)`。误读会让管理员可见实例算成 0 | 判据改用 `IsPlatformAdmin`，并在两处注释写明真实语义 |
| 确认端点领域失败落入通用映射 | 返回 500 INTERNAL_ERROR，既像平台 bug 又暗示可重试（票据已消费） | 新增 `AssistantToolExecError` → 502 `TOOL_EXEC_FAILED` + `retryable:false` |

### 9.2 边界用例的反向验证（确认断言不是空转）

四条边界都用「注入缺陷 → 断言必须变红 → 恢复」的方式验证过，实际发现两处**断言本身无效**：

| 边界 | 注入的缺陷 | 结果 |
|---|---|---|
| 重复 approve | 只把 CAS 的 `Where` 放宽为按 id | **仍全绿** —— 因为第 552 行的前置 `status != pending` 检查先挡住了，CAS 根本走不到。说明只测状态码测不出 CAS 失效 |
| 重复 approve | 前置检查 + CAS 同时放宽 | 变红（二次 200、审计 2 条），断言有效 |
| 改参失效 | 跳过 `hashToolArgs != ArgsHash` 校验 | 变红（票据 `consumed` 并真去执行），断言有效 |
| 等待期撤权 | 跳过 `access.Can(tool.Capability)` 校验 | 变红（票据 `consumed` 并真去执行），断言有效 |

由此记录一条口径：**「重复 approve」的判据不能只看状态码**。CAS 与前置 status 检查是两道独立的闸，
只放宽其中一道时行为不变——因此判据必须包含「副作用次数」或「审计行数」，否则测不出 CAS 是否还在。

## 10. 已定 / 未决

### 10.1 已定（2026-10-23 用户确认）

**危险操作确认机制：聊天走新票据，jmat 通道不动，但票据抽象预留 `source` 扩展点。**

具体含义：
- 本 FR 交付的票据只承载 `source=chat`；数据结构里 `source` 是枚举字段（取值 `chat` / 预留 `jmat`），不是硬编码。
- 既有 `/api/v1/mcp` 的危险操作继续用「参数名精确确认」（`confirmBotName` 等），**本 FR 不改其行为**，也不改其契约。
- 代价如实记录：平台短期内存在两套危险操作确认语义。路由该缺口到 FR-396（`RequiresConfirm` 统一骨架）单独收敛，不在本 FR 顺手做。

### 10.2 未决

1. **只读工具的执行频率**：是否需要给 read 类加「每 run 最多 N 次」上限，防止模型反复轮询制造负载。
2. **确认取消后是否保留票据记录**：当前设计保留（审计需要），仅标 `rejected`。

## 附：PRD 表内原始记录（2026-10-26 迁入）

按「PRD 只保留一行 FR + 状态，详情归 spec」的约定，以下内容自 PRD 迁入（保留原文）。

### FR-516

受限系统工具与逐调用写入确认

（**执行层 + 契约内工具覆盖度全部接通，真机验收通过**：工具登记表从 `agentActionCatalog` 派生并收窄（写必确认、禁止项排除）、委托执行器执行前重跑完整权限判定、确认票据与回执两表 + CAS 消费同事务 + 启动回收 `in_flight → unverified`、三个确认端点、前端确认卡。**覆盖度真实规模**：契约内 action 仅 11 条，目录 = 10 个 catalog 工具 + 3 个受限只读 = 13 个（早期「40+ 读/50 写」误把 `mcp` 包 108 个 MCP 专属工具算入，那些不在 HTTP 契约内）。**测试**：service 22 项 + router 3 项，含「登记即实现」守门用例。**真机验收**（独立库 + 本地假 SSE 供应商）：8 个只读工具各调用一次并返回真实数据；`node_maintenance_enter/leave` 经确认后 `consumed` 且库内 `maintenance` 0→1→0 双向生效；`instance_start` 在无 Worker 时如实返回 502 `TOOL_EXEC_FAILED`。**过程中修掉 6 个真实缺陷**（run 循环用 svc 级判空致工具调用被静默丢弃、`AgentResourceNone` 漏判、schema 与解析器参数结构不一致、whoami 用权限键数冒充节点数、误读 `AccessibleInstanceIDs` 第二返回值、确认端点领域失败落成 500）。**审计串联已落地**：`audit_logs` 增三列（可空+索引）写助手写操作，`action=assistant.tool.*` 区分「用户直接操作」与「助手代办」，详情只存参数摘要不留原文，失败也留痕；真机按 runId 精确查到该轮记录。**`bot_send_command` 真机闭环跑通（2026-10-25）**：起真实 Paper 1.20.1（隔离目录、25599 端口、离线模式）+ 真实 Worker（grpc 19111）+ 真实 Bot（mineflayer），验证「模型自主发现工具 → 助手确认 → 平台审批 → Bot 在真实服务器执行」全链：模型自行 `search_tools`→`describe_tool`→`call_tool` 选中 `bot_send_command`；**确认前 MC 日志该命令出现 0 次**（零副作用）；助手确认消费后生成平台审批票（**双闸是有意设计**），批准后服务器日志出现 `testbot issued server command: /say FIXED-88888`。

