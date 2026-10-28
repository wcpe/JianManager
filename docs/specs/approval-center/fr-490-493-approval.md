# 功能规格：平台级审批票据与执行器（FR-490 / FR-491 / FR-492 / FR-493）

> 状态：**spec 已立项，实现待开始**　·　关联 PRD：FR-490（P0）/ FR-491（P0）/ FR-492（P0）/ FR-493（P1）
> 四者是一条**依赖链**，必须按 490 → 491 → 492 → 493 顺序交付，不可并行。
> 本文件合并四份 spec：它们共享同一套票据与执行器，分开写会各写一份状态机。

## 1. 背景与目标

### 1.1 现状（只读核实结论）

| 事实 | 证据 |
|---|---|
| 平台**没有任何审批票据基础设施** | 全仓无 `ApprovalTicket` / `approval_tickets` / `type Approval` |
| MCP 危险操作目前只有「参数名精确确认」 | `internal/controlplane/mcp/tools_bot.go` 的 `requireExactConfirm("confirmBotName", ...)` |
| `agentActionCatalog` 里 `Operation=write` 只有 **5 条** | `agent_capability.go`：instanceStart / Stop / Restart、nodeMaintenanceEnter / Leave |
| 但真正高危的 MCP 工具**多数不在 catalog 里** | `file_issue_transfer_ticket`、`instance_send_command`、`instance_update_config`、`instance_binary_upgrade/rollback`、`provision_server`、`instance_snapshot_rollback` |
| 既有异步承载者可用 | `TaskService.RunAsync(spec, work)` 已承载 FR-441 的长操作与阶段进度 |
| 聊天侧另有一套确认票据 | `assistant_confirmations`（FR-516）；**与本案是两套**，见 §1.3 |

### 1.2 为什么需要它

MCP 通道（Agent Token）可以让一个 AI Agent 直接启动/停止实例、写文件、下发控制台命令。
当前唯一的门是「参数里写一遍目标名字」——**这是防手滑，不是防越权**：
Agent 拿到 Token 就能自己填对那个参数，等于没有审核环节。

平台需要的是「**高危操作在真正执行前，必须有一个人类点过批准**」，
且这个批准要留下可追溯的票据、证据快照与执行回执。

### 1.3 与 FR-516 聊天确认票据的关系（**不要合并**）

| | FR-516 `assistant_confirmations` | 本案 `approval_tickets` |
|---|---|---|
| 触发者 | 聊天里模型发起的工具调用 | MCP Agent Token 调用 / 面板高危操作 |
| 批准者 | 会话所有者本人（self-confirm） | 具备审批权限的人类（**不要求是申请人**） |
| 生命周期 | 10 分钟 | **24 小时** |
| 目的 | 防模型误操作 | 防 Agent 越权，含双人复核语义 |

**不复用的理由**：两者的批准人语义、有效期、权限判据全不同。合并会让
「谁能批」变成一处需要分支判断的地方，而这正是权限漏洞最常出现的位置。
但两者的**状态机形状**应当一致（都含 CAS 消费），差异只允许在语义层。

## 2. 冻结的选择（实施前不得改）

1. **票据状态机**：`pending` / `withdrawn` / `rejected` / `expired` / `executing` / `succeeded` / `failed`。
2. **过期 24 小时**（与 FR-516 的 10 分钟刻意区分：审批是跨班次的流程）。
3. **幂等键**：同一 `(owner, tool, target, payload_hash)` 在 `pending` 期间重复提交返回**同一张票**，不新建。
4. **并发 CAS**：消费票据必须 `WHERE status='pending'` 且检查 `RowsAffected`，不得依赖进程内锁。
5. **批准仅人类 JWT**：Agent Token 在**服务层**永久不可批准——不是靠工具列表隐藏。
   判据是「调用方身份是否人类」，不是「有没有 `approval.approve` 权限节点」。
6. **服务层失败关闭**：未登记审批的高危工具一律拒绝执行，而不是「没配置就放行」。
7. **按 action 粒度登记**，不按 `Operation` 分类：`instance_start` 与 `instance_stop`
   同属 `write`，但本项只要求 `stop` 进审批——按分类无法表达这个差异。
8. **证据快照**：票据落库时冻结目标、参数摘要、申请时的资源版本；执行前重校验，漂移即 `failed`。

