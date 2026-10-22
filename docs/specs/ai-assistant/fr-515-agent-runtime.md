# 功能规格：Go 内置轻量 Agent 与私有会话（FR-515）

> 状态：草拟 / 待用户审核，**未实现**（本 FR 无任何代码）　·　关联 PRD：FR-515　·　范围：仅 FR-515
> 共享契约见 [shared-contracts.md](shared-contracts.md)，本文不复述；只补 FR-515 需要冻结的具体表、状态迁移与接口。
> 前置：FR-514（模型配置与流式适配）**已实现配置面**，见 [../ai-provider-models/spec.md](../ai-provider-models/spec.md)。

## 1. 背景与目标

FR-514 解决了「模型从哪来」，但还没有任何人能真正对话——没有会话存储、没有 run 状态机、没有事件流。FR-515 交付**对话运行时本体**：一个 Go 内置的轻量 Agent，把「用户消息 → 模型流 → 工具调用（交 FR-516 执行）→ 结果回灌 → 下一轮」这个循环跑起来，并把全过程持久化成可续读的事件。

范围内：会话/消息/run/event 四张表的持久化、run 状态机与唯一活跃槽、模型流式调用与工具循环的编排、事件写入与补读、取消与预算、进程重启后的中断标记。

范围外：工具的实际执行与确认（FR-516）、外部 MCP（FR-517）、页面上下文采集（FR-518）、聊天 UI（FR-519）、AI 首页（FR-520）。本文只定义运行时与数据，不定义任何具体业务工具。

## 2. 现状核实（只读结论）

