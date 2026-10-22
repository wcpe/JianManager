# 功能规格：受限系统工具与逐调用写入确认（FR-516）

> 状态：草拟 / 待用户审核，**未实现**（确认票据内核与用户委托执行器均无代码）　·　关联 PRD：FR-516　·　范围：仅 FR-516
> 共享契约见 [shared-contracts.md](shared-contracts.md)（§2 授权、§4.2 工具描述、§5 执行与确认），本文不复述。
> 前置：FR-515（运行时与 run 状态机）。

## 1. 背景与目标

FR-515 能跑对话循环，但还没有任何工具可调。FR-516 交付**工具执行层**：把平台既有业务能力以「当前登录用户自己的权限」暴露给模型，并且**所有写入都必须由用户逐次确认、且确认不提权**。

核心矛盾（ADR-105 上下文）：现有 MCP 工具面是为「持有显式 scope 的自动化凭证」设计的（`AgentPrincipal` 带 `ScopedInstanceIDs / ScopedNodeIDs / WriteAllowlist`，见 [internal/controlplane/service/agent_token.go](internal/controlplane/service/agent_token.go#L275-L283)）。人类 JWT 不是 MCP 凭据，直接复用会让「模型说什么就做什么」变成新的提权面。因此本 FR 要新建一条**用户委托执行链**，与 jmat 通道完全隔离。

范围内：工具登记表（读/写风险分类）、用户委托执行器、受限只读工具补齐、逐调用确认票据与回执、执行前重验、审计留痕。

范围外：会话与 run 状态机（FR-515）、外部 MCP 工具（FR-517）、页面上下文（FR-518）、聊天 UI（FR-519）、平台级审批中心（FR-490～493，仍为计划）。

## 2. 现状核实（只读结论）

- **action 目录已是唯一真源**：`agentActionCatalog` 与各 `frNNNDomainActions` 登记了 `Action / V2Capability / ResourceType / Operation / V1Allowed / V1WriteAllow / HTTPMethod / HTTPPath / HTTPInContract / RequiresConfirm`（[internal/controlplane/service/agent_capability.go](internal/controlplane/service/agent_capability.go#L46-L57)）。FR-516 **复用这张表**，不新建平行目录。
- **`RequiresConfirm` 目前只是目录标记，没有执行骨架**：注释明写「FR-396：destructive 操作须服务端精确确认参数」（同文件 L57），而 `mcp/tools_bot.go` L14、`mcp/tools_content.go` L88 都注明「待 FR-396 的骨架合入后可整体收敛为一份」。即：**当前危险操作靠逐个工具的参数名精确确认**（如 `confirmBotName`、`confirmPath`），不是统一票据机制。
- **capability 分类已足够细**：`node.read/operate/destructive`、`instance.read/write/life/command/provision/configure/content/destructive`、`bot.read/manage/load`、`observability.read`（同文件 L65-L80）。读写分界无需重新发明。
- **已有可信目标解析**：`AgentTrustedTarget`（`ResourceType / InstanceID / NodeID`）由 CP 侧数据解析，客户端不得伪造归属（同文件 L59-L63）。用户委托执行器沿用同一解析路径。
- **`UserAccess` 是授权真源**（[internal/controlplane/service/authz.go](internal/controlplane/service/authz.go#L59-L69)），每轮重新装载，不缓存。
- **无平台审批中心**：FR-490～493 仍为计划，本 FR **不得伪称已存在**，也不改变既有 jmat 调用策略。

## 3. 冻结的共同选择

| 项 | 冻结值 | 理由 |
|---|---|---|
| 授权来源 | 每次执行前重新装载 `UserAccess`，校验账号状态 + `AuthVersion` + action 权限 + 资源归属 | 不引入「会话内权限快照」（ADR-105 决策 2） |
| 身份构造 | **不构造** `AgentPrincipal`，不复用 `/api/v1/mcp` | 两条授权链保持独立（ADR-105 决策 2） |
| 工具目录 | 复用 `agentActionCatalog`，仅**收窄**不扩张 | 单一真源，避免两套 action 说法 |
| 风险分类 | 读 = 自动执行；写 = 一律逐调用确认（不看模型自述、不看 MCP annotations） | 用户已确认「读取自动/写入确认」 |
| 确认有效期 | 10 分钟 | 与平台审批的 24 小时区分（shared-contracts §5.2） |
| 确认绑定 | `ownerUserId + conversationId + runId + callId + 工具/schema/策略版本 + 规范化参数哈希 + 目标指纹 + expiresAt` | 任一变化即使旧确认失效 |
| 确认接口 | 只接受 `approve` / `reject`，**不接受新参数或新目标** | 防止「确认即改参」 |
| 消费语义 | CAS 消费，执行前重验权限与目标状态 | 一次性、不可重放 |
| 结果不确定 | 标 `unverified`，**不自动重试** | 跨 Worker/外部网络无事务保证 |
| 审计 | 复用既有业务审计，附 `conversationId / runId / callId` | 不新造审计体系 |
| 明确不做 | 通用 Shell、CP 任意文件读写、平台治理、秘密明文、数据库浏览、助手自身配置、自更新 | shared-contracts §2.6 |

## 4. 工具登记

### 4.1 复用与收窄

从 `agentActionCatalog` 派生助手工具目录，规则：

1. 只收录 `HTTPInContract` 为真的 action（契约内，路径与参数有据可查）。
2. 只收录 `Operation` 属于 `read` 或 `write`/`destructive` 的 action；`platform_governance` 类**永久排除**。
3. 工具对外名用稳定命名空间：`system.<action>`，避免与未来外部 MCP 连接的工具名冲突（FR-517）。
4. `read` 类：`RequiresConfirm=false`，自动执行。
5. `write` / `destructive` 类：**强制** `RequiresConfirm=true`。即使目录里标记为 false 也不放行——写必确认是本 FR 的不变量。

### 4.2 需补齐的只读工具

现有目录偏向运维动作，AI 首页与排查场景还缺三类受限读（shared-contracts §2.5 要求「尚无受限读路径的工具先不暴露」）：

| 缺口 | 说明 | 约束 |
|---|---|---|
| 集群概览摘要 | 节点在线数、实例状态分布、资源水位 | 只回聚合值，不回逐台明细；非管理员按可见范围聚合 |
| 任务列表（受限） | 当前用户有权看到的任务 | 按创建者/关联资源收敛；不可见即 404（沿原服务语义） |
| 告警事件（受限） | 未解决告警 | 目标按规则维度识别 node/instance，**不得把 targetId 一概当实例**（shared-contracts §2.5） |

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
  → 执行前重验：装载 UserAccess、校验 AuthVersion、动作权限、目标归属与当前状态
  → CAS 消费票据（write 路径）
  → 调用**原领域 service**（不复制业务逻辑）
  → 写回执 + 审计
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
| `owner_user_id` | uint，索引 | |
| `conversation_id` / `run_id` | uint，索引 | |
| `call_id` | string | 与事件里的 callId 对应 |
| `tool_name` | string | 命名空间名 |
| `tool_schema_version` / `policy_version` | int | 版本变化使旧确认失效 |
| `args_hash` | string | 规范化参数的 SHA-256（键排序后序列化） |
| `target_fingerprint` | string | 真实目标指纹（resourceType + ID） |
| `status` | string | `pending` / `approved` / `rejected` / `expired` / `consumed` / `invalidated` |
| `decided_at` / `expires_at` | datetime | 有效期内未决定即 `expired` |
| `consumed_at` | datetime，可空 | CAS 消费时间 |

`(run_id, call_id)` 唯一：同一调用不重复出票。

### 6.2 `assistant_receipts`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint PK | |
| `confirmation_id` | uint，唯一 | 一张票一份回执 |
| `outcome` | string | `ok` / `failed` / `rejected` / `expired` / `unverified` |
| `error_code` / `error_message` | string | 已脱敏 |
| `result_summary` | text | 有界摘要（上限见 §8） |
| `started_at` / `finished_at` | datetime | |

`unverified` 语义：已发出写调用但未取得确定结果（连接断开、超时等）。**禁止**把它当失败重试，也禁止当成功。

### 6.3 失效条件

以下任一变化使 `pending` 之外的旧票据不可复用：

- 用户 `AuthVersion` 变化（改密、改权、被禁用）
- 工具/schema 或策略版本变化
- 参数哈希或目标指纹不匹配
- 超过 `expires_at`
- run 已终止

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

- [ ] 组外用户调用越界工具：执行前重验拒绝，且**不产生任何副作用**（用副作用可观测的资源验证，不只看返回码）
- [ ] 实例读权不推导为整机写权：只读用户对节点级写工具被拒
- [ ] 写工具在未确认前零副作用；确认后按原参数执行成功
- [ ] 确认过期（>10 分钟）后 approve 被拒且不执行
- [ ] 确认后参数被改（模拟模型二次提交不同参数）→ 旧票据失效，返回 `CONFIRMATION_STALE`
- [ ] 同一票据重复 approve 只执行一次（CAS 生效）
- [ ] 用户权限在等待确认期间被撤销 → approve 被拒，不执行
- [ ] 模型/Agent 身份调用决策端点被拒（403）
- [ ] 写调用超时 → 回执 `unverified`，不自动重试，前端显示「结果待核实」
- [ ] 审计中能按 `conversationId / runId / callId` 串起一次完整调用
- [ ] 平台治理、秘密明文、任意文件、助手自身配置不在工具目录（用目录快照断言）
- [ ] 三类受限只读工具（概览/任务/告警）在无受限路径时**不出现在目录**中
- [ ] 既有 `/api/v1/mcp` 契约与 jmat 调用策略**零改动**（跑原有 MCP 测试全绿）

## 10. 未决 / 需用户确认

1. **与 FR-396 的关系**：现有 `RequiresConfirm` 只是目录标记，且各工具用「参数名精确确认」兜底。本 FR 引入统一票据后，是否**同时**把 jmat 通道的危险操作也迁到票据机制？当前设计是「只做聊天切片，不动 jmat」（shared-contracts §5.2 明写不顺手改旧策略），但这会让平台存在两套危险操作确认语义。
2. **只读工具的执行频率**：是否需要给 read 类加「每 run 最多 N 次」上限，防止模型反复轮询制造负载。
3. **确认取消后是否保留票据记录**：当前设计保留（审计需要），仅标 `rejected`。