## 3. FR-490：票据与状态机内核

### 3.1 数据模型

```
approval_tickets
  id, uuid
  applicant_user_id            -- 申请人（Agent Token 归属的用户）
  applicant_kind               -- human | agent（记录渠道，便于审计区分）
  token_id                     -- Agent 调用时记下是哪把 Token（可空）
  action                       -- 按 action 粒度，如 mcp.instance_stop
  target_type, target_id       -- 冻结目标
  payload_hash                 -- 参数规范化后的 SHA-256
  payload_frozen               -- 冻结参数原文（供批准人核对）
  evidence                     -- JSON：申请时的资源快照（状态、版本、名称等）
  reason                       -- 申请人填写的理由（必填）
  status                       -- 状态机取值
  idempotency_key              -- 唯一于 (applicant, action, target, payload_hash, pending)
  decided_by_user_id           -- 批准/驳回人（可空）
  decided_at, consumed_at, expires_at
  failure_reason               -- status=failed 时的原因
  created_at, updated_at
```

索引：
- `(status, expires_at)` —— 到期扫描与待审列表
- `(applicant_user_id, status)` —— `own.list`
- 部分唯一索引 `(applicant_user_id, action, target_type, target_id, payload_hash) WHERE status='pending'`
  —— 幂等（同 §2 第 3 条）

### 3.2 状态机（唯一真源）

```
pending ──apply 人撤回──→ withdrawn
   ├──人类驳回──→ rejected
   ├──超 24h──→ expired（惰性判定 + 巡检兜底）
   └──人类批准──→ executing ──→ succeeded
                        └────→ failed（领域执行失败或目标漂移）
```

- **只有 `pending` 可被决策**（批准/驳回/撤回）。
- `executing` / `succeeded` / `failed` 为终态之外的「已认领」态，**不可再决策**。
- `expired` 由两处产生：读取时的惰性判定（`expires_at < now`）与巡检任务兜底。
  惰性判定必须**同时回写状态**，否则列表与详情会出现「显示待审但点不动」。

### 3.3 接口（FR-490 交付面）

**申请人自查（对齐 Beacon 语义）**
- `own.list`：自己的申请列表（含状态），支持按 status 过滤与分页
- `own.get`：单张详情（批准人、决定时间、失败原因）
- `own.withdraw`：**仅 `pending` 可撤回**；非 pending 返回 409 并说明当前状态

**人类决策**
- `pending.list`：待审列表（按审批权限节点收敛，不是「所有人可见」）
- `approve` / `reject`：**仅人类 JWT**

### 3.4 权限节点（FR-493 的权限部分先在此定义）

- `approval.read`：查看待审与历史
- `approval.decide`：批准/驳回

**遵循既有权限树约束**（FR-089「deny 永胜、超管唯一」、FR-432 可配置权限树）：
新节点必须走既有权限服务体系注册，不得旁路。

## 4. FR-491：执行器与执行回执

### 4.1 认领语义（三步同事务）

1. CAS：`UPDATE ... SET status='executing', consumed_at=now WHERE id=? AND status='pending'`
   —— `RowsAffected==0` 即「已被别人认领」，返回 409，**不执行**。
2. 写执行回执（`outcome=in_flight`）。
3. 提交事务后才真正调领域 adapter。

**与 FR-516 的 `approveAndRun` 同构**，可直接参照它的三步实现；差异只有状态名与有效期。

### 4.2 执行前重校验

- **目标漂移**：执行时重新读目标（实例/节点），比对 `evidence` 里的版本快照；
  不一致即 `failed`，**不执行**，并保留批准事实与失败原因。
- **payload 未被篡改**：重算 `payload_hash` 与票据比对，不一致即 `failed`。
- 注意 `AuthVersion` 只是必要不充分：角色模板改权/成员移除不会递增它，
  因此若涉及权限判定，必须**重跑完整有效权限判定**（同 FR-516 §5.1 的教训）。

**实现要点（`service/approval_target_drift.go`）**：

