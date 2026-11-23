# ADR-096: MCP 端点在 Streamable HTTP 路径上无状态化

- **日期**: 2026-09-28
- **状态**: accepted
- **关联**: FR-489 · 部分取代 ADR-077（决策 5 与两条后果）· 修订 ADR-080（决策 6 的论证）· 增强 ADR-076

## 上下文

CP 内嵌 MCP 网关（ADR-077）把「会话」做成了协议一等公民：`initialize` 创建内存会话并以响应头 `Mcp-Session-Id: mcps_<hex>` 下发，后续请求必须携带；会话承载归属校验、空闲/绝对超时、全局与每 Token 并发上限、管理员列表/踢线，以及**初始化时的 principal 快照**。

这套模型在两个方向上都已构成实际障碍：

1. **通道脆弱**。会话本质是一个「服务端必须记得客户端」的契约：CP 进程重启（部署、崩溃、滚动升级）或会话空闲到期后，客户端手里的 `mcp-session-id` 即失效，后续请求得到 `404 SESSION_GONE`。要恢复，客户端**必须重新 `initialize`**——但主流 MCP 客户端（远程 IDE、NarraFork 内置 HTTP 客户端等）在收到该错误后不会自动重发 `initialize`，通道因此**永久卡死**，只能人工重连。ADR-077 的「后果」把「CP 重启会话全丢」记为可接受，实际效果却是一次静默的能力中断。
   - 同源问题已在两侧真机复现：Beacon 控制面的 MCP 端点因 5 分钟会话空闲超时导致桥接通道反复卡死，已按同一方向修复（见其 SEP-2567 无状态化改造）。
2. **与规范方向相悖**。MCP 的会话无状态化提案 SEP-2567《Sessionless MCP via Explicit State Handles》已合并，官方 Go SDK v1.8.0 已提供对齐实现（`StreamableHTTPOptions.Stateless`）。无状态是 MCP 远程传输的既定演进方向，继续加深会话耦合是逆流。

同时，会话快照引入了一处**语义缺口**：`tools/list` 与 `tools/call` 的能力判定取的是**初始化时**写进会话的 principal 快照，而每请求到达时其实**已经**由 `middleware.AgentAuth` → `AgentTokenService.Authenticate` 重新解析过一次 principal（该链路会校验吊销与过期）。两者不一致时以快照为准，意味着 **Token 被吊销、scope 被下调之后，既有会话仍按旧能力放行**，直到客户端重新 `initialize` 才收敛。

## 决策

1. **Streamable HTTP 路径（`POST /api/v1/mcp`）去会话**：不再下发也不要求 `Mcp-Session-Id`；每个请求独立鉴权、独立处理；`initialize` 直接返回 `initializeResult()`。
2. **授权一律取每请求重建的 principal**，不再有 principal 快照。鉴权链不变（仍只认 Agent Token `jmat_`；人类 JWT 不得充当 MCP 凭据），策略仍唯一落在 CP（ADR-076）。
3. **移除协议会话的全部运维语义**：空闲/绝对超时、超时巡检、全局与每 Token 并发上限、会话列表与踢线、`SESSION_GONE` 与超限 429 响应，以及 `mcp.session.open/close/kick` 流水。
4. **`GET /api/v1/mcp` 与 `DELETE /api/v1/mcp` 均改为 405 + `Allow: POST`**（前者无会话可保活，后者无会话可终止）。DELETE 由「不注册 → 通用 404」改为显式 405，是遵循 Streamable HTTP 规范对「服务端不支持客户端终止会话」的建议——回通用 404 会让客户端误判为路径写错而反复重试。
5. **保留 SSE 兼容路径**（`GET /api/v1/mcp/sse` + `POST /api/v1/mcp/message`）。它的连接态是**传输固有**而非协议会话——`/message` 必须把工具结果回推到 `/sse` 那条已建立的 HTTP 响应流上，这需要一个连接登记与回推通道。故该状态收敛为 **SSE 传输连接登记**（连接 id / principal / IP / 回推通道 / 取消），不承载超时、并发上限、能力快照等协议会话语义，连接断开即注销。
6. **运维视图随之从「会话维度」改为「Token 维度」**：`agent_call_logs` 已是每 Token 每次调用的权威流水（FR-390），按 Token 聚合即可回答「谁在用 MCP、最近何时用、用了什么、失败多少」，且**不依赖任何进程内状态**。原管理员会话端点（列表/踢线）由活动视图端点取代。

