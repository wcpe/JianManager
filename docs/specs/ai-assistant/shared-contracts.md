# AI 助手共享契约

> 状态：**跨批契约（部分已被 FR-514 实现验证）**。§1～§7 的边界仍然有效；
> FR-515～520 的详细规格未冻结，本契约**不是**已实现能力。
> FR-514 已按本契约落地配置面与协议适配（见 [../ai-provider-models/spec.md](../ai-provider-models/spec.md)），
> 其中 §4.1 的事件名、§2 第 7 条的管理员判据、§7 的出站规则已由代码实现；§7 的「管理员按 CIDR 放行」一项
> **仅实现了能力、未实现配置入口**（见该处标注）。
> 用户已确认：Go 内置、所有登录用户限自身权限、读取自动/写入确认、外部仅远程 Streamable HTTP、保留经典首页。

## 1. 范围与所有权

| FR | 职责 | 不承担的职责 |
|---|---|---|
| 514 | 供应商/模型/凭据、协议流式适配、管理员配置页 | 不执行任何业务工具 |
| 515 | 用户私有会话、消息、运行、事件、模型/工具循环 | 不解释某工具的领域权限 |
| 516 | 内置工具、当前用户委托、逐调用确认、业务审计 | 不放宽既有 jmat MCP 或平台审批 |
| 517 | 远程 MCP 客户端、连接/工具策略、上游信任边界 | 不启动本机子进程 |
| 518 | 类型化页面上下文、实体选择、发送快照 | 不读取整页 DOM，不授权资源 |
| 519 | 共享输入器、会话视图、全局浮窗、历史与确认卡 | 不在前端决定工具是否获准 |
| 520 | 默认 AI Dashboard、经典首页切换及运维摘要 | 不以模型代替实时指标 API |

Go 代码只在 CP 内新增模块，模型 HTTP 与外部 MCP 都由 CP 出站；游戏实例动作仍走原 service→Worker 反向隧道。不得复用 Bot Worker 运行助手，不增加 Node.js 服务。

业务视图留主控台应用，设计系统只增加确有通用价值的原语，不把会话、权限、MCP 或供应商业务并入组件库。

## 2. 身份与授权

1. 浏览器只向 CP 提交正常用户登录凭据。现有 `/api/v1/mcp` 仍只接受 jmat Agent Token；不新增 JWT 绕过路径，不构造假 AgentPrincipal。
2. 助手的 actor 是真实用户，执行器必须取得非空 UserAccess，每次读取/执行重新检查有效账号、授权版本、动作权限及资源归属；拒绝 nil access 的“全平台”隐式语义。
3. 复用 action 描述、真实目标解析与原领域 service，但不是直接照搬所有 MCP handler。现有固定 `(userID=0,isAdmin=true)` 的 adapter 不可服务普通用户。
4. 实例可见不代表可写，节点读写分别按现有节点权限，不把节点角色硬编码为超管，也不从实例权限反向推导整机权限。
5. 任务只查询当前用户有权看到的记录；告警目标必须按规则维度识别 node/instance，不能把 targetId 一概当实例。尚无受限读路径的工具先不暴露，不能用全局查询后前端过滤。
6. 平台治理、秘密明文、任意数据库浏览、自更新、助手自身配置不进入工具目录。用户输入不能新增权限或绕过禁区。
7. 供应商/模型/MCP 管理要求有效 UserAccess 的 IsPlatformAdmin，不用实际等同 node.manage 的旧辅助函数代替。
8. 普通用户只取得可用模型 ID、显示名、协议能力和默认模型，不取得端点、凭据引用、密文、连接私网策略或管理员诊断信息。
9. 会话、消息、流、run、取消与确认均先校验会话所有权；组管理员不是其他用户私有会话的所有者。
10. 新一轮装配资源上下文与历史工具数据时重新核验当前权限；已撤权的资源数据不能借历史回灌重新外发。已经由用户合法看到的消息不承诺远程擦除，用户可删除自己的历史。

## 3. 数据边界

### 3.1 平台配置

- Provider / Model 使用数据库内部 ID。原始上游 modelId 与平台 Model ID 分开，不用显示名作为调用标识。
- 协议固定为 `openai-chat`、`openai-responses`、`anthropic`。模型能力显式声明 streaming/tools，不能从名称猜测。
- 默认模型由一个单行配置引用表达；不允许多个 model 的 isDefault 布尔竞争。
- 配置有版本；同一模型变更不改写过去 run 的配置快照。供应商停用、凭据失效在后续模型轮次生效，不能悄悄换供应商外发。
- API Key / MCP 认证材料只在后端解析与注入，秘密存储采用环境变量引用或受控加密。前端留空保留、替换与清除必须三态明确。
- 环境变量引用限 `JIANMANAGER_ASSISTANT_*` 凭据命名空间，仅查进程环境，不扩展到文件读取或模板求值。