| 事实 | 处理 |
|---|---|
| 校验什么 | 实例：存在性 + 节点归属 + `updated_at`；节点：存在性 + `updated_at` |
| 刻意不校验 | 「实例是否在运行」等随正常运维波动的事实——判它会让审批在真实节奏里频繁失败，反而促使人绕过审批 |
| 版本取值 | `updated_at` 纳秒（现有表无乐观锁列，零迁移成本）。复查条件：引入显式 `revision` 列后应改用它 |
| **version 的 JSON 形态** | **十进制字符串**。evidence 是 `text` 列，`map[string]any` 经一次 JSON 往返后数字全变 float64，而纳秒值（约 1.8e18）远超 float64 精确整数上限 2^53，往返必被舍入 → 「目标没变」也会判成漂移，票据执行永久失败。**不得改回数字**；也不退一步用毫秒（毫秒会把同一毫秒内两次真实写更新误判为「没变」，等于放行一次针对已变更目标的执行） |
| 旧票据无 evidence | **不阻断**（宁可放过一次无法校验的执行，也不让存量票据在上线瞬间全部失败）；取舍已写入代码注释 |
| 装配 | `main.go` 中 `approvalExec.SetTargetVerifier(service.VerifyTargetDrift(instanceSvc, nodeSvc))`；出票时由 MCP 拦截层调用 `CaptureTargetSnapshot` 冻结快照。**此前 `SetTargetVerifier` 只有定义、从未被调用，漂移校验实际不生效** |

### 4.3 异步承载：复用 `TaskService.RunAsync`

FR-491 明确要求：**优先复用 `RunAsync`，不另造调度**。理由是有两条互不相识的异步链路时，
「任务中心看到一个在跑，但没人知道它是什么」——排查成本极高。

做法：adapter 内部用 `RunAsync` 包住长操作，票据的 `executing → succeeded/failed`
由该任务的最终状态驱动。

### 4.4 执行回执

`approval_receipts`：`ticket_id, outcome(in_flight/ok/failed/unverified), error_code,
error_message, started_at, finished_at`。

`unverified` 用于「可能已产生副作用但未确认」（超时/连接中断）——**不得当成功，也不得自动重试**。

## 5. FR-492：高危工具接入（按 action 穷举）

### 5.1 登记方式

以 `agentActionCatalog` 为准穷举 + **MCP 侧单独维护一份高危名单**（因为多数高危工具不在 catalog 里）。
登记表是**代码常量**，不是 DB 配置：漏登记必须是编译期/测试期可见的，而不是运行期静默放行。

### 5.2 已知必须覆盖（PRD 点名的三类绕过口子）

| action | 为什么必须审 |
|---|---|
| `file_issue_transfer_ticket` | 签票后走流式上传即可写盘，是绕过 `file_write_text` 的**完整路径** |
| `instance_send_command` | 控制台命令等价任意代码执行，当前**连精确确认都没有** |
| `instance_update_config`（改 `startCommand`） | 等价「改成任意可执行命令」 |
| `instance_binary_upgrade` / `instance_binary_rollback` | 等价「换掉要执行的二进制」 |
| `provision_server`（远程 URL 二进制来源） | 等价「下载并执行任意二进制」 |
| `instance_snapshot_rollback` | 覆盖既有数据 |
| `instance_stop` | PRD 要求（`start`/`restart` 不要求——见 §2 第 7 条） |

### 5.3 失败关闭

- MCP 调用这些 action 时**只落票据**、返回 `requestId`、**零副作用**。
- 未登记但被判为高危的情况：`Operation=write` 且不在「免审白名单」内 → 拒绝并提示登记缺失
  （**不是放行**）。
- 免审白名单必须显式列全（如 `instance_start` / `instance_restart` 按 PRD 免审），
  未列即视为需审——默认拒绝。

### 5.4 三入口同策略（CLI / MCP / curl）

MCP 工具名与 agent 运维面 action 是两套命名（`instance_stop` vs `agent.instance_stop`）。
若只把拦截接在 MCP 的 `CallTool` 上，**同一个 Agent Token 改用 curl/CLI 就能跳过审批**——
审批机制只堵了一半门口。因此 HTTP 入口同样过闸（`AgentOpsHandler.gateApproval`）：

| 入口 | 需审 action 的响应 | 位置 |
|---|---|---|
| MCP | 200 + `{status:"awaiting_approval", requestId}` | `mcp/approval_intercept.go` |
| curl / CLI | **202 Accepted** + 同结构 | `router/agent.go` 的 `gateApproval` |

- action 映射规则只有一条：去掉 `agent.` 前缀后**必须命中登记表**才算需审
  （`service.AgentActionNeedsApproval`）；不猜、不模糊匹配。