## 理由

- **无状态消除了故障类别本身，而不是缓解它**：没有服务端会话，就没有「会话失效」这一状态，CP 重启与长时间空闲都不再能中断通道。调大超时只能延后发作。
- **每请求授权更正确**：Token 吊销与 scope 调整立即生效，与会话内快照脱钩，消除上述语义缺口。这与 ADR-080 的目标（策略热更新一致性）同向且更强——不再需要「快照不漂移」的论证，因为不再有快照。
- **运维视图换到 Token 维度后更耐用**：进程内会话列表在 CP 重启后即为空，只反映「当前连着谁」；调用流水聚合反映的是**实际行为历史**，跨重启连续。
- **SSE 连接态的保留是必要而非妥协**：它是该传输能工作的前提，且 FR-389 已把 SSE 作为兼容路径承诺；删除它是削减既有能力，也与 SEP-2567 无关——该提案要移除的是协议会话，不是传输连接。
- 方向与 MCP 规范演进一致，避免后续再次返工。

## 后果

- **配置项废弃**：`mcp.idle_timeout`、`mcp.absolute_timeout`、`mcp.max_global_sessions`、`mcp.max_sessions_per_token` 失去意义，随之移除；配置结构、示例配置 `configs/control-plane.yml` 需同步移除，且不得留下「配置里有但代码不读」的项（`docker-compose.yml` 本就未配置这些键，无需改动；全仓已无这四项残留）。
- **错误语义变化**：`404 SESSION_GONE` 与超限 429 不再出现；无凭据仍 401；策略拒绝仍是 HTTP 200 + `result.isError=true`（不变）。
- **流水变化**：`mcp.session.open/close/kick` 不再产生，`agent_call_logs` 只剩调用类 action。
- **客户端兼容**：符合 MCP Streamable HTTP 的客户端无需改造即可受益；**携带旧 `mcp-session-id` 的调用会被忽略而非拒绝**，故存量客户端在会话失效后不再卡死，而是自动恢复正常。
- **管理员面变化**：`GET/DELETE /api/v1/agent/mcp/sessions` 由活动视图端点取代；前端「MCP 会话」页改为按 Token 聚合的活动视图（页面与路由随之改名，旧路径保留重定向）。
- **不再需要「会话随重启丢失」的说明**（ADR-077 的该条后果作废）。
- **文档同步面**：`ARCHITECTURE.md`（§1 全景 / §4.1 / CP 目录结构）、`API.md` MCP 章节、`docs/specs/cp-mcp-server/spec.md`、`docs/specs/agent-call-log/spec.md`、`docs/specs/agent-capability-policy-v2/spec.md`、PRD FR-388/389/391/439，以及 P1 批次摘要行。

## 备选（未采纳）

- **延长会话超时 / 调大并发上限**：只是把故障推远——客户端在会话失效后仍然不会重发 `initialize`；且与规范方向相悖。
- **要求客户端自动重连（收到 `SESSION_GONE` 后重发 `initialize`）**：需逐个改造生态内所有 MCP 客户端，把兼容责任推给外部；服务端无状态化对客户端零要求。
- **连同 SSE 兼容路径一并删除**：SSE 的连接态是传输固有，删除它削减 FR-389 已承诺的能力，收益为零。故只删协议会话语义、保留传输连接登记。
- **保留会话但仅供 SSE 使用，Streamable HTTP 复用同一会话对象**：会把协议会话语义（超时、上限、快照）重新渗回传输层，正是本 ADR 要消除的耦合。

## 关系

- **部分取代 ADR-077**：其决策 5（会话内存可运维）与「后果」中的会话超时/并发上限、「CP 重启会话全丢可接受」两条被本 ADR 取代；决策 1（CP 内嵌）、2（仅 Agent Token）、3（协议适配 only）、4（工具集动态裁剪）、6（FR-386 处置）**仍然有效**。
- **修订 ADR-080**：其决策 6 关于「MCP 会话中的 principal 快照不会因策略热更新产生不一致」的论证不再需要——无状态化后每次请求都重新解析 principal，天然一致。
- **增强 ADR-076**：策略真源与硬拒绝面不变，且因每请求授权而更强。
- FR-489（本决策落地）、FR-390（活动视图的数据源，本 ADR 不改其定义）、FR-389（SSE 兼容路径的承诺仍有效）。
