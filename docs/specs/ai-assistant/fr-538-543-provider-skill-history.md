# 供应商协议/发现、系统提示词、思考强度、自带 MCP、历史检索与回退（FR-538 ~ FR-543）

> 状态：实现中　·　关联 PRD：FR-538（P0）/ FR-539（P0，已交付）/ FR-540（P0）/ FR-541（P1，部分交付）/ FR-542 / FR-543（P1）
> 依赖：FR-514（供应商与模型）、FR-515（对话运行时）、FR-516（工具与确认）、FR-517（外部 MCP）

## 1. 背景

第一批（FR-514~520）把「能配、能聊、能调工具、能确认」跑通了，真机联调也过了。
但在真实使用中暴露出五类**配置面缺口**：模型名只能手抄、代理只能全局、自带工具默认不生效、
系统提示词与思考强度无处可配、历史只能看不能搜不能改。本批逐项补齐。

## 2. FR-538：协议三选与模型自动发现

### 2.1 协议三选

`assistant_providers.protocol` 取值 `openai-chat` / `openai-responses` / `anthropic`，
三者在 `ai.NewStreamer` 与前端下拉里**都已存在**（FR-514 已落）。本项只补契约与回归：
新增用例断言三值均被 `ai.ProtocolSupported` 接受、且非法值在保存期被拒。

### 2.2 模型自动发现

`POST /assistant/admin/providers/:id/discover-models`

按协议请求上游列表端点：

| 协议 | 端点 | 解析 |
|---|---|---|
| `openai-chat` / `openai-responses` | `GET {baseURL}/models` | `data[].id` |
| `anthropic` | `GET {baseURL}/models?limit=1000` | `data[].id` |

出站走**该供应商自己的** egress 客户端（复用 FR-539 的代理与准入），
凭据复用 `resolveCredential`——发现与聊天必须走同一条出站策略，
否则会出现「聊天被准入拦住、发现却能连通」这种自相矛盾的状态。

**返回结构**：`{discovered: [{modelId, alreadyConfigured}], total, truncated}`。

**边界（必须写进实现注释与错误码）**：
- 上游无列表端点或返回非 200 → `upstream_rejected` 分类，**不静默返回空列表**
  （空列表会让管理员以为「这个供应商没有模型」，方向完全错）。
- 单个模型 ID 超过 256 字符 → 跳过并计入 `skipped`，不整单失败。
- 发现**只读**：不自动落库。导入由管理员在前端勾选后走既有 `POST /admin/models`，
  以便逐个确认 `contextWindow` / `tools` 等能力——这些能力不能从名字猜。

## 3. FR-541（补齐）：系统提示词与思考强度

### 3.1 系统提示词

`assistant_settings.system_prompt`（TEXT，可空）。空时用内置基线
`assistantChatSystemPrompt`。组装顺序固定：

```
[内置基线] + [自定义系统提示词] + [已启用技能]
```

三层各自可为空；全空时只剩基线。**改动只对新 run 生效**：run 在提交时冻结
`systemPrompt` 快照到 events 之外不落库（本轮不冻结，遵循「提示词随配置走」），
但历史消息内容不会被追溯改写。

### 3.2 思考强度

`assistant_models.reasoning_effort`（可空，取值 `minimal`/`low`/`medium`/`high`）。
为空表示不指定（由上游默认）。`ai.Request` 增 `ReasoningEffort string`，按协议映射：

| 协议 | 映射 |
|---|---|
| `openai-chat` | 顶层 `reasoning_effort` |
| `openai-responses` | 顶层 `reasoning: {effort: ...}` |
| `anthropic` | `thinking: {type: "enabled", budget_tokens: N}`，N 由档位映射（1024/2048/8192/16384），且必须 `max_tokens > budget_tokens` |

**关键约束**：`anthropic` 的 `thinking` 与 `temperature` 互斥，且启用后响应含
`thinking` 内容块——必须**跳过而非报错**（否则一旦开思考强度，所有 Anthropic 请求全挂）。
未知档位一律**不发送**该参数（不发比发错安全：发错直接被上游 400）。

## 4. FR-540：自带 MCP 工具默认启用（进程内直连）

### 4.1 为什么不是「配一条指向自己的连接」

FR-517 的外部 MCP 连接走 HTTP + Agent Token，并对指向本平台的地址做 `MCP_SELF_LOOP` 拒绝
（防管理员误配成回环依赖）。让助手用同一路径接自己，需要伪造 Token、处理回环超时、
并给自环检测开一个例外——安全性净下降。

本项改为**进程内直连**：助手工具执行器直接调用平台自己的工具注册表，
不经 HTTP、不需要 Token、天然无回环。

### 4.2 暴露范围与默认值

- 默认**启用**（用户要求「默认启用本项目自带的 mcp 工具」）。
- 可经 `assistant_settings.builtin_tools_enabled` 关闭（bool，默认 true）。
- 工具可见性沿用 FR-516 规则不变：`read` 自动执行；`write` / `unknown` 逐调用确认。
- **不扩大权限**：每次执行前仍重跑完整权限判定；工具只做调用者本就有权做的事。

### 4.3 与既有 system.* 工具的关系

既有 `system_<action>` 工具来自 `agentActionCatalog` 的契约内 action（13 个）。
本项不重复造一套，而是把「平台自带工具」这个开关做成**统一入口**：
关闭时连 `system_*` 也一并不可见（否则「关掉自带工具」只关掉一半）。

## 5. FR-542：历史页与检索

### 5.1 后端

