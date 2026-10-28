# 功能规格：AI 供应商、模型与管理配置（FR-514）

> 状态：**实现中（配置面、协议接入层与探测端点均已落地；真实上游验收与跨重启验证未做）**　·　关联 PRD：FR-514　·　范围：仅 FR-514
> 共享契约（消息/事件/上下文/工具风险）见 [../ai-assistant/shared-contracts.md](../ai-assistant/shared-contracts.md)，本文不复述。
> 本文不含任何真实凭据、真实端点、参考项目名称或绝对路径；所有示例地址均为文档保留段。
>
> **与初版草稿的差异已按实际实现就地修订**，并在受影响处标「实现偏差」说明为什么改。
> **已实现**（原先列为待办的项已补齐）：§4.3 私网放行设置、配置面审计流水、跨重启凭据验证。
> **仍未实现**：真实上游验收（T9）——没有凭据就是**未验证**，不拿模拟上游冒充。

## 1. 背景与目标

平台要提供内置 AI 助手，必须先有「模型从哪来、凭据怎么存、凭什么能出站」这一层底座。当前仓库不存在模型供应商抽象、凭据存储或对话运行时（详见 §2）。FR-514 只交付**配置面与模型接入层**：管理员能登记供应商与模型、显式探测连通性、用真实上游跑一次最小补全；普通用户能拿到一份「我可以用哪些模型」的摘要。聊天 run、会话存储、工具执行、外部 MCP 属 FR-515 及之后，本 FR **不实现**。

范围内：供应商/模型数据与管理员 API、凭据保存与脱敏、三协议最小流式适配（文本/工具调用参数/用量）、出站网络准入、设置页供应商与模型分区。
范围外：会话与消息持久化、run 状态机、工具执行与批准、MCP 客户端、AI 首页、聊天 UI、计费、OAuth、多供应商自动故障转移。

## 2. 现状核实（只读结论）

