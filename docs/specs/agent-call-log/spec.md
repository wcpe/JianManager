# 功能规格：Agent 调用流水与 Token 活跃

> 状态：开发中（spec 已审，实现中）　·　关联 PRD：FR-390　·　依赖：FR-384　·　关联 ADR：076　·　关联：FR-389（client=mcp）、FR-385（client=jmagent）

## 1. 背景与目标

现有审计仅覆盖 Token 签发/吊销与部分**写**运维；读路径无记录；`lastUsedAt` 有 bump 但无调用量；无法区分 MCP / CLI / curl。  

**目标**：统一 **agent 调用流水**（读+写），支持按 token/action/client 查询；Token 活跃字段可运营；为 FR-391 观测 UI 提供 API。

**阶段**：P0 · Agent 可观测地基。

## 2. 需求（要什么）

### 范围内

- 表 `agent_call_logs`（名称可微调，GORM 模型固定）：
  - `id`、`created_at`
  - `token_id`、`token_name`（冗余快照，吊销后仍可读）
  - `action`（与 `service.AgentAction*` 或 MCP session 事件对齐，如 `agent.whoami`、`agent.instance_start`、`mcp.session.open`）
  - `client`：`mcp` | `jmagent` | `curl` | `unknown`（优先 `X-JM-Agent-Client`，缺省 unknown；MCP 传输路径强制 `mcp`）
  - `transport`：可选 `streamable_http` | `sse` | `http` | 空
  - `target_type`、`target_id`（可空）
  - `success`（bool）、`error`（截断短文，禁 Token 明文）
  - `latency_ms`（uint）
  - `ip`（varchar）
  - 索引：`(token_id, created_at)`、`(created_at)`、`(action, created_at)` 视查询需要
- **写入点**：
  1. 所有 `/api/v1/agent/*` Ops（含 whoami/list/get/metrics/logs/start/stop/restart/maintenance）——成功与策略拒绝（403）均记；鉴权失败（401）可不记或记 `token_id=0`（推荐：**仅成功鉴权后**记，避免爆破刷库）
  2. MCP tool call（FR-389）：每 tool 一条，`client=mcp`
  3. MCP 会话 open/close/kick（可选但推荐）
- **不**把流水当通用人类审计替代；写操作可**同时**保留现有 `audit` 记录（detail 含 actorKind=agent），流水专注 agent 调用分析
- 查询 API（平台管理员 JWT）：
  - `GET /api/v1/agent/call-logs?tokenId=&action=&client=&success=&from=&to=&page=&pageSize=`
  - 响应分页 + 稳定排序 `created_at DESC`
- Token 活跃：
  - 继续在 `Authenticate` bump `last_used_at`（已有）
  - `GET /api/v1/agent/tokens` 列表项增加：
    - `lastUsedAt`（已有字段确保序列化）
    - `callCount24h`（只读聚合，可缓存短 TTL 或实时 count）
- 保留：默认 **14 天**（可配置 `agent_call_log_retention_days`）；启动或定时清理过期行
- 客户端约定：文档规定 `X-JM-Agent-Client: jmagent|mcp|…`；未知值归 `unknown`（长度限制，防注入）

### 不做

- 不存 tool 入参/出参全文（可另期抽样）
- 不做图表大盘聚合 API（24h count 仅 Token 级）
- 不改人类用户审计主键模型为大重构（可选后续加 `actor_kind` 列；本期流水表独立即可）
- 不在 FR-390 删除 mcp-bridge（FR-392）

## 3. 设计（怎么做）

### 3.1 模块

- `model.AgentCallLog`
- `service.AgentCallLogService`：`Record(...)`、`List(filter)`、`Count24h(tokenID)`、`PurgeExpired`
- 中间件或 Ops/MCP handler 统一 `defer` 记流水（避免每个 handler 复制）
- 解析 client：`middleware` 读 header，写入 `gin.Context`，Record 时取出

### 3.2 与审计关系

| 类型 | audit 表 | agent_call_logs |
|---|---|---|
| token create/revoke | ✅ | 可选 |
| agent 写操作 | ✅（现有） | ✅ |
| agent 读操作 | ❌ | ✅ |
| MCP tool | 可选 | ✅ |

### 3.3 性能

- 同步写 SQLite：单条 insert；失败只打 WARN，**不阻断**主请求
- 清理：每日或启动时 `DELETE WHERE created_at < ?` LIMIT 批删

无需新 ADR（归属 ADR-076 审计/可观测增强）；若实现时引入独立「双写审计」争议可补 ADR-XXXX。

## 4. 任务拆分

- [x] model + AutoMigrate + 保留清理
- [x] Record/List/Count24h service + 单测
- [x] Agent Ops 全路径接入流水（读+写+403）
- [x] Token 列表 API 附 `callCount24h` + 确认 `lastUsedAt` JSON
- [x] 查询 API + 单测过滤/分页
- [x] 文档：API.md、PRD FR-390→开发中；jmagent 默认 `X-JM-Agent-Client: jmagent`
- [ ] **真机**：远程 jmagent whoami +（若 389 已合）MCP tool 各至少 1 条流水，client 可区分（本 worktree：集成测覆盖 List/Count/client；远程标待真机验）

## 5. 验收标准

1. whoami / list nodes 成功后流水有记录，`action` 正确，`client` 在设置 header 时为 `jmagent`  
2. 写操作 403（scope/白名单）有流水且 `success=false`  
3. 未鉴权请求不产生海量流水（401 不刷库）  
4. `GET call-logs` 可按 tokenId/action/client/时间过滤；非管理员 403  
5. Token 列表含 `lastUsedAt`、`callCount24h`  
6. 过期清理单测或集成测证明超期可删  
7. **真机**：现有远程 CP 或本机验收栈上产生 ≥2 条可区分 client 的记录（若仅 390 先合：curl + jmagent 即可）

## 6. 风险 / 待定

- 与 FR-389 并行：MCP 写入点在 389 合入时接 `Record`；390 先提供 service，389 agent 提示词写明「若 AgentCallLogService 非 nil 则记」  
- SQLite 高频写：MVP 可接受；过大再异步队列  
- `callCount24h` 实时 count 成本：Token 数量少时可接受  

## 7. UI 契约摘录（供 FR-391）

- **调用流水**页：筛 token / action / client / 成败 / 时间；表格列时间、token、action、client、IP、耗时、结果  
- Token 表：列「最近使用」「24h 调用」  
- 审计页：快捷「仅 agent」——可筛 action 前缀 `agent.` 或跳转流水页（实现择一）  
