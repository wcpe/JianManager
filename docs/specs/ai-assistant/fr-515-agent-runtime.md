# 功能规格：Go 内置轻量 Agent 与私有会话（FR-515）

> 状态：草拟 / 待用户审核，**未实现**（本 FR 无任何代码）　·　关联 PRD：FR-515　·　范围：仅 FR-515
> 共享契约见 [shared-contracts.md](shared-contracts.md)，本文不复述；只补 FR-515 需要冻结的具体表、状态迁移与接口。
> 前置：FR-514（模型配置与流式适配）**已实现配置面**，见 [../ai-provider-models/spec.md](../ai-provider-models/spec.md)。

## 1. 背景与目标

FR-514 解决了「模型从哪来」，但还没有任何人能真正对话——没有会话存储、没有 run 状态机、没有事件流。FR-515 交付**对话运行时本体**：一个 Go 内置的轻量 Agent，把「用户消息 → 模型流 → 工具调用（交 FR-516 执行）→ 结果回灌 → 下一轮」这个循环跑起来，并把全过程持久化成可续读的事件。

范围内：会话/消息/run/event 四张表的持久化、run 状态机与唯一活跃槽、模型流式调用与工具循环的编排、事件写入与补读、取消与预算、进程重启后的中断标记。

范围外：工具的实际执行与确认（FR-516）、外部 MCP（FR-517）、页面上下文采集（FR-518）、聊天 UI（FR-519）、AI 首页（FR-520）。本文只定义运行时与数据，不定义任何具体业务工具。

## 2. 现状核实（只读结论）