`GET /assistant/conversations?q=<kw>&cursor=&limit=` 增 `q`：
- 命中**标题**或**任一消息正文**（`LIKE %kw%`，转义 `%` `_` `\`）；
- 只返回该用户的会话（会话归属过滤不可省，否则检索变成越权读）；
- 结果按 `updated_at` 倒序，游标分页；
- **不返回消息正文**（列表只回摘要，正文在详情里取）——否则一次检索会把大段内容拖进响应。

### 5.2 前端

新增 `/assistant/history` 页：搜索框（防抖）+ 列表（标题/时间/消息数/摘要）+ 打开会话。
复用既有 `AssistantChatPanel` 继续对话。

## 6. FR-543：消息编辑与回退（内容层）

### 6.1 语义边界（用户已决策）

**只回退会话内容，不撤销已发生的副作用。** UI 必须显式提示这点：
回退不等于把「已启动的实例」停回去。副作用撤销需要为每个工具定义逆操作，
且部分操作（重启、发命令）本质不可逆，另立 FR 处理。

### 6.2 编辑用户消息

`PATCH /assistant/messages/:id`

- 只允许改**自己会话里的 user 消息**（assistant/tool 消息不可编辑）；
- 写入时保留原文（`original_content` + `edited_at` + `edit_count`），
  不覆盖原始内容——审计与「改了什么」必须可回溯；
- 编辑后**不自动重跑**：由用户显式再次发送（否则一次编辑会意外产生两条回复、两次计费）。

### 6.3 回退

`POST /assistant/conversations/:id/rollback` body `{messageId}`

- 把 `conversation_id` 下**该消息之后**的消息标记为 `superseded_at`（**不物理删除**）；
- 之后读取上下文时只取未被取代的消息；
- 若该会话有活跃 run → 409（不能在生成中途回头，否则事件 seq 会错乱）；
- 审计 `assistant.conversation.rollback`，记录 from/to 消息 id；
- **可再次回退**到更早的位置（幂等方向：只增不减 superseded）。

### 6.4 为什么是软删除而不是硬删除

工具调用消息关联着 `assistant_receipts` 与平台审计。硬删会让「这条副作用是谁批准的」
失去线索；软删保留可查性，代价是查询要带 `superseded_at IS NULL`。

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-544 助手工具渐进式披露与执行路径统一（feat，依赖 FR-516/517，需 spec

**需求补充**：`docs/specs/ai-assistant/fr-538-543-provider-skill-history.md`）：把「工具定义」收敛为**单一真源 = MCP 目录**，并让助手按需探索而非全量预载。①**渐进式披露**：常驻上下文只下发三个元工具——`search_tools`（按关键词/分类查有哪些工具）、`describe_tool`（取某工具参数 schema）、`call_tool`（真正调用）；实测常驻 schema 1350 字节，为全量 108 工具 45088 字节的 **3%**（原 13 工具为 4306 字节），且**与工具总数无关**。②**执行路径统一**：`call_tool` 内部转给 MCP 的 `CallTool`，审批拦截/能力判定/scope 校验/精确确认/调用流水全部复用，助手不再有第二条执行实现；确认票据与权限重验所需的工具元数据同样从 MCP 派生。③**双闸**：在调用 MCP **之前**判定读写——写类出助手侧确认票（与 Agent 客户端 CallTool 前拦截同构），读类直放；真机验证请求「启动实例」后生成确认卡且实例状态未变。④**身份按用户 RBAC 派生**（不造全能力替身）：白名单映射权限节点→agent 能力，未列出的不给，助手做不了用户本人做不了的事。⑤**页面上下文改为只带引用**（页面 + 实体类型#id + 标签页，不含名称/状态/指标），并首次真正注入模型（此前仅落库）。**边界**：外部 MCP 工具名改为可逆编码 `conn_<slug>_<remote>`（字面下划线转 `_5f`、非法字节转 `_XX`），因点号违反上游字符集 `^[a-zA-Z0-9_-]{1,64}$`。

**交付状态**：✅ 已交付·真机验证：调用序列为 search→describe→call，模型自发跨域探索（查完实例顺 nodeId 查节点）；`bot_send_command` 经 call_tool 出助手确认卡且批准前零副作用；`cluster_overview`/`list_tasks`/`list_alerts` 三个受限只读已补为正式 MCP 工具（原实现随工具真源统一而删除，MCP 面缺等价项）。service 全包 121s、mcp 全绿、CP 可构建

### FR-538

供应商协议三选与模型自动发现

🔨 代码完成待真机验收（2026-10-25）：`model/business_event_cursor.go`（无 payload）+ `service/business_cursor_writer.go`（有界环 + 批量 500/1s + 丢最旧计数 + Close flush）+ `service/business_cursor.go`（缺口查询 + 保留期裁剪）+ `router/business_cursor.go`（`GET /business/cursor`）；`business_stream.go` 的 `Ingest` 仅在投递之后追加一次非阻塞入队，**原投递语义与性能零改动**。契约已进 `docs/openapi`（`check-openapi.sh` 17/17 一致）。**实测修掉一个真实缺陷**：GORM 在 SQLite 上会静默忽略 `Delete` 的 `Limit`（生成无 LIMIT 的 DELETE），原「分批删除」形同虚设、一次删整段过期数据即长事务锁库 → 改为「先查 id 再按 id 批删」，并补一条超过单批上限的用例盯着。**未验证**：真实 MySQL 上的裁剪行为与批量写吞吐；真机 100 实例 × 20 事件/秒压力下的表体量与写入延迟（估算 7 天约 12 亿行，ADR-107 已指出需分区/归档，本轮未做）。