### 3.2 会话与执行（由 FR-515/516 冻结具体表）

- Conversation：ownerUserId、标题、选定模型、创建/更新时间、活跃 run 槽及版本；删除语义与运行中的处理要显式。
- Message：用户/assistant/tool 角色、可见正文、tool call ID 关系、轮次与完整/中断标记。
- Run：本轮用户请求、发送上下文快照、配置/工具策略版本、状态、终止原因和可空 usage。
- Event：会话/run、单调 seq、类型及有界载荷；只追加并在持久化成功后发布。
- Confirmation / Receipt：来源、策略、用户、调用、目标指纹、规范化参数哈希、策略/工具 schema 版本、期限、决定、执行认领与回执。
- 现有 agent_call_logs 是 Token 调用流水，不拿它替代用户私有聊天存储。内部用户委托写入用户可解释的业务审计，保留 conversation/run/call 关联。

存储使用 CP 业务库；目前可执行数据库驱动只有 SQLite，不能把文档目标当作 MySQL 已支持，本批不扩数据库后端。新增持久状态必须验证迁移、二启/三启及脏环境。

## 4. 统一模型与工具协议

### 4.1 模型适配器

接口概念：`Stream(ctx, Request, Emit) error`。

Request 至少含：模型配置快照、system 与角色消息、授权工具定义、已有调用/结果、输出限额。Emit 只产出归一事件：

- `text_delta`：本次增量文本；不保证供应商分片和中文字符边界相同。
- `tool_call_start` / `tool_args_delta` / `tool_call_end`：真实 callId（必要时在适配边界稳定生成）、索引、工具名与完整参数边界。
- `usage`：供应商报告的真实**输入/输出**量（`InputTokens` / `OutputTokens`，**无 total 字段**），缺失为 null；累计计费量与当前上下文占用分开。
- `end`：完成原因与供应商原始结束原因；全流最多一个终态。