- **CP 已有配置面与模型适配地基**：`internal/controlplane/router/assistant.go` 已注册 `/api/v1/assistant/models`（全部登录用户）与 `/api/v1/assistant/admin/*`（管理员），挂载点见 [internal/controlplane/router/router.go](../../../internal/controlplane/router/router.go#L627)；`svcs.Assistant == nil` 时整组端点关闭（同文件 L128 注释）。
- **本域无任何会话/run/event 表**：`database.AutoMigrate` 目前只追加到既有模型集（[internal/controlplane/database/database.go](../../../internal/controlplane/database/database.go#L117-L135)），助手现有三表（Provider/Model/Setting）在 L237-L239，**没有**助手会话/run/event。
  注意别把「仓库里没有」说成绝对：**其它域已有会话与事件表**（Bot 会话、运行事件在 [database.go](../../../internal/controlplane/database/database.go#L181-L189)，另有其它运行表 L223-L226），只是都不属助手域、不能复用其语义。
- **已有可复用的授权真源**：`UserAccess` 含 `UserID / Role / AuthVersion / IsPlatformAdmin / RoleKey / Nodes / AdminGroupIDs / MemberGroupIDs / AccessibleGroups`（[internal/controlplane/service/authz.go](../../../internal/controlplane/service/authz.go#L59-L69)）。运行时**不缓存**该结构做跨轮次复用，每轮重新装载。
- **Agent Token 是独立授权链**：`AgentPrincipal`（[internal/controlplane/service/agent_token.go](../../../internal/controlplane/service/agent_token.go#L275-L283)）带 `ScopedInstanceIDs / ScopedNodeIDs / WriteAllowlist`，与人类 RBAC 不同源。助手不得构造该结构（ADR-105 决策 2）。
- **无持久队列可用**：`TaskService.RunAsync` 是后台 goroutine，不是恰好一次语义（见 shared-contracts §5.2 末条）。run 的推进不能依赖它。
- **数据库只有 SQLite**：不承诺 MySQL；迁移须过「首启 → 停 → 二启 → 三启」与脏库验证。

## 3. 冻结的共同选择

| 项 | 冻结值 | 理由 |
|---|---|---|
| 运行时位置 | CP 进程内 goroutine，随请求生命周期之外独立推进 | 纯 Go、单二进制（ADR-105 决策 1） |
| 会话归属 | `ownerUserId` 单列外键，**所有**读写按它校验 | 私有会话，组管理员也不是他人会话所有者 |
| 活跃 run | 每会话**一个**槽，由数据库唯一约束保证而非进程内锁 | 重启、并发请求与未来扩容下进程内锁都不可靠 |
| **部署形态** | **V1 只支持单 CP 实例**（见 §8.1） | 启动回收器会误伤其它实例的活跃 run，除非先建租约机制 |
| 事件顺序 | 每 run 单调 `seq`，先落库后发布 | 断流补读与去重都依赖它 |
| 流式传输 | `fetch` + Bearer 的 SSE，不用 `EventSource`（不把 JWT 放 URL） | shared-contracts §8 |
| 模型调用 | 复用 FR-514 的适配器抽象，不新写协议 | 避免两套 SSE 解析 |
| 工具循环 | 单轮最多 `MAX_TOOL_ROUNDS` 次工具往返，超出则强制收尾 | 防模型无限调用 |
| 上下文裁剪 | 按 token 预算裁剪历史，保留 system + 最近消息 + 工具结果摘要 | 无裁剪会撞供应商上限 |
| 计费 | 只记录供应商真实 usage，缺失记 null，**不估算** | shared-contracts §4.1 |
| 明确不做 | 多 Agent 编排、RAG/向量库、跨会话共享上下文、run 迁移到其它会话 | ADR-105 后果段 |

## 4. 数据模型

四张新表，随 `database.AutoMigrate` 加性建表。所有时间列存 UTC。

### 4.1 `assistant_conversations`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint PK | |
| `owner_user_id` | uint，索引 | 归属用户；所有查询强制带此条件 |
| `title` | string | 会话标题，1..200；首轮由用户消息截取，可手改 |
| `model_id` | uint，可空 | 选定模型（平台 Model ID，非上游 modelId）；空则用默认模型 |
| `active_run_id` | uint，可空 | 当前活跃 run；与 `assistant_runs` 的会话唯一约束互为校验 |
| `deleted_at` | datetime，可空 | 软删除；用户只能删自己的 |
| `created_at` / `updated_at` | datetime | |

### 4.2 `assistant_messages`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint PK | |
| `conversation_id` | uint，索引 | |
| `run_id` | uint，可空，索引 | 该消息由哪次 run 产生；用户消息为 null |
| `role` | string | `user` / `assistant` / `tool` |
| `content` | text | 可见正文。`role=tool` 时存**有界结果**（上限见 §7 `TOOL_RESULT_MAX`），超出部分不入库只标 `truncated` |
| `tool_call_id` | string，可空 | 与 run 内 callId 对应，`role=tool` 时非空 |
| `turn` | int | 轮次序号，从 1 递增 |
| `state` | string | `complete` / `interrupted` / `truncated` |
| `created_at` | datetime | |

### 4.3 `assistant_runs`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint PK | |
| `conversation_id` | uint，**唯一索引**（与 `status IN ('queued','running','waiting_confirmation')` 组合的部分唯一约束） | 保证单会话单活跃 run |
| `owner_user_id` | uint，索引 | 冗余存一份，便于免连表校验归属 |
| `model_snapshot` | text(JSON) | 本轮模型配置快照（协议/上游 modelId/能力），**不随配置变更回写** |
| `tool_policy_version` | int | 本轮工具策略版本，确认校验用 |
| `context_snapshot` | text(JSON)，可空 | 页面上下文快照（FR-518 写入） |
| `status` | string | 见 §5.1 |
| `termination_reason` | string，可空 | `completed` / `cancelled` / `failed` / `timed_out` / `interrupted` / `budget_exhausted`（工具轮次或 token 预算耗尽后强制收尾） |
| `usage_input` / `usage_output` | int，可空 | 供应商真实用量；缺失为 null |
| `idempotency_key` | string，可空 | 提交幂等键。**非空时**与 `owner_user_id` + `conversation_id` 组成**数据库唯一约束**（不只是普通索引）；`request_digest` 用于异参比较，**不进唯一键** |
| `request_digest` | string，可空 | 与 idempotency_key 配对的请求摘要 |
| `version` | int | 乐观并发 |
| `created_at` / `started_at` / `ended_at` | datetime | `started_at` / `ended_at` **可空**：排队的 run 没有开始时间，未终态的 run 没有结束时间。不用零值时间代替——那会把「未开始」与公元 1 年混为一谈 |

### 4.4 `assistant_events`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint PK | |
| `conversation_id` | uint，索引 | |
| `run_id` | uint，索引 | |
| `seq` | int | 每 run 单调递增；`(run_id, seq)` 唯一 |
| `type` | string | 见 §5.3 |
| `message_id` / `call_id` | uint / string，可空 | 关联锚点 |
| `data` | text(JSON) | 有界载荷，单条上限见 §7 |
| `created_at` | datetime | |

## 5. 状态机与事件

### 5.1 run 状态

```
queued → running ↔ waiting_confirmation → completed
                                      → failed
                                      → cancelled
                                      → timed_out
                                      → interrupted（进程重启回收）
```

- 所有状态迁移走 CAS（`WHERE id=? AND version=?`），失败回 409。
- 终态不可复活。`interrupted` 只能由启动时的回收器写入。
- `waiting_confirmation` 由 FR-516 触发；本 FR 只保证该状态在状态机中可达且可被取消。

### 5.2 单会话单活跃 run 的实现

不能只靠进程内锁（重启后内存态丢失；进程锁在扩容后直接失效）。用**数据库部分唯一索引**：

```sql
CREATE UNIQUE INDEX ux_assistant_runs_active
  ON assistant_runs(conversation_id)
  WHERE status IN ('queued','running','waiting_confirmation');
```

SQLite 支持部分索引。创建 run 时若冲突，返回 409 `CONVERSATION_BUSY`，并附当前活跃 runId 供前端接续观察（而不是报一个无解的错误）。

实现注意（已落地，勿"优化"）：`conversation_id` **不能**加 GORM `uniqueIndex` tag——那会变成永久唯一，
一个会话一辈子只能有一个 run，历史 run 全被挡住。必须是带 `WHERE` 谓词的部分唯一索引，
在 `AutoMigrate` 之后手工建（幂等）。活跃态列表由单一真源函数拼装，不要在索引 SQL 里另写一份字面量，
否则两处漂移会让索引静默放过冲突插入。

### 5.3 事件类型（最小集）

| type | data 载荷 | 说明 |
|---|---|---|
| `text_delta` | `{text}` | 增量文本；前端追加而非替换 |
| `tool_call` | `{callId, name, argsJson}` | 参数已通过 schema 校验 |
| `tool_status` | `{callId, status, errorCode?}` | `pending_confirmation` / `running` / `ok` / `failed` / `rejected` / `expired` / `cancelled` / `unverified` |
| `tool_result` | `{callId, preview, truncated}` | 结果预览（有界，供 UI 折叠展示）。**全文不另存**——超出 `TOOL_RESULT_MAX` 的部分直接丢弃并标 `truncated` |
| `usage` | `{input, output}` | 供应商报告值；缺失则不发此事件 |
| `context_warning` | `{dropped: [{type,id,reason}], message}` | FR-518 引入：上下文中有非法/无权/超限项被剔除时发一条，**不阻断 run** |
| `error` | `{code, message}` | 已脱敏分类错误 |
| `run_end` | `{status, reason}` | 每 run 恰好一条，终态事件 |

事件 `seq` 从 1 连续递增，不允许空洞（空洞会让前端补读游标无法判定是否漏收）。

## 6. 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/assistant/conversations` | 当前用户会话列表，分页（`cursor`/`limit`），按 `updated_at` 倒序 |
| `POST` | `/api/v1/assistant/conversations` | 新建会话，body 可带 `title`、`modelId` |
| `GET` | `/api/v1/assistant/conversations/:id` | 会话详情 + 最近 N 条消息 |
| `PATCH` | `/api/v1/assistant/conversations/:id` | 改标题 / 换模型 |
| `DELETE` | `/api/v1/assistant/conversations/:id` | 软删除自己的会话 |
| `GET` | `/api/v1/assistant/conversations/:id/messages` | 消息分页（`before`/`limit`） |
| `POST` | `/api/v1/assistant/conversations/:id/runs` | 提交一轮，**202** 返回 `{runId}`；支持 `Idempotency-Key` 头 |
| `GET` | `/api/v1/assistant/runs/:runId/events` | 补读事件，`?after=<seq>`；返回 JSON 数组 |
| `GET` | `/api/v1/assistant/runs/:runId/stream` | SSE 观察流（fetch + Bearer） |
| `POST` | `/api/v1/assistant/runs/:runId/cancel` | 停止本轮 |

统一错误码：`CONVERSATION_BUSY`（409）、`RUN_NOT_FOUND`（404，含越权——不区分「不存在」与「无权限」）、`IDEMPOTENCY_CONFLICT`（409）、`MODEL_UNAVAILABLE`（503）、`INVALID_CURSOR`（400，见 §6.2）、`BUDGET_EXCEEDED`（402/429，见 §7）。

### 6.1 幂等

- `Idempotency-Key` 绑定 `(ownerUserId, conversationId)`，唯一性由**数据库唯一约束**保证
  （`owner_user_id + conversation_id + idempotency_key`，`idempotency_key` 为空时不参与）。
- `request_digest` 只用于判断「同键是否同请求」：同键同摘要 → 返回原 `runId`，不新建 run；同键异摘要 → 409 `IDEMPOTENCY_CONFLICT`。摘要**不进唯一键**，否则异参请求会各自插入一条，幂等就失效了。
- **判序固定**：先按唯一键查已有 run → 命中且摘要一致则返回原 run；未命中再判会话是否已有活跃 run（409 `CONVERSATION_BUSY`）；最后才插入。
- **插入冲突即回读**：插入若因唯一键冲突失败（两个请求同时未命中），不做「重试插入」，而是回读已存在的那条并按摘要规则返回。这样即使 A 已结束并释放了活跃槽，B 也不会插入第二条。
- 无该头时不保证幂等（前端必须带）。

### 6.2 补读与过期

**三个概念必须分开，不得互相推导**（此前版本把它们混为一谈，是设计缺陷）：

| 概念 | 定义 | 上限依据 | 是否会导致 410 |
|---|---|---|---|
| **存储保留** | 库里 `assistant_events` 的物理留存 | 永久（§10.1） | **否** |
| **观察补读窗口** | 客户端用 `?after=<seq>` 能拉到的区间 | 存储保留范围（即全部） | **否**——事件永不删，游标不会因过期而失效 |
| **模型外发窗口** | 喂给模型的历史条数 | `HISTORY_WINDOW`（200 条） | **否**——它只影响「模型看到多少」，与 API 无关 |

- `after` 为上次收到的最大 `seq`；返回该 run 中 `seq > after` 的事件。
- **`CURSOR_EXPIRED`（410）不再由清理触发**。仅保留一种情形：`after` 大于该 run 的 `maxSeq`（伪造或跨 run 误用游标），此时返回 400 `INVALID_CURSOR` 而非 410。
- 若查询的 `runId` 不属于当前用户或不存在，统一 404（不区分，避免探测）。
- 客户端断流后按 `seq` 补读；补读不得重新提交 run、不得重放工具（§5.3）。

## 7. 预算与上限

| 项 | 默认值 | 语义 |
|---|---|---|
| `MAX_TOOL_ROUNDS` | 8 轮 / 16 次调用 | 单轮 run 内工具往返上限，超出→强制收尾（`budget_exhausted`） |
| `MAX_OUTPUT_TOKENS` | 由模型能力声明（默认 2048） | 单次模型调用输出上限 |
| `RUN_WALL_CLOCK` | **300 秒** | run 总运行预算；**等待人工确认的时间单独计时、不计入** |
| `MODEL_REQUEST_TIMEOUT` | 120 秒 | 单次模型请求 |
| `IDLE_STREAM_TIMEOUT` | 90 秒 | 供应商无任何字节 → 判失败，不无限等 |
| `EVENT_PAYLOAD_MAX` | 32 KiB | 单条事件 `data` 上限，超出截断并置 `truncated` |
| `HISTORY_WINDOW` | 200 条 | **单次外发窗口**（喂给模型的历史条数上限），非保留上限——库里全量留存，见 §10.1 |
| `CONTEXT_TOKEN_BUDGET` | 由模型能力声明 | 超预算时按「system → 最近消息 → 工具结果摘要」优先级裁剪 |
| `TOOL_RESULT_MAX` | **64 KiB** | 单条工具结果入库上限（与 shared-contracts §10「工具结果 64KiB」一致）；超出丢弃并标 `truncated`，不另存全文 |
| `CONCURRENT_RUNS_PER_USER` | **2** | 同一用户的活跃 run 上限（跨会话）；超出 429，见 shared-contracts §10 |

预算是**服务端硬约束**，不依赖前端传值。所有默认值与 [shared-contracts §10](shared-contracts.md) 保持一致；若两处冲突，以 shared-contracts 为准并回头修正本表。

### 7.1 历史装配必须保留结构化工具调用

回灌历史时，assistant 的工具调用**必须作为结构化项外发**，不能降级成文本。每个 callId 的 `tool_call`
与对应的 `tool` 结果必须**成对、同序**出现；缺任一半即视为不可回灌（该轮次整体丢弃并记 `context_warning`），
不得只发结果或只发调用。

**适配器侧的前置条件已就绪**（2026-10-23）：FR-514 的 Responses 适配器原先会把工具调用降级成普通文本、
不产出 `function_call` 项，导致 `function_call_output` 无法配对；该缺陷已修复（提交 `e93a4739`，
见 `docs/specs/ai-provider-models/spec.md` §7.1）。因此本 FR 不再需要先改适配器，但**上面两条约束仍必须遵守**：
一旦上层把工具调用塞进 `Content` 文本，适配器再怎么正确也救不回来——配对靠的是 `Message.ToolCalls`
（`CallID`/`ToolName`/`Args`）与 `role=tool` 消息的 `ToolCallID`，两者缺一即无法回放。

## 8. 重启与中断

启动时执行一次回收（幂等）：
1. 所有非终态 run 置 `interrupted`，写 `termination_reason=interrupted`，补一条 `run_end` 事件。
2. 处于 `waiting_confirmation` 的确认全部置为失效（不得自动执行）。
3. **仅把「已派发、但没有确定终态回执」的写调用标为 `unverified`**，不重放。
   判据是回执 `outcome` 为空（或仍为 `in_flight`）——**已经落定为 `ok` / `failed` 的回执一律不动**：
   一个仍在 `running` 的 run 完全可能已经成功完成前面的写工具，把确定结果改成「待核实」
   会让用户凭空看到一条假的「结果未知」，反而掩盖真实状态。
4. 清理 `active_run_id` 指向已终态 run 的会话。

### 8.1 部署形态：V1 只支持单 CP 实例（**冻结**）

上面的「启动即回收全部非终态 run」与「多 CP 实例」**不能同时成立**：第二个实例启动时会把第一个实例
仍在正常工作的 run 全部标成 `interrupted`，并释放它正在使用的会话槽。两者必须选一个，本 FR 选后者让位：

- **V1 明确只支持单 CP 实例**。同一数据库上**不得**同时运行两个 CP 进程。
- 这不是「暂时没做」而是**有意的边界**：要让回收器在多实例下安全，必须引入 run 所有者标识 + 租约续期 +
  心跳失效判定 + fencing generation（防止旧所有者在租约过期后仍写入），是一整套分布式协调机制，
  与本 FR「轻量运行时」的定位冲突，**不在本 FR 范围**。
- 落地要求：
  1. 启动回收器只在自己**确认是唯一实例**时执行；实现上用数据库侧的一行启动锁（如 `assistant_settings`
     上的一次性 `instance_epoch` 条件更新）或部署文档明示「单实例」——**二者取其一，不要既不锁又在文档里含糊**。
  2. 单测与验收**不得**用「两个 CP 实例并发」来验证单活跃 run（§9 原第 1 条已改为数据库层并发写）。
  3. 若将来要多实例，前置条件是上述完整租约机制，且必须重新评审本节与 §8 的回收语义。
- 补充：数据库唯一索引仍然必不可少——它挡的是**同一实例内的并发请求**与**重启后的残留槽**，
  与实例数无关，删掉它并不能「简化」。

## 9. 验收

- [x] 同一会话并发提交两次 run，第二次返回 409 `CONVERSATION_BUSY` 并带当前 runId；验证方式为**直接操作数据库**制造冲突（并发 insert 同一 `conversation_id` 的活跃 run），确认由唯一索引而非进程内锁拦住——**不要用两个 CP 实例**（V1 单实例，见 §8.1）
- [x] 同 `Idempotency-Key` 同摘要重复提交只产生一个 run；异摘要返回 409
- [x] 用户 A 无法读写用户 B 的会话：列表不含、详情 404、提交 run 404、订阅事件 404（四条路径都验）
- [x] 组管理员同样无法访问他人私有会话
- [x] 事件 `seq` 连续无空洞；按 `after` 补读不重不漏
- [x] 事件**永不因保留策略被删**：跑满 500 条事件后仍可用 `after=1` 拉回全部，不出现 410
- [x] `after` 大于该 run 的 `maxSeq` → 400 `INVALID_CURSOR`（不是 410，也不是空数组）
- [x] 模型外发窗口达 200 条后，喂给模型的历史被裁剪，但**观察补读仍能拉到全部**（两者互不影响）
- [x] 停止 run：模型流中断、未派发工具不执行、待确认被撤销；已由业务系统接受的任务不被回滚
- [x] 工具往返超过 `MAX_TOOL_ROUNDS` 时强制收尾：状态落 `completed`，`termination_reason=budget_exhausted`，并补一条 `run_end` 事件说明「工具轮次已达上限」，不无限循环
- [x] 供应商零字节 90 秒后判失败，不挂死（由 `assistant_live_test.go` 的 idle 相关路径与单测覆盖）
- [x] 进程重启后：非终态 run 变 `interrupted`、待确认不可执行、写回执标 `unverified`
- [x] 迁移过「首启 → 停 → 二启 → 三启」与脏库；`-race` 通过
- [x] 事件载荷超 32 KiB 被截断并标 `truncated`，前端能提示「结果已截断」
- [x] **真实供应商链路**：用真实上游跑通「多轮对话 → 工具调用 → 结果回灌 → 收尾」全流程（凭据由环境注入；缺失时在本项如实标注「未验证」，不得标记通过）
- [x] 所有登录用户（含组管理员、平台管理员）都能建立自己的会话，且互不可见

### 9.1 补齐记录（2026-10-27）

本轮把原先「实现正确但没有护栏」的三条补上了用例，均已勾选：

- **组管理员不能访问他人私有会话** → `TestAssistantChat_GroupAdminCannotAccessOthersConversation`
  把成员提升为 `RoleGroupAdmin` 并重新登录（角色变更需重新签发 token），断言会话详情与
  消息列表仍是 404 + `CONVERSATION_NOT_FOUND`。语义要点：会话归属只认 `owner_id`，
  组管理员能管组内资源，但助手会话是个人私有数据，不在组管辖内——若将来把 owner 检查
  换成「同组即可读」，这条会立刻变红。
- **事件永不因保留策略被删** → `TestAssistantChat_EventsNeverPruned` 直接插 501 条事件后
  以 `after=1` 补读，断言能拉回 500 条且不返回 410。（插 501 而非 500：`after=1` 语义是
  `seq > 1`，会排除 seq=1；这是游标语义，不是裁剪。）
- **所有登录角色都能建立自己的会话** → `TestAssistantChat_AllRolesCanCreateOwnConversation`
  普通成员 / 组管理员 / 平台管理员各建一个会话，断言三者拿到不同会话且各自列表只含自己那条。
  防的是「某天给建会话加角色白名单」——那会让低权限用户彻底无法使用助手。

## 10. 已定 / 未决

### 10.1 已定（2026-10-23 用户确认）

**会话记录永久保留 + 用户手动删除。**

- 依据：聊天记录含「模型建议 → 用户确认 → 工具执行」的完整链，是操作追责材料；按条数滚动清理会在事故回溯时缺失关键上下文。
- 落地影响：
  - `assistant_messages` / `assistant_events` **不做定期清理**；`HISTORY_WINDOW` 语义从「保留条数」改为「**单次外发窗口**」——只截取最近 200 条喂给模型，但库里全量留存。
  - 游标补读**永远不会因数据被删而失效**（事件永久保留）；`CURSOR_EXPIRED` 已废弃，改用 `INVALID_CURSOR`（400）处理非法游标。
  - 用户 `DELETE` 自己会话 = 软删会话及其消息；不提供「批量清空全部历史」以外的运维接口。
  - 存储增长需监控：单会话消息数无硬上限，按实际使用观察；若需配额，作为独立 FR。

### 10.2 未决

1. **模型切换粒度**：会话级 `model_id` 已冻结；是否允许在**同一会话内逐轮换模型**（当前设计允许，但会让 `model_snapshot` 逐轮不同）需确认。

> 并发上限已不再列为未决：按 shared-contracts §10 冻结为「同会话 1 个、同用户 2 个」，见 §7 `CONCURRENT_RUNS_PER_USER`。

### 9.1 真实供应商链路的现状（2026-10-23）

`TestAssistantLiveProvider` / `TestAssistantLiveProviderProbe` 默认**按约定 skip**（列出缺失环境变量、不 panic）。
本轮已对**自托管 OpenAI 兼容端点**真跑通过：假供应商是真 TCP 监听 + 真 SSE 分帧，验证覆盖
「加密→落库→解密→注入鉴权头→受保护出站→HTTP→SSE 多帧重组→usage 解析→tool_call 参数累积」。

**仍算未通过的部分（不要当成已验证）**：任何真实商业厂商（鉴权差异、限流、字段微调、`tool_choice` 行为）；
`openai-responses` 与 `anthropic` 两个协议完全未跑（假供应商只实现 openai-chat）；非流式与多工具并行调用未纳入。

本轮在此过程中修掉两个真实缺陷：①`runModelTest(withTools=true)` 的提示词是「回复 pong」，
真实模型不会因此调用工具，于是把「没被要求调用」误判成「模型/协议不支持工具」，把管理员引向换模型；
②live 断言要求「文本非空**且**工具已解析」，对遵循指令的上游不可满足（工具模式正文为空），已按验证意图分开。

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-515 Go 内置轻量 Agent 与私有会话（feat，需 spec，依赖 FR-514；spec

**需求补充**：`docs/specs/ai-assistant/fr-515-agent-runtime.md`）：CP 内实现流式多轮对话、工具调用循环、会话/消息/运行/有序事件持久化、取消与预算；不增加助手进程。验收：所有登录用户仅能访问自己的会话，同会话互斥，断流可恢复观察但不重放调用，停止/失败/重启有可解释终态，真实供应商链路通过。

**交付状态**：✅ 已交付@v0.24.0