- 两条入口的交集实际只有 `instance_stop`：HTTP/CLI 只暴露 5 个写 action，其余 18 条需审 action 仅在 MCP 面。
- 审批服务未装配 → **失败关闭**（HTTP 503 `APPROVAL_UNAVAILABLE`），与 MCP 侧同一取向。

## 6. FR-493：审批中心与权限节点

- 页面：待审列表（操作 / 目标 / 申请人 / 风险 / 原因 / 证据）+ 批准执行 / 驳回 + 历史追溯。
- 无权限账号访问 → 403（前后端都要拦，后端为准）。
- 证据快照要**可读呈现**：把 `evidence` JSON 渲染成「申请时 vs 现在」的差异对比，
  否则批准人只能看到一串 JSON，等于没有证据。

## 7. 验收

### FR-490
- [ ] 同 `(owner, action, target, payload)` 在 pending 期间重复提交 → 返回同一张票（断言票据行数不变）
- [ ] 撤回仅 `pending` 有效；对 `executing/succeeded/rejected/expired` 撤回 → 409 且不改状态
- [ ] 超过 24h 未决策 → 读取即为 `expired` 且**状态已回写**（不是只显示过期）
- [ ] **Agent Token 调批准端点 → 403**（在服务层拦，绕过工具列表直接打 HTTP 也应被拒）
- [ ] 无 `approval.decide` 权限的人类 → 403
- [ ] 并发批准同一张票：只有一个成功（CAS），另一个 409

### FR-491
- [ ] 批准后执行失败 → `failed` 且保留批准事实与失败原因（不吞错）
- [ ] 执行前目标被改（模拟证据版本不符）→ `failed` 且**零副作用**
- [ ] payload 被篡改 → `failed` 且不执行
- [ ] 执行超时 → 回执 `unverified`，不自动重试
- [ ] 长操作经 `RunAsync` 承载（任务中心能看到，且与票据状态联动）

### FR-492
- [ ] 对每个登记的高危 action：MCP 调用只落票、**零副作用**（用可观测副作用的资源验证）
- [ ] 未登记的高危 action → 拒绝（失败关闭），不是放行
- [ ] 免审 action 正常直接执行（不产生票据）
- [ ] `instance_stop` 需审、`instance_start` 不需审 —— **同属 write 但行为不同**（这是粒度断言）
- [ ] **三入口同策略**：`POST /agent/instances/:id/stop`（curl/CLI）→ 202 + `requestId`，
      且**实例状态未被改动**（零副作用）；同 Token 走 MCP `instance_stop` 同样需审
- [ ] **审批服务未装配 → 两条入口都失败关闭**（HTTP 503 / MCP isError），不放行

### FR-493
- [ ] 无权限账号访问审批中心 → 403
- [ ] 待审列表展示证据；批准后列表移除、历史可追溯
- [ ] 权限节点注册进既有权限树，`deny` 永胜语义不被绕过

## 8. 未决

1. **双人复核**是否需要（PRD 未提，Beacon 语义里是单人批准）。
2. **撤销已批准但未执行**（`executing` 之前）是否允许——当前设计为不允许。
3. 审批通知（邮件/IM）不在本批范围（FR-105 邮件通知仍在 V1.1）。
4. **Agent 面缺审批查询工具**（本轮核查发现，待决策）。
   `own.list` / `own.get` / `own.withdraw` 三个端点已实现，但挂在**人类 JWT 会话**组
   （`/approvals/own/*`），按登录用户归属限域；MCP 工具面 108 个工具里**没有**任何审批查询工具。
   因此 Agent 提交后**无法自行轮询**结果，与 FR-490 原文「使 Agent 提交后能轮询结果、
   闭环不断在盲等」存在差距。
   - 已做的处理：出票返回文案不再声称「用 own.get 轮询」（那会让调用方去调不存在的东西），
     改为如实说明「批准后由服务端执行，请稍后重新查询目标状态」。
   - 补齐方案（未实施）：新增 `approval_own_list` / `approval_own_get` / `approval_own_withdraw`
     三个 MCP 工具，按票据 `token_id` 严格限域（比按申请人用户更窄，避免同一人的多把 Token 互相可见）。
     代价是要扩 action 目录与 108 条工具契约投影，属独立改动。