- **CP 已有配置面与模型适配地基**：`internal/controlplane/router/assistant.go` 已注册 `/api/v1/assistant/models`（全部登录用户）与 `/api/v1/assistant/admin/*`（管理员），挂载点见 [internal/controlplane/router/router.go](internal/controlplane/router/router.go#L627)；`svcs.Assistant == nil` 时整组端点关闭（同文件 L128 注释）。
- **尚无任何会话/run/event 表**：`database.AutoMigrate` 目前只追加到既有模型集（[internal/controlplane/database/database.go](internal/controlplane/database/database.go#L117-L135)），本 FR 需加性建表。
- **已有可复用的授权真源**：`UserAccess` 含 `UserID / Role / AuthVersion / IsPlatformAdmin / RoleKey / Nodes / AdminGroupIDs / MemberGroupIDs / AccessibleGroups`（[internal/controlplane/service/authz.go](internal/controlplane/service/authz.go#L59-L69)）。运行时**不缓存**该结构做跨轮次复用，每轮重新装载。
- **Agent Token 是独立授权链**：`AgentPrincipal`（[internal/controlplane/service/agent_token.go](internal/controlplane/service/agent_token.go#L275-L283)）带 `ScopedInstanceIDs / ScopedNodeIDs / WriteAllowlist`，与人类 RBAC 不同源。助手不得构造该结构（ADR-105 决策 2）。
- **无持久队列可用**：`TaskService.RunAsync` 是后台 goroutine，不是恰好一次语义（见 shared-contracts §5.2 末条）。run 的推进不能依赖它。
- **数据库只有 SQLite**：不承诺 MySQL；迁移须过「首启 → 停 → 二启 → 三启」与脏库验证。

## 3. 冻结的共同选择

| 项 | 冻结值 | 理由 |
|---|---|---|
| 运行时位置 | CP 进程内 goroutine，随请求生命周期之外独立推进 | 纯 Go、单二进制（ADR-105 决策 1） |
| 会话归属 | `ownerUserId` 单列外键，**所有**读写按它校验 | 私有会话，组管理员也不是他人会话所有者 |
| 活跃 run | 每会话**一个**槽，由数据库唯一约束保证而非进程内锁 | 多实例/多进程下进程锁失效 |
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
| `content` | text | 可见正文；tool 消息存结果摘要 |
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
| `termination_reason` | string，可空 | `completed` / `cancelled` / `failed` / `timed_out` / `interrupted` |
| `usage_input` / `usage_output` | int，可空 | 供应商真实用量；缺失为 null |
| `idempotency_key` | string，可空，索引 | 提交幂等键（用户+会话+请求摘要） |
| `request_digest` | string，可空 | 与 idempotency_key 配对的请求摘要 |
| `version` | int | 乐观并发 |
| `created_at` / `started_at` / `ended_at` | datetime | |

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

不能只靠进程内锁（CP 可能多实例，且重启后内存态丢失）。用**数据库部分唯一索引**：

```sql
CREATE UNIQUE INDEX ux_assistant_runs_active
  ON assistant_runs(conversation_id)
  WHERE status IN ('queued','running','waiting_confirmation');
```

SQLite 支持部分索引。创建 run 时若冲突，返回 409 `CONVERSATION_BUSY`，并附当前活跃 runId 供前端接续观察（而不是报一个无解的错误）。

### 5.3 事件类型（最小集）

| type | data 载荷 | 说明 |
|---|---|---|
| `text_delta` | `{text}` | 增量文本；前端追加而非替换 |
| `tool_call` | `{callId, name, argsJson}` | 参数已通过 schema 校验 |
| `tool_status` | `{callId, status, errorCode?}` | `pending_confirmation` / `running` / `ok` / `failed` / `rejected` / `expired` / `cancelled` / `unverified` |
| `tool_result` | `{callId, summary, truncated}` | 结果摘要，非全量（全量另存消息） |
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

统一错误码：`CONVERSATION_BUSY`（409）、`RUN_NOT_FOUND`（404，含越权——不区分「不存在」与「无权限」）、`IDEMPOTENCY_CONFLICT`（409）、`MODEL_UNAVAILABLE`（503）、`CURSOR_EXPIRED`（410，附恢复指引）、`BUDGET_EXCEEDED`（402/429，见 §7）。

### 6.1 幂等

- `Idempotency-Key` 绑定 `(ownerUserId, conversationId, requestDigest)`。
- 同键同摘要 → 返回原 `runId`，不新建 run。
- 同键异摘要 → 409 `IDEMPOTENCY_CONFLICT`。
- 无该头时不保证幂等（前端必须带）。

### 6.2 补读与过期

- `after` 为上次收到的最大 `seq`；返回 `seq > after` 的事件。
- 若 `after` 早于保留窗口（事件按会话保留最近 N 条，N 见 §7），返回 410 `CURSOR_EXPIRED`，body 带 `oldestAvailableSeq` 与「请重新拉取会话快照」的指引。**不伪装成「没有更多事件」**——那会让前端静默丢一段对话。

## 7. 预算与上限

| 项 | 默认值 | 语义 |
|---|---|---|
| `MAX_TOOL_ROUNDS` | 8 | 单轮 run 内工具往返上限 |
| `MAX_OUTPUT_TOKENS` | 由模型能力声明 | 单次模型调用输出上限 |
| `RUN_WALL_CLOCK` | 10 分钟 | 超时 → `timed_out` |
| `IDLE_STREAM_TIMEOUT` | 90 秒 | 供应商无任何字节 → 判失败，不无限等 |
| `EVENT_PAYLOAD_MAX` | 32 KiB | 单条事件 `data` 上限，超出截断并置 `truncated` |
| `HISTORY_WINDOW` | 保留最近 200 条事件/会话 | 超出后旧事件可清，游标随之过期 |
| `CONTEXT_TOKEN_BUDGET` | 由模型能力声明 | 超预算时按「system → 最近消息 → 工具结果摘要」优先级裁剪 |

预算是**服务端硬约束**，不依赖前端传值。

## 8. 重启与中断

启动时执行一次回收（幂等）：

1. 所有非终态 run 置 `interrupted`，写 `termination_reason=interrupted`，补一条 `run_end` 事件。
2. 处于 `waiting_confirmation` 的确认全部置为失效（不得自动执行）。
3. 处于 `running` 且已有写工具回执的调用，回执标 `unverified`（结果未知），**不重放**。
4. 清理 `active_run_id` 指向已终态 run 的会话。

## 9. 验收

- [ ] 同一会话并发提交两次 run，第二次返回 409 `CONVERSATION_BUSY` 并带当前 runId；进程内锁不是唯一防线（可用两个 CP 实例或直接操作 DB 验证）
- [ ] 同 `Idempotency-Key` 同摘要重复提交只产生一个 run；异摘要返回 409
- [ ] 用户 A 无法读写用户 B 的会话：列表不含、详情 404、提交 run 404、订阅事件 404（四条路径都验）
- [ ] 组管理员同样无法访问他人私有会话
- [ ] 事件 `seq` 连续无空洞；按 `after` 补读不重不漏
- [ ] `after` 指向已清理区间时返回 410 且带 `oldestAvailableSeq`，不返回空数组
- [ ] 停止 run：模型流中断、未派发工具不执行、待确认被撤销；已由业务系统接受的任务不被回滚
- [ ] 工具往返超过 `MAX_TOOL_ROUNDS` 时强制收尾并标 `terminated`，不无限循环
- [ ] 供应商零字节 90 秒后判失败，不挂死
- [ ] 进程重启后：非终态 run 变 `interrupted`、待确认不可执行、写回执标 `unverified`
- [ ] 迁移过「首启 → 停 → 二启 → 三启」与脏库；`-race` 通过
- [ ] 事件载荷超 32 KiB 被截断并标 `truncated`，前端能提示「结果已截断」

## 10. 未决 / 需用户确认

1. **会话保留策略**：`HISTORY_WINDOW=200` 是占位值，需确认是否需要「永久保留 + 手动删除」，还是「按条数滚动清理」。
2. **模型切换粒度**：会话级 `model_id` 已冻结；是否允许在**同一会话内逐轮换模型**（当前设计允许，但会让 `model_snapshot` 逐轮不同）需确认。
3. **并发上限**：shared-contracts §10 提「同用户 2 个活跃 run」，本 FR 只实现了「同会话 1 个」；同用户跨会话的并发是否需要额外限制需确认。