**模型适配层没有 `error` 事件类型**（已实现，见 [internal/controlplane/ai/types.go](../../../internal/controlplane/ai/types.go#L34-L36)）：错误通过 `Stream(ctx, req, emit)` 的**返回值**表达，避免「既收到 error 事件又收到 error 返回」的双重语义歧义。

> **两套事件必须分清**：
> - **适配器事件**（本节）：进程内的 `Emit` 回调，只有上面四种，无 error。
> - **运行时事件**（FR-515 §5.3）：落库并推给浏览器的 `assistant_events`，**有** `error` 类型。
>   由 runner 把 `Stream` 返回的 error 转写成一条运行时 `error` 事件。
> 引用时不要说「适配器发 error 事件」，也不要拿运行时事件类型去要求适配器。

原始 SSE 片段不得直接当模型文本显示；工具参数片段不得执行。完整参数先验证 JSON 对象与 schema，再交执行器。FR-514 的真实测试只能验证协议及模型，不执行业务工具。

第一版不依赖供应商隐藏推理输出，也不支持多模态附件、图片生成、原生联网搜索、专有网关协议或跨供应商自动回退。

### 4.2 工具描述

共同描述至少包含：namespacedName、invokeName、source（system/connection）、action、inputSchema、read/write 风险分类、策略版本及可用作用域。

- 系统工具风险来自 CP 静态登记；外部 MCP annotations 只是提示，不能自己授予自动执行资格。
- 外部连接默认为禁执行；管理员显式启用工具并制定策略，未知风险默认确认。
- 参数、工具结果和外部描述都是不可信数据，不能覆盖 system 规则或发起自动批准。
- 不把所有工具 schema 每轮全量投喂；在服务器授权全集以内，按页面和本轮任务选择有界工具集。

## 5. 执行状态与确认

### 5.1 运行

建议状态：`queued → running ↔ waiting_confirmation → completed/failed/cancelled/timed_out/interrupted`。状态变化需 CAS/版本检查，终态不可复活。

- 同会话只允许一个活跃 run；数据库槽位而非仅进程内锁决定认领。
- 提交幂等键绑定用户、会话及请求摘要。同键同摘要返回原 run，同键异参 409。
- 消息中的每个完整 tool call 最终都有对应结果：执行成功、失败、拒绝、过期、取消或结果待核实。不留悬空配对。
- 模型输出明确结束且没有待执行工具时才完成；没有 usage 不阻止结束，但不能编造用量。
- 工具错误不能被模型润色成成功，页面展示原始可信执行状态与可读错误摘要。

### 5.2 确认与 FR-490～493

当前 FR-490～493 仍为计划，本批不伪称存在已交付审批中心，也不顺手改变旧 MCP 调用策略。

- FR-516 首先实现统一票据/回执抽象中的 `source=chat`、`policy=self_confirm` 切片，保留未来 `platform_gate` 策略；分类标记与 Operation 正交。
- self_confirm 仅确认“用当前用户本来就有的权限执行本次动作”，不授予平台批准权；要求平台级审批但尚无执行机制的动作先不开放。
- 聊天确认建议 10 分钟有效；FR-490 的平台审批 24 小时是不同策略，不互相覆盖。
- 冻结绑定：ownerUserId、conversationId、runId、callId、工具/schema/策略版本、规范化参数哈希、真实目标指纹与 expiresAt。
- 决定接口仅接受 approve/reject，不接受新的参数或目标。执行时重载当前权限及目标状态，再 CAS 消费并认领。
- 参数、目标、配置或工具策略改变使旧确认失效；拒绝、过期或已消费不得执行；Agent 和模型不能调用批准端点。
- 不能承诺数据库事务跨 Worker / 外部网络副作用。先持久化认领，执行后保存回执；发出写调用后连接断开等不确定情况标“待核实”，禁止无条件自动重试。
- TaskService.RunAsync 只能复用作已有业务任务入口，不能把其后台 goroutine 当作持久队列或恰好一次执行保证。

### 5.3 停止与恢复

- 明确停止：取消模型流与未派发工具、撤销本 run 的待确认。已经由业务系统接受的任务不会自动回滚。
- UI 收起/关闭不等于停止；首页/浮窗卸载不控制服务端 run 生命周期。
- 观察流中断后按 seq 补读，不重新提交 run；工具写入结果未知不因观察重连而重放。
- CP 重启后遗留非终态 run 记 interrupted，遗留确认不可自动执行；执行中写入根据回执标明待核实，不伪称取消已撤回远端动作。

## 6. 页面上下文与外发

结构化引用至少包含 pageKey、允许的实体引用、枚举页签、时间窗、必要筛选。实体通过类型与 ID 表达，名称由授权后端回填。

1. 当前页芯片对用户可见、可移除，可通过服务端搜索手动添加实例。
2. 发送时冻结；换页面只更新下一轮的“当前页”，不改写已发消息目标。
3. 页面 URL、query、用户传的名称和ID不构成权限凭据；发送时后端重新查实体和权限。
4. 日志/指标/错误只读授权范围与必要窗口，结果限长并脱敏；不自动注入完整日志、环境变量、密钥、Cookie 或 DOM。
5. 不把移除芯片后的背景继续作为隐藏上下文；界面注明选定模型所属供应商及“相关数据将发送给该供应商”。
6. 任务按其创建者/关联资源语义收敛；告警按真实 node/instance 维度；存在性隐藏沿原服务 404 语义。
7. 千级候选服务端搜索、防抖、有界首屏与截断提示；长会话与历史按页加载或虚拟化。

## 7. 外部连接边界

FR-517 首版只支持远程 Streamable HTTP：POST 的回复可为 JSON 或请求范围 SSE；不支持旧双端点 SSE、stdio、自动启动命令或 OAuth 动态注册。

- 共享外部管理员凭据不能自动映射为所有用户权限。无法验证上游用户/资源隔离的连接只对平台管理员可用；后续组用户委托须单独定义可验证适配。
- 凭据专属于连接；不转发面板 JWT、Cookie，也不把另一连接的认证信息带过去。
- 工具名加连接命名空间，同名不冲突。工具发现/策略变化使对应待确认失效。
- 供应商和 MCP 共用受保护出站规范：仅 HTTP(S)、无 URL userinfo/敏感 query，默认无重定向；默认拒绝私网；metadata 目的始终拒绝。
  **已实现**（[protected.go](../../../internal/platform/httpclient/protected.go)）：每次拨号解析并钉住已校验 IP（防 DNS rebinding/TOCTOU）、`ErrProxyUnsupported` 显式拒绝代理组合、`ErrBlockedTarget` 拒绝私网/回环/链路本地/元数据。
  **尚未实现**：① 私网放行只有 `AllowPrivate []netip.Prefix` 的 CIDR 列表，**没有 origin 绑定**；② `GuardOptions` 里**没有**响应头超时、流空闲超时、单行事件上限、响应体上限字段（SSE 单行 1 MiB 限制在 [ai/sse.go](../../../internal/controlplane/ai/sse.go#L28-L36)，不在 Guard）；③ 放行策略无持久化字段与管理 UI（只能改代码）。
  引用时不要把这些写成「已具备默认值」——预算类限制需按协议分别标定后才算落地。
  **落地现状**：规则本身已实现在 `internal/platform/httpclient/protected.go`（FR-514），
  但「管理员按 CIDR 放行」目前只有能力（`GuardOptions.AllowPrivate`）而**没有配置入口**，
  即无法通过管理面放行内网目标。FR-517 若需要接入自托管 MCP，须先补该入口。
- 校验覆盖每次 DNS 解析与实际拨号，固定已验证目标 IP 并保留原 Host/SNI；不能仅先解析检查再交默认客户端重解析。
- 先按原域名做 proxy/no_proxy，再实现 direct/CONNECT/SOCKS 的目标约束；无法保证目标固定的代理模式明确拒绝，不静默直连。代理自身为单独可信出站配置，不是 SSRF 防线。
- 单次调用失败/过量/超时只终止对应调用，不阻塞整个助手或其它连接。写结果不确定时不自动重试。

## 8. 浏览器 API 与事件方向

首批 FR-514 详细接口在独立 API 规格中定义；下列是 FR-515～519 的共享方向，后续详细规格仍须列完整 DTO 与错误码后才开发。

- `/api/v1/assistant/models`：全部已登录用户的可用模型目录。
- `/api/v1/assistant/admin/*`：仅管理员的供应商/模型/连接配置。
- `/api/v1/assistant/conversations/*`：用户自己的会话/历史/运行。
- 创建 run 使用 POST 返回 202 与 runId；观察采用带 Bearer 的 fetch SSE，不把 JWT 放 URL，不用 EventSource query token。
- run 的事件接口按 `after` 游标补读。事件框架：`{v,conversationId,runId,seq,type,messageId?,callId?,data}`。
- 事件类型至少覆盖文本增量、工具调用/状态/结果、待确认、用量、错误、run 终态。
- 事件先持久化再发布，按 seq 去重；过期游标给明确快照/恢复指引，而不是伪装成没有消息。
- 订阅、补读、取消、确认都重新校验用户与会话所有权。

## 9. 前端展示与路由

- `/` 按**记忆的模式**渲染（无记忆则 AI Dashboard）；`?home=classic` / `?home=assistant` 显式指定。优先级「显式参数 > 前端记忆 > 默认 AI」，记忆存前端 `localStorage` 且可重置（细节见 [fr-520-ai-home.md](fr-520-ai-home.md) §4.1/§4.2）。切换保留无关合法查询，两边都能反向切换。
- 只挂载当前模式的数据查询。AI 首页复用有界异常/告警/任务摘要，不拉完整实例表、资源趋势和健康墙。
- 新首页的主要内容：问候与提问框、模型/上下文、建议问题、需要关注、近期任务；桌面右栏为紧凑集群摘要与最近会话，窄屏改单栏。
- 外壳级只挂一个轻量 manager；会话状态、事件订阅与缓存按用户分区，首页与浮窗使用 selector 消费，不让每 token 触发 DashboardPage 整树重绘。
- 顶栏入口打开非模态右侧浮窗；支持收缩/关闭/展开/新会话/历史，移动端全屏。首版不做自由拖动和复杂窗口管理。
- 设置配置页建议 `/settings/ai`，由平台设置进入；直接深链也须管理员守卫。新增/编辑用现有自适应 Dialog，不能内联挤开原布局。
- 不同展示形态是同一会话、草稿及 run。切页/收缩不重新发模型请求；停止是单独操作。
- 登出/换用户立即释放订阅、草稿与前端缓存，调用来源及模型凭据不进入 localStorage。
- 全站保留原主题、5px 容器圆角、微元素档位、焦点环与轻阴影；不复制其它品牌图标与广告。不出现原生 textarea/全量 Select 回潮。

### 9.1 与既有经典首页的交接（2026-10-23 勘察）

现有首页挂在 `<Route index element={<OverviewPage />} />`（`components/console/Workspace.tsx`），是**整页替换**式挂载，没有模式开关。落地 FR-520 时按下述边界改造，避免把已完成的经典首页重做一遍（详细规格见 [fr-520-ai-home.md](fr-520-ai-home.md)，本节是它的事实来源）：

- **`OverviewPage` 即「经典模式」**，模式切换加在路由层而非页内：`/` 判 `?home` 决定渲染 `AssistantHome` 还是 `OverviewPage`，两棵树互不嵌套。
  准确的判据是：**查询必须随分支挂载**——若在共同父组件里无条件调用两套 hook，无论最终渲染哪个子组件，两套查询都会运行。路由层切换天然保证「只挂载当前模式的树及其 hooks」。
  （注意：页内条件渲染**也**能做到互斥，前提是子组件各自持有自己的 hooks；写成路由层是为了让这个约束由结构保证，而不是靠每个页面自觉。）
- **经典模式的查询构成**（已实测，勿重复造）：`useNodes`、`/metrics/overview`（totals + 趋势）、`/observability/overview`（健康快照 + Bot Worker）、`/observability/health-wall`（健康墙）、`/metrics/players/trend`、`/metrics/instances/ranking`、`/metrics/slo`、`/instances/search?status=CRASHED`（前 5）、`/tasks?limit=5`、`/alerts/events?resolved=false&pageSize=5`、`/metrics/resource-attribution`（仅在打开归因浮层时）。其中 **`/instances/search?status=CRASHED`、`/tasks?limit=5`、`/alerts/events` 这三项**是 AI 首页要复用的「有界异常/告警/任务摘要」，可复用现有 hook，不必新写；但**三者的权限收敛程度不同，`/alerts/events` 不能直接用于普通用户**，见下方缺口说明。（`/metrics/resource-attribution` 只在打开归因浮层时才请求，不属摘要复用范围。）
  （**更正**：此前把「`/metrics/overview` 的 totals」与「趋势图」混为一项——AI 首页只借 totals 概览，**不挂趋势图**；`/observability/health-wall` 同样不进 AI 首页。详见 FR-520 §5.2。）

  注意这里是两条不同路径，别混为一谈：**AI 首页渲染摘要**走的是前端既有 hook（浏览器带用户 JWT 直取）；**模型调工具**走的是 FR-516 的用户委托执行器（服务端按 `UserAccess` 重验，且要求有「受限读」的工具登记）。前者不需要工具登记，后者缺登记就**不进目录**——两者不互为前提。

- ⚠️ **三条摘要的「按权限有界取数」并不齐平，`/alerts/events` 是缺口**（2026-10-23 复核）：
  - `/tasks?limit=5`：**有**用户归属收敛（`service/task.go` 按当前用户过滤），可直接复用。
  - `/instances/search`：按既有实例权限收敛，可直接复用。
  - `/alerts/events`：**没有** scope 收敛。`router/alert.go` 调 `alertSvc.ListEvents(f)` 时**不传 `UserAccess`**，`service/alert.go` 亦明写「本端点没有 scope 收敛」并注明这是有意取舍（理由与实例名回填有关）。**因此它不能用来兑现 FR-520 的「告警按权限有界取数」验收。**
  - 处置（冻结）：AI 首页的告警摘要**先于**受限告警读路径落地之前，**只对平台管理员挂载**；普通用户不显示该区块（不是显示后前端过滤，也不是靠前端过滤「看起来合规」）。受限告警读（按 `UserAccess` 收敛的新查询面）作为 FR-520 的**明确前置任务**，另行实施并单独验收。
- **AI 首页明确不要的三项**：完整实例表（已在 2026-10-23 从经典首页移除，`useInfiniteInstanceSearch` 不再被首页引用）、资源趋势图、健康墙。若 AI 首页需要集群水位，用 `/metrics/overview` 的 totals 概览，不挂健康墙。
- **切换时的查询成本**：`?home=classic` ↔ `/` 切换会卸载并重新挂载另一棵树。缓存**按 `queryKey` 共享**（TanStack 语义），不是按页面模式天然隔离——两模式若用了相同 key 与相同 hook，切回时会直接命中缓存；若 key 不同则各存一份。设计时按这个真实规则判断，不要假设「模式切换 = 缓存独立」。

## 10. 默认预算（提请审核，详细 spec 可收紧）

| 项目 | 默认上限/语义 |
|---|---|
| 活跃运行 | 同会话 1 个、同用户 2 个；超额明确拒绝 |
| 工具循环 | 一次 run 最多 8 轮、16 次调用 |
| 模型请求 | 单次 120 秒，总运行 300 秒（等待人工确认单独计时） |
| 工具请求 | 单次 30 秒；已存在业务长任务改为返回 taskId 并查询，不无限挂住 |
| 确认 | chat/self_confirm 10 分钟；不等于 platform_gate 的期限 |
| 上游响应 | 单次网络解码体 2MiB，超量明确报错 |
| 工具结果 | 单条 64KiB，截断标记和回查引用；不假称内容完整 |
| 模型输入 | 序列化上下文 128KiB，上游配置输出 token 限额默认 2048 |
| 只读重试 | 瞬时故障最多 1 次；写操作绝不自动重试未知结果 |

字节上限是工程保护，不冒充精确 token 数。精确用量采用供应商真实 usage；未知 context length 不伪造百分比。超预算要提示缩小范围，不静默丢工具配对或关键历史。

## 11. 必须通过的验收

1. 三协议的真实模型文本流与工具协议可观测；需要的凭据由环境注入，无凭据不宣称真机通过。
2. 用户隔离覆盖 list/get/subscribe/continue/cancel/confirm；每工具都覆盖无读权、无写权与组外目标。
3. 未确认、驳回、过期、重复、变参、权限撤销、schema 改变、工具停用都不得产生额外写副作用。
4. 断网/重订阅/重复提交/停止/重启不重复派发工具；未知结果明确待核实。
5. 供应商凭据不出现在管理读取响应、普通模型目录、审计、错误、流、工具参数或模型上下文。
6. 外部 MCP 同名、不可达、超时、超量、非可信只读声明、SSRF 与跨用户上游权限均有负向证据。
7. 默认 AI/经典切换、当前页芯片、历史、新会话与浮窗在真实浏览器可操作；宽窄屏/明暗/键盘焦点覆盖。
8. 千级候选和长列表用浏览器 DOM 探针与网络请求双重佐证；不得只凭理论声称性能更好。
9. 持久状态按迁移→首启→停→二启→三启及脏环境验证。测试套件串行运行，不以 mock 当真实服务证据。

## 12. 审核与实施顺序

FR-514 的配置面、协议接入层与**探测端点**均已实现（配置侧已按本契约 §2 第 7 条 / §3.1 / §4.1 / §7 落地；
探测见 `docs/specs/ai-provider-models/spec.md` §8）。仍未做的是**真实上游验收**（三协议适配器均未与真实供应商联通）
与**跨重启凭据验证**，故 FR-514 在 PRD 中标为「开发中」而非「已实现」。
后续 FR-515～520 逐项补详细规格并复用这些边界；**未列完整 DTO 与错误码前不开发**。
七项详细规格现已全部写出：`fr-515-agent-runtime.md`、`fr-516-tools-and-confirmation.md`、
`fr-517-external-mcp.md`、`fr-518-page-context.md`、`fr-519-chat-ui.md`、`fr-520-ai-home.md`，
未决项汇总在 `open-questions.md`；全部经两轮独立复审，**待用户审核冻结**后再排实现顺序。
依赖顺序（PRD 已定）：514 → 515 → 516 → [517, 518] → 519 → 520。
界面 v0 只作设计验证，正式可用性必须以真实模型和工具闭环判断。

本批不做多代理、Shell、任意 CP 文件访问、工程工作区、Hooks、代码回滚、RAG、图片/附件、长期记忆、共享会话或计费平台。不动被用户暂停的分组头视觉，也不还原工作区其它改动。

## 附：PRD 表内原始记录（2026-10-26 迁入）

按「PRD 只保留一行 FR + 状态，详情归 spec」的约定，以下内容自 PRD 需求列迁入。

### FR-490 审批票据与状态机内核（feat）

**需求补充**：为 MCP 高危操作建立统一审批真源——票据状态机（`pending` / `withdrawn` / `rejected` / `expired` / `executing` / `succeeded` / `failed`）+ 冻结目标载荷与证据快照 + 24 小时过期 + 幂等键 + 并发 CAS + 强审计；**申请人自查与撤回能力对齐 Beacon 语义**：`own.list` / `own.get`（查询自己提交的申请与状态）/ `own.withdraw`（仅 `pending` 可撤回），使 Agent 提交后能轮询结果、闭环不断在盲等；**批准仅人类 JWT**，Agent Token 在服务层永久不可批准（不靠工具列表隐藏）。需修订既有「MCP 不做面板审批」结论（ADR-076/077 与 cp-mcp-server spec）

**交付状态**：✅ 已交付·**内核与真机验收均通过**：7 态状态机、24h 过期、幂等（数据库部分唯一索引）、并发 CAS、强审计、`own.list/get/withdraw` 三端点、Agent 不可批准（服务层永久拒绝）全部落地；18/19 个高危 action 完成真机闭环（出票零副作用 → 批准 → 副作用真实发生 → `approval.approved` + `approval.executed` 双审计），仅 `bot_send_command` 待建 Bot 记录后补验。**已知边界**：`own.*` 三端点挂在人类 JWT 会话组、按登录用户限域，MCP 工具面无审批查询工具，故 Agent 无法自行轮询——与正文「使 Agent 提交后能轮询结果」有差距，记为未决项（见 spec §8.4，补齐需扩 action 目录与 108 条工具契约投影）

### FR-514

AI 供应商、模型与平台配置

（**配置面与协议接入层已落地**：`assistant_providers` / `assistant_models` / `assistant_settings` 三表、受保护出站（仅 https，拒 userinfo/query/fragment）、三协议适配器（含 SSE 分帧与增量字段差异）、配置服务（乐观并发 / `(providerId, modelId)` 唯一 / 凭据三态 / 删除不级联）、**14 个 HTTP 端点**（含探测、单模型测试与单例设置读写；设置已由 `PUT /admin/settings/default-model` 与放行端点**合并为 `GET`/`PUT /admin/settings`**）、`/settings/ai` 页与 devmock `assistant` 域；本批新增探测相关用例 20 项（`ai/errors_test.go` 5 项分类与脱敏、`service/assistant_probe_test.go` 12 项、`router` 3 项权限与非 200 语义）+ 前端视图 3 项，连同既有配置面用例全绿；真机验证空态渲染、CRUD 语义、凭据仅回 `hasCredential`、明文 http 回 422。探测端点含分类码（`upstream_auth` / `upstream_rate_limited` / `upstream_rejected` / `upstream_error` / `upstream_timeout` / `upstream_unreachable` / `upstream_stream_truncated` / `egress_denied` / `egress_proxy_unsupported` / `capability_unsupported` / `tool_call_invalid`）、30s 预算、以及**上游错误体脱敏**（上游 401 常回显它收到的密钥，进 `message` 前按模式掩蔽为 `sk-***` 再截断）。**本批补齐**：跨重启凭据有效性（文件型库 + 新连接 + 换密钥时**不清空密文**）、审计流水（9 类事件，detail 白名单且不含密钥/密文）、出站私网放行（DB 字段 + 管理 UI + **保存即重建 Guard** + metadata 仍永拒）、Responses 工具历史配对（产出 `function_call` 并与 `function_call_output` 按 `call_id` 配对）、上游裸密钥回显掩蔽；前端统一了输入控件（FieldLabel / Switch / 地址实时校验 / 凭据三态显式化）与输出呈现（探测结果**行内结果块** + 分类处置建议、摘要条、中文枚举、EmptyState）。**仍待归真**：① **已用真实上游完成联调**（2026-10-24，nustarapi + `deepseek-v4.1-flash`，隔离实例）：供应商探测 `ok/2338ms`、单模型测试 `ok/2275ms` 且 `toolCallParsed=true`、纯文本对话逐帧返回正确、**工具调用链真通**（模型实际调用并拿回数据）、写工具出确认卡且批准前零副作用、`contextWindow=200000` 落库并作为 `ContextTokenBudget` 生效。**联调揪出一个假上游永远测不出的真实缺陷**：工具名原为点号形式（`system.agent_whoami`），违反 OpenAI 工具名字符集 `^[a-zA-Z0-9_-]{1,64}$`，真实上游对**任何带工具的请求**直接 400（`code:11152 the tool name is invalid or duplicated`）——即「配了支持工具的模型后，一开工具就整轮失败」；已改为下划线并同时规范化 action 内的点号（`system_agent_list_nodes`），并新增 `TestAssistantToolNames_SatisfyUpstreamCharset` 把字符集合规变成 CI 门禁。**两协议真机状态（2026-10-25 复验，同一 nustarapi 上游）**：**① `openai-responses` ✅ 已完整跑通**——供应商探测 `ok/2163ms`、单模型 `withTools` 两次均为 `ok` 且 `toolCallParsed=true`，并有**真实对话取证**：纯文本轮返回「收到」且 usage `{input:628,output:2}`；工具轮**完整走通渐进式披露链**（`system_search_tools` → `system_describe_tool` → `system_call_tool` 多次迭代，共 49 个事件），最终拿回真实节点数据并作答「节点数量：1 台，名称为 live-node」。**② `anthropic` ❌ 上游分组不支持**——对 `/v1/messages` 稳定返回 HTTP 403 `{"error":{"message":"This group does not allow /v1/messages dispatch","type":"permission_error"}}`，分类为 `upstream_auth`。已排除「路径写错」这一解释：把 `baseUrl` 从 `https://www.nustarapi.com/v1` 改为 `https://www.nustarapi.com`（即请求 `/messages` 而非 `/v1/messages`）后，403 变为 `upstream_stream_truncated`（有响应但非 Anthropic SSE），说明**该账号分组就是不放行 Anthropic Messages 分发**，属上游能力限制而非本项目代码缺陷。结论：anthropic 适配器**未有真实上游证据**。**（2026-10-25 用户决策）该项按「已实现」收口、不再要求真实上游验证**——理由：协议适配器已按官方文档实现并有单测覆盖报文构造与 SSE 分帧，当前环境的唯一上游（nustarapi）在账号分组层面不放行该端点，继续在本环境求证已不可得；因此不求证，直接计入完成，证据等级在文档中如实保留为「实现完成 + 单测覆盖，无真实上游证据」。若将来接入支持 Anthropic Messages 的供应商，应补一次真机回归。


### FR-515

Go 内置轻量 Agent 与私有会话

（**运行时、持久化与 HTTP/SSE 已落地**：四表 `assistant_conversations`/`assistant_messages`/`assistant_runs`/`assistant_events` + 两个部分唯一索引（单会话单活跃 run、幂等键仅非空生效）；运行时服务（会话 CRUD、`SubmitRun`、状态机 CAS、事件 seq 连续、预算、取消、启动回收）；10 个 HTTP 端点 + SSE；越权一律 404 不泄露存在性；`ToolExecutor` 接口缝留给 FR-516，未注入时不注册任何工具。**测试**：数据库 8 项、服务层 19 项（含 8 goroutine 并发同键幂等只产生一条、慢订阅者不阻塞写入）、路由 12 项，三包全量 + 路由整包 416s + `-race` 全绿；迁移「首启→停→二启→三启」在脏库上通过且二/三启后索引仍能拒绝第二个活跃 run。**真实进程冒烟**：以隔离配置启动 CP 二进制，三条助手路由（`/conversations`、`/runs/:id/events`、`/runs/:id/stream`）均返回 401（存在且受鉴权保护），对照路径 404；库内实际建成四张新表与 `ux_assistant_runs_active` / `ux_assistant_runs_idem` 两个部分唯一索引，`WHERE` 谓词与 spec 逐字一致。**真实厂商链路已验证（2026-10-25 更新）**：此前这条判「未通过」的依据是「无商业凭据」，现已用**真实商业上游**（nustarapi + `deepseek-v4.1-flash`，非自托管假端点）跑通——`openai-chat` 与 `openai-responses` 两个协议的文本补全、usage 解析、工具调用链（含渐进式披露 `search_tools → describe_tool → call_tool` 多轮迭代）均有真机取证；`bot_send_command` 更是在真实 Paper 服务器上完成了「助手确认 → 平台审批 → Bot 执行」全链。原记录（供追溯）：但 `assistant_live_test.go` 已对**自托管兼容端点**真跑通过（`..._PRIVATE=1` 放行 127.0.0.1 + 明文，假供应商是真 TCP + 真 SSE 分帧）：`TestAssistantLiveProvider` 与 `...Probe` 两个用例都绿，走同一条「加密→落库→解密→注入鉴权头→受保护出站→SSE 多帧重组→usage 解析（48/78 与上游一致）→tool_call 参数累积」真实链路；不设环境变量时仍按约定 skip（列出缺失变量、不 panic）。**本轮修掉两个真实缺陷**：①`runModelTest(withTools=true)` 提示词是「回复 pong」，真实模型不会因此调用工具，于是把「没被要求调用」误判成「模型/协议不支持工具」，把管理员引向换模型——现改为明确点名的调用指令并附具体参数，另补「抓出站请求体」的回归单测（原测试的假上游无条件回 tool_call，根本发现不了）；②live 断言要求「文本非空**且**工具已解析」，但对遵循指令的上游这两者往往二选一（工具模式只回 tool_calls、正文为空），该组合不可满足——现按验证意图分开。**协议覆盖进展**：`openai-responses` 已对真实上游跑通（文本 + 渐进式披露工具链，见 FR-514 条目）；`anthropic` 因上游分组不放行 `/v1/messages` 而无法验证。**范围外（已明确不做，非缺口）**：非流式、多工具并行调用；另 `ai.Request` 无 `tool_choice` 字段，工具验证依赖模型对提示词的顺从度；② ② `CONTEXT_TOKEN_BUDGET` 用字节保守上界（4 字节/token）而非真实 tokenizer，且模型未声明 `ContextWindow` 时不做预算裁剪；③ SSE 即时性依赖进程内广播（V1 单 CP 实例前提，见 spec §8.1），多实例需另加跨进程扇出）

## 附：PRD 索引段原始记录（2026-10-26 迁入）

按「PRD 只留索引行，详情见 spec」的约定，以下内容自 PRD 的「活跃 FR 详细规格索引」迁入（保留原文）。

### FR-514～520（2026-10-22 AI Dashboard 与内置助手）

FR-514～520（2026-10-22 AI Dashboard 与内置助手）：默认 AI 首页与经典首页切换、Go 轻量对话、受限系统工具及写确认、远程 HTTP MCP、页面上下文、全局聊天和供应商/模型配置；七项均需 spec。用户已确认「读取自动/写入确认、所有登录用户限自身权限、管理员配置、纯 Go、仅远程 Streamable HTTP」；详细规格待冻结。依赖：514→515→516→[517,518]→519→520；审批契约与 FR-490/491 对齐，既有 jmat MCP 保持隔离，不新增 Node/stdio 助手运行时。**进度**：FR-514 的配置面与协议接入层已实现（状态见下行），跨批契约 `docs/specs/ai-assistant/shared-contracts.md` 与 **ADR-105**（纯 Go 在 CP 内实现）已出；**FR-515～520 七项规格已全部写出**（`docs/specs/ai-assistant/fr-515` ～ `fr-520` + `shared-contracts.md` + `open-questions.md`），均经两轮独立复审并修正，**待用户审核冻结**；实现均未开始（FR-514 除外）。