- 平台设置是**白名单字符串键**覆盖模型（[internal/controlplane/service/settings.go](../../../internal/controlplane/service/settings.go)、[internal/controlplane/router/settings.go](../../../internal/controlplane/router/settings.go#L65-L71)），只适合标量；多供应商/多模型不能塞进字符串键。
- 管理员面已有 `settings` 分组（[internal/controlplane/router/router.go](../../../internal/controlplane/router/router.go#L617-L618)），但写权限走 `requireNodes(...)` 这类 helper；本 FR 的**唯一真源**是显式 `UserAccess.IsPlatformAdmin`（[internal/controlplane/middleware/auth.go](../../../internal/controlplane/middleware/auth.go#L83-L100) 已按该字段放行），不再叠加 helper。
- 已有可逆加密器 `*service.KeyEncryptor` 与三轨解析 `ResolveKeyEncryptor`（[internal/controlplane/service/client_key_crypto.go](../../../internal/controlplane/service/client_key_crypto.go)），在 [apps/control-plane/main.go](../../../apps/control-plane/main.go#L423-L446) 装配后 `SetKeyEncryptor` 注入各 service；`nil` 加密器是既有降级语义。
- 出站已有共享客户端工厂与代理配置（[internal/platform/httpclient/provider.go](../../../internal/platform/httpclient/provider.go)、[docs/adr/037-outbound-proxy.md](../../../docs/adr/037-outbound-proxy.md)）；代理**不是** SSRF 防护，不能复用来做目的地址准入。
- 现有出站测试按钮只用于代理/JDK 源连通性，不是模型协议探测。
- 数据库只有 SQLite（`database.AutoMigrate` 追加模型，见 [internal/controlplane/database/database.go](../../../internal/controlplane/database/database.go#L117-L135)）；本 FR 不扩任何数据库后端，不承诺 MySQL 表结构。
- 设置 UI 是单页分类（[apps/control-plane-web/src/pages/SettingsPage.tsx](../../../apps/control-plane-web/src/pages/SettingsPage.tsx)），路由懒加载表在 [apps/control-plane-web/src/lib/shared/route-chunks.ts](../../../apps/control-plane-web/src/lib/shared/route-chunks.ts)。

## 3. 冻结的共同选择

| 项 | 冻结值 |
|---|---|
| REST 前缀 | `/api/v1/assistant`；管理员子域 `/api/v1/assistant/admin` |
| 普通用户入口 | `GET /api/v1/assistant/models`，只回可用模型摘要 + `defaultModelId` |
| 管理面授权 | 显式 `access.IsPlatformAdmin`，**不用** `node.manage` / `requireNodes` helper |
| 协议枚举 | `openai-chat` / `openai-responses` / `anthropic` |
| 模型标识 | 存**原始** `modelId`（上游原样字符串），不硬编码任何品牌型号 |
| HTTP 实现 | Go 标准库 `net/http`，SSE 手写行解析；不引第三方 SDK |
| FR-514 工具语义 | 只做 toolcall **解析与参数完整性校验**，不执行任何工具，结果交后续 runner |
| 明确不承诺 | 隐藏思考、图片/多模态、OAuth、自动多供应商 failover、batch、prompt caching |
| UI 落点 | 设置 `/settings/ai` 新子页 + 平台设置入口，复用现有设置布局与 `Dialog`/`DangerConfirm` |
| 共享契约 | 引用 `../ai-assistant/shared-contracts.md`；本文只定义配置面 |

上游协议细节（字段名、SSE 事件名、用量结构）在实施前须与官方文档逐条对齐并留证据；本文只冻结抽象与统一事件模型。

## 4. 数据模型（最简可落地方案）

三张新表，全部随 `database.AutoMigrate` 加性建表；`version` 用于乐观并发，写入须带 `version`，不匹配回 409。

### 4.1 `assistant_providers`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint PK | |
| `name` | string，**唯一** | 显示名，1..64 |
| `protocol` | string | 三枚举之一 |
| `base_url` | string | 仅 origin（可含固定路径前缀），无 userinfo/query/fragment |
| `credential_mode` | string | `none` / `env` / `stored` |
| `credential_env` | string，可空 | 仅 `JIANMANAGER_ASSISTANT_` 前缀，白名单内 |
| `credential_enc` | string，可空 | AES-256-GCM 密文（GORM 字段名 `CredentialEnc`，`json:"-"`）；**永不出现在响应** |
| `credential_set_at` | time，可空 | 仅用于「已配置」标志与展示 |
| `enabled` | bool | 供应商总开关 |
| `version` | int | 乐观并发 |

实现字段名以代码为准：`Name` / `Protocol` / `BaseURL` / `CredentialMode` / `CredentialEnv` / `CredentialEnc` / `CredentialSetAt` / `Enabled` / `Version`。

### 4.2 `assistant_models`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint PK | |
| `provider_id` | uint | 与 `model_id` 组成**唯一**约束 |
| `model_id` | string | 上游原始 ID，1..200 |
| `display_name` | string | 可等于 `model_id` |
| `streaming` / `tools` | bool | 管理员声明，用于过滤与显式错误 |
| `context_window` | int，可空 | 上下文窗口声明（留空表示未声明，**不是 0**） |
| `max_output_tokens` | int，可空 | 提示与后续 run 的预算上限 |
| `enabled` | bool | 单模型开关 |
| `version` | int | 乐观并发 |

**实现偏差**：初版草稿写的是 `capabilities_streaming` / `capabilities_tools` 与「无 `context_window`」；
落地时改为平铺布尔 `Streaming` / `Tools` 并补 `ContextWindow`。原因是前端表格需要把「上下文窗口」
直接展示为一列，塞进 capabilities 前缀会让 API 投影多一层无意义嵌套；而 `ContextWindow` 用指针
（`*int`）表达「未声明」是必需的——用 0 表达会与「声明为 0」混淆，且 UI 无法区分。

**复合唯一索引的坑（已用测试钉住）**：`ProviderID` 上的 `uniqueIndex` **标签名必须与 `ModelID` 的一致**
（`idx_assistant_model_provider_model`）。若给 `ProviderID` 用同名 `uniqueIndex` 之外还有别的索引名，
GORM 会把两列拆成两个独立唯一索引，`(provider_id, model_id)` 就退化为 **`model_id` 全局唯一**——
表现为「两个网关都提供 `gpt-4o-mini` 时第二个建不进去」，而错误信息只会说 duplicate，看不出是这个原因。

### 4.3 `assistant_settings`（**单行**，`id=1`）

| 字段 | 类型 | 说明 |
|---|---|---|
| `default_model_id` | *uint，可空 | **指针单行**，不做多行 bool |
| `allow_private_cidrs` | text（JSON 数组），空 | 出站准入放行的私网段；**显式 column 名**（默认蛇形会把 `CIDRs` 拆成 `c_id_rs`） |
| `version` | int | 乐观并发 |

`allow_private_cidrs` **已实现**（TEXT + JSON 数组，随 `Version` 乐观并发）。

**存 DB 还是配置文件**：选 DB + 管理 UI。自托管网关的网段常随环境调整，
强制改文件+重启会让运维绕过它（比如干脆把网关暴露到公网）。风险改由「谁能改」控制：
仅平台管理员可写、每次写入留审计 `assistant.settings.egress`。
**放行网段与放行 `http://` 明文是两个独立开关**——后者只在 dev 部署开启
（`cfg.Server.DevMode`），视图以只读 `insecureAllowed` 回显，避免「改了 CIDR 却发现 http 仍不通」。

**生效机制**：`AllowPrivate` 在 `NewGuard` 时被编译进拨号器，没有可变入口。
因此装配层提供工厂 `SetEgressFactory`，保存设置后 service **立即重建 Guard**——
不重建就是「保存成功但行为不变」。启动时 `ApplyStoredEgress` 装配；
解析失败只告警不清空（静默清空会让内网网关无预兆全断且无线索）。

**校验**：非法项**整单拒绝并点名哪一项**（静默丢弃会变成最难查的一类问题）；
自动规范化（掩主机位）与去重，`10.0.0.1/8` 与 `10.0.0.0/8` 视为同一段。

### 4.4 语义约束

- **删除 = 硬删除**，不做软删：唯一的「历史引用」来源是未来会话，本 FR 不存在；因此不引入 `deleted_at`，避免「软删行占唯一索引」这类糊涂账。FR-515 的会话表以**冗余文本快照**记录当时用的模型，不建外键级联，删除模型不破坏历史。
- 删除被引用的对象**拒绝**并给明确错误：供应商下有模型 → 409 `IN_USE`（**不级联**）。级联删除会让一批模型在没有提示的情况下消失，而管理员通常只是想停掉一个供应商。
- **默认模型的停用与删除刻意不同**（**实现偏差**：初版草稿把两者写反了——草稿说「停用同事务清空指针、删除则拒绝」，落地语义正好相反，理由如下）：
  - **停用**当前默认模型：**不清** `default_model_id`。停用是可原地恢复的（`enabled=false→true`），若停用时清指针，恢复后默认模型就永久丢了，管理员必须重设一次——而「暂时关一下」恰恰是最常见的操作。停用期间的可用性由**读时解析**兜底：`GET /assistant/models` 只在默认模型「自身启用且其供应商启用」时才回它，否则回 `null`。
  - **删除**当前默认模型：**在同一事务内清空指针**。删除不可逆，留着一个指向不存在行的 `default_model_id` 会让每次读摘要都要处理悬空引用，且后续写入会因外键/一致性校验失败而报出与真实原因无关的错。因此删除是唯一需要动指针的路径。
- 停用供应商**不重写**其子模型 `enabled`：供应商 `enabled=false` 只在读取侧隐藏，模型表原样保留，重新启用即恢复。
- 默认模型写入与「该模型必须 enabled 且其供应商 enabled」为**同一事务**；并发下以 `assistant_settings.version` 与唯一性保证只有一个赢家，输家 409。
- 用户可见模型 = 供应商 `enabled` ∧ 模型 `enabled`；若 `default_model_id` 指向不可用模型，读取侧返回 `null`，**不在读路径写库**。

## 5. 凭据

- 三种模式含义固定：`none` 无凭据；`env` 只存**变量名**，运行期 `os.Getenv` 取值（不走 `resolveEnvRef` 的 QQ 文件回落，也不复用其它业务凭据约定）；`stored` 存密文。
- env 变量名**白名单**：必须匹配 `^JIANMANAGER_ASSISTANT_[A-Z0-9_]{1,64}$`（例：`JIANMANAGER_ASSISTANT_PROVIDER_KEY`）；其它前缀一律 422。
- 写接口的 secret 指令（**实现偏差**：初版草稿设计的是嵌套动作对象 `{"action":"replace","value":"..."}` / `{"action":"clear"}`，落地改为**两个平铺字段**，理由见下）：
  - `credentialSecret` **缺省或为空** = 保留原密文（改显示名、改 baseUrl 都不会丢密钥）；
  - `credentialSecret` **非空** = 替换为新密文；
  - `clearCredential: true` = 清空并把 `credential_mode` 降为 `none`（同事务）。
  - 改平铺的理由：草稿那套动作对象在 Go 里要额外定义一棵带判别字段的结构体，而它的三种语义
    恰好是「有没有值 / 有没有标志」两个正交维度，直接用「值缺省=保留、非空=替换、显式标志=清除」
    表达更少歧义——尤其避免了「`action` 拼错时被当成保留而静默不生效」这类失败模式
    （未知 action 要么被忽略要么报错，两种都不如「没有 action 这个字段」来得干净）。
- **不回传密文或明文**，响应只有 `hasCredential` 与 `credentialMode`。
- `credential_mode=stored` 写入时若注入的 `*KeyEncryptor` 为 `nil` → **拒绝**（422 `ENCRYPTOR_UNAVAILABLE`），绝不落明文、不静默降级为 `env`。
  **为什么必须显式拦**：`KeyEncryptor.Encrypt` 在 receive 为 `nil` 时**返回空串且不报错**
  （既有降级语义，为的是不阻断无关业务）。若不拦，`stored` 凭据会以「已配置」的形态存成空密文，
  直到第一次真正调模型才以 401 暴露，届时现场没有任何线索指向「当初没加密成功」。
- 复用 main.go 已装配的同一 `KeyEncryptor` 实例与同一密钥来源（`JIANMANAGER_CLIENT_KEY_ENC_SECRET` > 持久化 autogen > dev 回退），**不引入第二把主密钥**。
- 生产（`dev_mode=false`）禁止用内置 dev 密钥兜底：若加密器来源为 dev 且要写 `stored` → 422 `enc_dev_not_production`。
- 跨重启主密钥不可自动替换：解密失败时**保留密文**、返回明确错误并在日志给出「加密器或主密钥已变化」提示，不静默清空、不自动改写。
- 无加密器时 `env`/`none` 模式完全可用；**AI 配置缺失或不可用一律不阻塞 CP 启动**（不得新增 `log.Fatalf`）。

## 6. 出站网络准入（受保护客户端工厂）

受保护客户端落在 `internal/platform/httpclient/protected.go`（**实现偏差**：初版草稿写的是
`internal/controlplane/assistant` 内的 `egress.go`）。移出来的理由：外部 MCP 客户端（FR-517）
与模型供应商需要**同一套**准入规则，把它放在任一业务域里都会让另一个域反向依赖；
而 `httpclient` 本来就是这个仓库所有出站工厂的家，且其既有职责（代理/transport）与
本处职责（目的地址准入）边界清晰、不重叠。

### 6.1 已实现

1. **静态 URL 校验**（配置时）：仅 `http`/`https`，禁止 userinfo / query / fragment，host 必填。
   明文 `http` 在登记时即被拒（422），不等到调用时。
2. **准入在拨号点强制**：`Guard.dialContext` 是唯一强制点，任何请求都必须经它——包括重定向后的请求
   （虽然重定向默认已禁，但把校验放在拨号点意味着即使将来放开重定向也不会绕过）。
   这样就不存在「先 Resolve 检查、再让默认 client 重新解析」的 DNS rebinding / TOCTOU 窗口。
3. **地址判定与钉 IP**：解析得到全部地址后**任一**命中拒绝段即整体拒绝（防轮换绕过）；
   通过与拨号使用同一个已校验 IP 字面量，`Host` 头与 TLS SNI 仍用原域名。
4. **metadata 永远拒**：云元数据地址段单列，即使管理员显式放行私网也拒绝——元数据泄漏的是主机身份凭据，
   代价与「访问一个内网服务」不在同一量级。
5. **重定向默认禁**：`CheckRedirect` 直接报错，不支持「跟随并重校验」。
6. **代理无法钉 IP 则显式不支持**：目标若会被进程代理接管，本次请求返回明确错误（`ErrProxyUnsupported`），
   **不静默直连、也不静默经代理丢校验**。静默直连会让「配了代理」的部署出现意外绕过路径，比报错难排查得多。
7. **不转发面板凭据**：永不把 JWT/Cookie 带给上游。

### 6.2 未实现 / 已知缺口

8. ~~私网显式放行没有配置入口~~ → **已实现**（见 §4.3）：DB 字段 + 管理 UI + 即时重建 + 审计。
   注意仍不实现的是 §6.2 第 11 条的「origin 精确绑定」（FR-517 前置）。
9. **出站预算字段尚未存在**（更正：早期措辞称「在 `GuardOptions` 里有默认值」，**与代码不符**）。
   `GuardOptions`（`protected.go` L45-L62）实际只有 `AllowPrivate` / `AllowInsecure` / `Timeout` /
   `AllowSameOriginRedirect` / `Proxy` / `NoProxy` 六项，**没有**响应头超时、流空闲超时、
   单行事件上限、单次响应体上限这些字段。当前这些约束分散在各协议适配器的局部实现里，未统一、
   也未按协议分别标定；初版草稿拟议的 4MiB/8MiB/64KiB 属设计意图，落地后未逐项回归验证。
10. **`ValidateBaseURL` 只做静态 URL 校验**，不承担保存阶段的 DNS/IP 准入；真正的地址准入只在拨号点
   （`Guard.dialContext`）发生。因此「保存时通过校验」不等于「该地址当时是公网可达」。
11. **origin 绑定未实现**：现有 `AllowPrivate` 只有 CIDR，没有「精确 origin + CIDR」的绑定语义。
   FR-517 要求按 origin 精确放行自托管目标，需在 `GuardOptions` 上扩展，属 FR-517 的前置任务。

## 7. 协议适配

统一内部事件（供 FR-515 复用，模型层不泄漏上游 schema）：`text_delta`、`tool_call_start`、
`tool_args_delta`、`tool_call_end`、`usage`、`end`。**没有 `error` 事件类型**——适配器的错误
通过 `Stream` 的返回值（Go `error`）表达，不混进事件流（见 `ai/types.go` 的注释：两套语义并存会让
调用方分不清「收到 error 后还有没有数据」）。运行时若需要向浏览器广播错误，那是 **FR-515 的持久化
`run` 事件**，与这里的适配器事件是**不同层次的东西**，不得混为一谈、也不得反向塞回适配器枚举。
（**实现偏差**：草稿写作 `tool_call_args_delta` 与 `finish(reason)`，落地简化为 `tool_args_delta`
与 `end`——`end` 事件自身携带结束原因字段，不必把原因编进类型名，否则每种原因都要多一个事件类型。）

- 三协议各自实现「构造最小请求」与「流解析」；请求只带：模型 ID、system/user 文本、可选工具 schema、`stream=true`。
- **toolcall**：按上游分片累积；`end` 时要求 `id`、`name` 非空且参数是**可解析的 JSON 对象**；任一不满足即 `tool_call_invalid`，不产出可执行调用。多工具调用必须按上游 index/id 独立归并，不得串味。
- **usage 可空**：上游未给用量时事件 `usage=null`，**不补 0**；当前 `Usage` 只有 `InputTokens` / `OutputTokens`
  两个指针字段（`ai/types.go`），**没有** `prompt/completion/total` 三字段——不要把 OpenAI 的字段名当内部模型。
- **提前 EOF**：未收到正常结束信号即断流 → `upstream_stream_truncated`，已累计文本不当作成功终态上报。
- **错误分类**（统一 code，附脱敏后的上游摘要）：`upstream_auth`(401/403)、`upstream_rate_limited`(429)、`upstream_rejected`(400/422)、`upstream_error`(5xx)、`upstream_timeout`、`upstream_unreachable`(DNS/TCP/TLS)、`egress_denied`、`upstream_stream_truncated`、`protocol_unsupported`、`capability_unsupported`。
- 能力不足要**显式**：未声明 `capabilities_tools` 时带工具请求直接 422 `capability_unsupported`，不静默丢工具。

### 7.1 已修复：Responses 协议的工具历史回放配对（2026-10-23）

**状态：已修复**（提交 `e93a4739`），本文保留记录以免日后被改回去。

原缺陷：`buildResponsesInput` 把 assistant 历史中的工具调用降级成一条普通 assistant 文本
（`"[调用工具 X 参数 Y]"`），**从不产出 `function_call` 项**，却把工具结果作为 `function_call_output` 发出。
后果是 `function_call_output` 找不到配对的 `function_call`（无匹配 `call_id`），上游判请求不合法；
即便某些上游宽容，模型也读不到「我调用过工具、这是结果」的结构化历史，多轮工具循环语义丢失。

**这个缺陷在本 FR 自己的验收范围内看不出来**：FR-514 只做「单次最小补全」与探测，不跑工具循环；
但 FR-515/516 的多轮工具循环一旦跨轮回放历史，Responses 协议必然失败。

现在的正确实现与两条硬约束：

- `responsesInput` 承载 `function_call` 项（`call_id` / `name` / `arguments`），与 `function_call_output`
  按 **`call_id`** 一一配对，`function_call` 必须排在其 output 之前；
- **不要给 `function_call` 填 `id`**：`id` 是上游为「输出项」分配的标识（形如 `fc_…`），我们没有保存过它；
  拿 `callId` 冒充属于凭空伪造，会让上游按一个不存在的项 id 去定位。`id` 在输入项里是可选字段，省略即可。

回归在 `ai/responses_input_test.go`，包括「结果倒序回灌仍按 call_id 正确定位」与「空正文不产出空 message」
两个容易写错的边界。

## 8. 探测与真实测试（**已实现**）

两种动作都由**显式按钮/接口**触发，绝不在输入框失焦、列表刷新或保存后自动发请求——
模型调用按次计费，把「保存」与「调用上游」绑定等于让每一次手滑都产生费用。
前端每个供应商行、每个模型行各一个按钮，请求进行中该行按钮禁用（防连点重复付费）。

- **供应商探测** `POST .../providers/:id/test`：用该供应商下的模型跑一次最小补全。
  选择顺序：**优先第一个启用模型；若没有启用模型，回退到任意一个（按 id 升序）**——
  探测的是「供应商连通性与凭据」而不是「那个模型是否该对外可用」，故停用模型同样能验证。
  供应商下**一个模型都没有**时才明确报错。（与实现一致，见 `service/assistant_probe.go` 的 `TestProvider`。）
  返回 `ok`、`latencyMs`、`classifiedError?`、`message?`。供应商下没有任何模型时明确报错
  （无法探测就是无法探测，不假装通过）。**实现偏差**：草稿设想「优先官方模型列表端点，
  不可用时退化」，落地直接只用最小补全——列模型端点各协议路径不同（`/models` vs `/v1/models`），
  且部分网关只实现推理端点，列模型反而更容易误报「不通」；要验证的本来就是「能不能说话」。
- **单模型真实测试** `POST .../models/:id/test`：跑**一次**最小补全（固定提示，不接用户数据、
  不执行任何工具）。返回 `ok`、`latencyMs`、`textPreview`(≤200 字符)、`promptTokens`/`outputTokens`
  （可空）、`toolCallParsed`、`classifiedError?`、`message?`。
  请求体可选 `{"withTools":true}`：额外验证工具参数能否解析完整。
- **上游失败仍回 200**：`ok:false` 是**结论**，不是本次 HTTP 请求失败。把「密钥错」映射成 401
  会让前端误以为自己的登录态失效并弹重新登录；把「上游 5xx」映射成 502 会让管理员以为面板坏了。
  只有请求级问题（对象不存在 404、非管理员 403、参数不合规 422）才走错误响应。
- 两者都不写会话表、不产生 run，与 FR-515 的聊天路径完全分离。
- **审计：仍未写**（见 §12 第 7 条）。

**凭据解析顺序**（探测与未来运行循环共用同一实现）：`none` 不发密钥头；`env` **只读进程环境**，
变量未设置时**在发请求前**报错（否则会得到一个 401，把「变量没配」误报成「密钥不对」）；
`stored` 解密，失败时明确提示重新录入而**不**降级为无密钥。

**分类码**（`ai.ErrorCode`）：`upstream_auth`(401/403) / `upstream_rate_limited`(429) /
`upstream_rejected`(其余 4xx) / `upstream_error`(5xx) / `upstream_timeout` / `upstream_unreachable` /
`upstream_stream_truncated` / `egress_denied` / `egress_proxy_unsupported` / `capability_unsupported` /
`tool_call_invalid`。前端据码给**处置建议**（换密钥 / 稍后重试 / 查网络 / 放行地址），不解析 message。

**脱敏**：上游错误体先按模式掩蔽疑似凭据（`sk-*` / `AIza*` / `Bearer *` / JSON 里的
`api_key|token|secret|authorization` 字段值 / `api key … : <值>` 句式 / `x-api-key: <值>`），
再截断到 512 字节。**为什么必须做**：上游的 401 经常回显它收到的那个密钥
（"Incorrect API key provided: sk-xxx"），这段文本会进 message、进前端提示、可能进日志与截图。
掩蔽只针对「看起来就是密钥」的片段，保留其余诊断信息——模型名写错时正是靠上游那句话排查。

**一处实测教训（`Bearer` 与 RE2）**：`Authorization: Bearer eyJ…` 里的 `Bearer` 本身是一串
字母，会被 header 规则当成密钥掩成 `Authorization: *** ***`，把诊断词一起丢掉；想用负向前瞻
排除它是不行的（Go 的 regexp 是 RE2，不支持 `(?!…)`）。最终按**值的形态**区分：
header 规则要求取值 ≥8 位，而 `Bearer` 恰好 6 位，天然被排除。
（中间试过「先占位再还原」，但占位符同样会被 header 规则再掩一次，方向本身是错的。）

### 8.1 验证现状（截至 2026-10-23）

| 验证形式 | 状态 | 说明 |
|---|---|---|
| 单测（报文构造 + SSE 分帧 + 分类 + 脱敏） | ✅ | 不依赖网络 |
| **三协议模拟上游端到端** | ✅ | 走真实 service 路径（DB 行 → 凭据解密 → 受保护出站 → 协议适配 → 分类），三个协议各覆盖文本、工具调用解析、供应商探测；另验证错误密钥归 `upstream_auth` 且密钥被掩蔽。**用的是模拟上游，不是真实供应商** |
| **真实供应商** | ❌ **未验** | 本机无任何真实凭据。可执行入口已就绪：`internal/controlplane/service/assistant_live_test.go`，注入 `JIANMANAGER_ASSISTANT_LIVE_*` 后 `go test -run TestAssistantLive` 即完成；缺变量时它 skip 并明写「未验证而非通过」 |
| 凭据跨重启有效 | ✅ | 文件型 SQLite + 新连接模拟重启；另断言换主密钥时解密失败但**密文原样保留** |
| 审计留痕 | ✅ | 9 类事件；断言 detail 不含明文密钥/密文/密钥字段名，且 `credentialChanged` 可见 |
| 私网放行 | ✅ | 非法项拒绝并点名、规范化去重、version 冲突、保存后**立即生效**、metadata 即便放行仍拒、启动装配 |

**模拟通过 ≠ 真实通过**：模拟上游只能证明「我们按文档实现的解析，在按同一份文档回帧时走得通」。
文档与实现有偏差时只有真实上游能暴露——这正是上表第 3 行仍未打勾的原因。

## 9. 任务拆分（可顺序执行）

- [x] T1 模型与迁移：`model/assistant.go` 三表 + `AutoMigrate` 追加 + 唯一约束。
- [x] T2 加密装配：main.go 复用同一 `KeyEncryptor` 注入新 service；`nil` 拒绝写入。
- [x] T3 出站准入：`internal/platform/httpclient/protected.go` 受保护工厂；URL 静态校验 / CIDR / metadata / 钉 IP / 禁重定向 / 代理显式不支持，均有单测。
- [x] T4 协议适配：`ai/types.go`、`ai/sse.go`、`ai/openai_chat.go`、`ai/anthropic.go`、`ai/openai_responses.go`、`ai/factory.go`；碎片合并、多 toolcall、提前 EOF、缺 usage、错误分类。
- [x] T5 服务层：`service/assistant.go`（供应商/模型 CRUD + 凭据三态 + 可用目录 + 默认模型事务）。
- [x] T6 路由与权限：`router/assistant.go`，管理员子域显式 `IsPlatformAdmin`，普通用户 `/models`；403/401 与真源测试。
- [x] T7 前端：`api/assistant.ts`、`pages/AssistantSettingsPage.tsx`、`components/views/settings/AssistantSettingsView.tsx`、路由与导航最小追加、devmock `assistant` 域。
- [ ] T8 文档同步：本批已补 API.md / ARCHITECTURE.md / CHANGELOG / PRD / 本 spec。
- [x] **T8b 探测端点**（§8）：`POST .../providers/:id/test` 与 `.../models/:id/test` 已实现，
  含凭据解析、30s 预算、分类码与上游错误体脱敏；前端每行一个按钮（请求中禁用）。
- [x] **T8c 私网放行配置**（§4.3 / §6.2）：`allow_private_cidrs` 字段 + 管理 UI + 保存即重建 Guard + metadata 仍永拒 + 审计留痕，已实现。
- [x] **T8d 配置面审计流水**：9 类事件，detail 按**白名单逐字段**拼接（不序列化请求体再裁剪——那会在新增字段时静默把敏感值带进审计表），不含密钥/密文。
- [ ] T9 真实供应商验收：三协议各一条真实上游（凭据经环境变量注入），由用户确认通过。**未做**（无 `JIANMANAGER_ASSISTANT_LIVE_*` 凭据，`assistant_live_test.go` 按约定 `t.Skip`）。
- [x] T10 迁移回放验证：FR-515 已补「首启 → 停 → 二启 → 三启」脏库回归（`database/assistant_chat_restart_cycle_test.go`），并断言二/三启后唯一索引仍能拒绝冲突插入。

## 10. 代码所有权（避免覆盖他人未提交改动）

| 归属 | 文件 |
|---|---|
| 本 FR 新建（后端） | `internal/controlplane/model/assistant.go`、`internal/platform/httpclient/protected.go`、`internal/controlplane/ai/{types,sse,openai_chat,anthropic,openai_responses,factory}.go`、`internal/controlplane/service/assistant.go`、`internal/controlplane/router/assistant.go`，及各自 `*_test.go` |
| 本 FR 新建（前端） | `apps/control-plane-web/src/api/assistant.ts`、`pages/AssistantSettingsPage.tsx`、`components/views/settings/AssistantSettingsView.tsx`、`packages/devmock/src/handlers/domains/assistant.ts` |
| 仅为注册点，最小追加 | `internal/controlplane/database/database.go`（AutoMigrate）、`internal/controlplane/router/router.go`（handler 装配与分组）、`apps/control-plane/main.go`（`NewAssistantService` + `SetEgress` 装配）、`apps/control-plane-web/src/components/console/Workspace.tsx`、`components/views/settings/SettingsPageView.tsx`、`pages/SettingsPage.tsx`、`lib/shared/route-chunks.ts`、`i18n/{zh,en}.json` |
| **禁止触碰** | 实例列表/分组/翻译等他人脏改动文件（`InstancesPage*`、`GroupsPage*`、`InstancePicker*`、`OverviewPageView.tsx`、i18n 既有条目） |

不提交、不推送、不改 Git 状态。

## 11. 验收标准

逐条标注当前状态（**不把未做的写成已做**）：

| # | 标准 | 状态 |
|---|---|---|
| 1 | 三协议可分别登记并接通（真实上游，凭据环境变量注入），各自都能返回文本增量 | ❌ **未验**：仅有报文构造与 SSE 分帧单测 |
| 2 | 支持工具的上游能解析出**完整参数**的工具调用并标记 `toolCallParsed=true` | ✅ 解析逻辑有单测；`toolCallParsed` 由 §8 测试端点产出，含「声明 tools 但参数不完整 → tool_call_invalid」路径 |
| 3 | 管理员可设/换/清默认模型；并发改默认只有一个成功 | ✅ 服务层 + 路由测试覆盖（11 + 9 项） |
| 4 | 停用默认模型后读摘要回 `defaultModelId=null`（**不清指针**，见 §4.4 实现偏差） | ✅ 读时解析，有测试 |
| 5 | 非管理员调任一管理端接口 → 403；未登录 → 401 | ✅ 路由测试覆盖 |
| 6 | `GET /assistant/models` 只返回摘要 + `defaultModelId`，**无** `baseUrl`、无凭据字段 | ✅ 真机验证 |
| 7 | 已保存密钥不出现在任何响应、审计 detail、日志、错误消息中 | ✅ 响应与日志有测试；**审计面未逐一核查**（本批未写审计流水） |
| 8 | `stored` 模式在加密器为 `nil` 时被拒（422 `ENCRYPTOR_UNAVAILABLE`） | ✅ 有测试 |
| 9 | 重启后配置与 `stored` 凭据仍可用（同主密钥） | 🟡 同进程内验证；跨重启未成文 |
| 10 | AI 完全未配置时 CP 正常启动 | ✅ `EnsureSetting` 失败只 warn 不阻断 |
| 11 | 出站：metadata 拒、未放行私网拒、放行后可通过、重定向拒、多地址任一命中即整体拒 | ✅ `httpclient` 7 项测试；**放行路径因无配置入口实际不可达**（§6.2 缺口 8） |
| 12 | 迁移通过「首启 → 停 → 二启 → 三启」且读回同一 `default_model_id` | ⚠️ 未单独成文 |
| 13 | 真实上游链路（T9）需**用户确认**才算通过；单测全绿不替代 | ❌ 未做 |

**未通过项即未通过**：本 FR 在 PRD 中据实标为「开发中」，不标「已实现」。

## 12. 风险 / 待审核点

1. 管理子域读作 `/api/v1/assistant/admin/*`（若改为 `/api/v1/admin/assistant/*`，仅路径前缀平移）。**已按前者落地**。
2. **代理场景下钉 IP 不成立 → 显式报错**（§6.1 第 6 条）。这会让「配了全局出站代理」的部署必须为供应商域加 `NO_PROXY` 才能用 AI。**取舍已确认接受**：静默直连会让配了代理的部署出现意外绕过路径，比报错难排查。
3. **复用拉取密钥那套 `KeyEncryptor` 与同一主密钥**（不新增 env）。已在 `apps/control-plane/main.go` 装配处按此实现；若将来希望 AI 用独立密钥，需另立 ADR。
4. **删除策略取硬删除 + 拒删被引用供应商**；FR-515 会话表**须用模型快照文本而非外键**（对本批之后的约束，见 §4.4）。
5. 探测原先设想「优先模型列表端点、不支持则退化」——落地改为只用最小补全（§8 实现偏差）；
   代价是探测本身会产生一次极小的付费调用。
6. 冻结的数值预算为初值，**未逐项回归验证**（§6.2 缺口 9）。
7. **本批未写审计流水**：供应商/模型的增删改（尤其「改 baseUrl」与「清空凭据」）目前不留审计痕迹。
   这与平台其它管理动作的惯例不一致，属已知缺口；补齐时须注意**只记元数据**，
   绝不把 `credentialSecret` 或密文写进 detail。

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-514 AI 供应商、模型与平台配置（feat，需 spec，2026-10-22 批；spec

**需求补充**：`docs/specs/ai-provider-models/spec.md` + `api.md`，契约：`docs/specs/ai-assistant/shared-contracts.md`，**ADR-105**）：管理员配置供应商、模型与默认模型，支持 OpenAI 兼容 Chat Completions、Responses 和 Anthropic Messages；凭据由 CP 受控保存且不回显，提供显式连接/单模型测试与能力校验。验收：真实三协议聊天可用、非管理员不能改配置或读取密钥、配置与凭据跨重启有效、未配置时明确引导。

**交付状态**：✅ 已交付@v0.24.0
