# 功能规格：外部 Streamable HTTP MCP 客户端与连接治理（FR-517）

> 状态：草拟 / 待用户审核，**未实现**（外部 MCP 客户端无代码；出站准入地基已就绪）　·　关联 PRD：FR-517　·　范围：仅 FR-517
> 共享契约见 [shared-contracts.md](shared-contracts.md)（§7 外部连接边界、§4.2 工具描述），本文不复述。
> 前置：FR-514（配置与出站）、FR-515（运行时）、FR-516（工具与确认）。

## 1. 背景与目标

前三个 FR 解决「自家模型 + 自家工具 + 自家确认」。FR-517 让管理员接入**外部 MCP 服务**，把第三方工具也纳入同一套工具目录与确认机制——但外部工具是**不可信输入**：工具描述、annotations、返回值都可能诱导模型越界。

范围内：连接登记与凭据、工具发现（分页）、Streamable HTTP 传输（POST + JSON 或请求级 SSE）、工具命名空间、工具策略与默认确认、失败隔离。

范围外：stdio 子进程、旧式双端点 SSE、OAuth 动态注册、工具结果的可信化处理（仍是不可信数据）、跨连接的工具编排。

## 2. 现状核实（只读结论）

- **受保护出站客户端已实现且能力足够**：[internal/platform/httpclient/protected.go](internal/platform/httpclient/protected.go) 提供 `Guard`，含：
  - `ValidateBaseURL`（仅 HTTP/S、拒绝 userinfo/query/fragment）
  - `dialContext` 内**每次拨号**校验并固定已解析 IP（防 DNS rebinding 与 TOCTOU，见文件头注释）
  - `resolveAllowed` / `checkAddr`：默认拒绝私网、回环、链路本地、云元数据（含 `169.254.169.254/32`）
  - 代理组合显式拒绝（`ErrProxyUnsupported`）而非静默直连
  FR-517 **直接复用**，不重写准入逻辑。
- **尚无 MCP 客户端**：现有 `internal/controlplane/mcp/` 是**服务端**（把平台能力暴露给 jmat Token），不是客户端。方向相反，不能复用其传输层。
- **无 stdio 支持基础**：本项目进程模型不允许 CP 启动任意子进程（ADR-105 备选五已否决）。
- **工具描述结构已有参照**：shared-contracts §4.2 已冻结 `namespacedName / invokeName / source / action / inputSchema / read-write 风险 / 策略版本`。
- **凭据存储可复用 FR-514**：加密器 `KeyEncryptor` 与 `credential_mode=none/env/stored` 三态已在 FR-514 spec §4.1 冻结。

## 3. 冻结的共同选择

| 项 | 冻结值 | 理由 |
|---|---|---|
| 传输 | 仅 **Streamable HTTP**：`POST` 到单一端点，响应可为 JSON 或请求范围 SSE | 用户已确认；旧双端点 SSE 可被请求范围 SSE 覆盖 |
| 明确不支持 | stdio、自动启动命令、OAuth 动态注册、SSE 双端点 | ADR-105 决策 6 / 备选五 |
| 连接归属 | 平台级（管理员配置），非用户级 | 用户无自行添加连接的路径 |
| 凭据 | 专属该连接，**不转发**面板 JWT / Cookie / 其它连接凭据 | shared-contracts §7 |
| 工具命名 | `conn.<connectionSlug>.<remoteToolName>` | 同名不冲突；slug 唯一且不可为 `system` |
| 默认策略 | 连接建立后**全部工具禁执行**；管理员显式启用 | 外部 annotations 只是提示，不授予自动执行资格 |
| 未知风险 | 默认要求确认 | 无法证明只读即视为写 |
| 共享凭据越权 | 无法验证上游用户隔离的连接**仅平台管理员可用** | shared-contracts §7 |
| 失败隔离 | 单次调用失败/超时/过量只终止该调用 | 不阻塞其它连接或整个助手 |
| 结果处理 | 工具结果为**不可信数据**，不覆盖 system 规则、不发起自动批准 | shared-contracts §4.2 |

## 4. 数据模型

### 4.1 `assistant_mcp_connections`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint PK | |
| `slug` | string，唯一 | 工具命名空间用；1..32，`^[a-z][a-z0-9_]*$`，保留字 `system` 不可用 |
| `name` | string | 显示名，1..64 |
| `endpoint` | string | 仅 origin+path，无 userinfo/query/fragment；经 `Guard.ValidateBaseURL` 校验 |
| `auth_mode` | string | `none` / `header` / `env` / `stored` |
| `auth_header_name` | string，可空 | 如 `Authorization`；`auth_mode=header` 时必填 |
| `auth_env` / `auth_enc` | string，可空 | 同 FR-514 的 env 引用 / 密文规则；**永不出现在响应** |
| `allowed_origins` | text(JSON)，可空 | 精确 origin + CIDR 放行列表（自托管目标）；空 = 默认拒绝私网 |
| `enabled` | bool | 停用后其工具立即从目录消失 |
| `policy_version` | int | 每次策略变更递增；使旧确认失效 |
| `last_probe_at` / `last_probe_ok` | datetime / bool，可空 | 连通性探测结果 |
| `version` | int | 乐观并发 |
| `created_at` / `updated_at` | datetime | |

### 4.2 `assistant_mcp_tools`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint PK | |
| `connection_id` | uint，索引 | |
| `remote_name` | string | 上游原始工具名 |
| `namespaced_name` | string，唯一 | `conn.<slug>.<remote_name>` |
| `description` | text | 上游描述，**截断并标为不可信** |
| `input_schema` | text(JSON) | 上游 schema，落库前做结构校验 |
| `risk` | string | `read` / `write` / `unknown`；由管理员确认，**不由 annotations 推导** |
| `enabled` | bool | 管理员显式启用才可执行 |
| `schema_hash` | string | schema 变化使旧确认失效 |
| `discovered_at` | datetime | |

### 4.3 发现与失效

- 发现结果**分页**拉取，单连接工具上限 `MAX_TOOLS_PER_CONNECTION`（见 §7）。
- 工具消失 → 置 `enabled=false` 并保留记录（审计需要），不物理删除。
- schema 变化 → 更新 `schema_hash`，该工具的 `pending` 确认全部 `invalidated`。

## 5. 传输与协议

### 5.1 请求形态

- 所有调用 `POST <endpoint>`，`Content-Type: application/json`。
- 响应两种情况都接受：
  - `Content-Type: application/json` → 单条 JSON-RPC 响应
  - `Content-Type: text/event-stream` → 请求范围 SSE，读到该请求的终止事件即结束（**不是**长连接）
- 不建立 goroutine 级别的持久 SSE 订阅；每次调用即一次 POST。

### 5.2 超时与限额

| 项 | 默认值 | 语义 |
|---|---|---|
| `MCP_CALL_TIMEOUT` | 30 秒 | 单次工具调用 |
| `MCP_DISCOVER_TIMEOUT` | 20 秒 | 单页发现 |
| `MCP_RESPONSE_MAX` | 1 MiB | 单次响应体上限，超出即失败（不截断后当成功） |
| `MCP_REDIRECT_MAX` | 0 | 默认**不跟随重定向** |
| `MAX_TOOLS_PER_CONNECTION` | 200 | 超出部分不发现，提示管理员收窄 |

## 6. 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/assistant/admin/connections` | 连接列表（凭据脱敏） |
| `POST` | `/api/v1/assistant/admin/connections` | 新建连接 |
| `PUT` | `/api/v1/assistant/admin/connections/:id` | 改连接（凭据三态：留空保留 / 替换 / 清除） |
| `DELETE` | `/api/v1/assistant/admin/connections/:id` | 删除连接及其工具 |
| `POST` | `/api/v1/assistant/admin/connections/:id/probe` | 连通性 + 能力探测（不落库） |
| `POST` | `/api/v1/assistant/admin/connections/:id/discover` | 拉取工具清单（分页），落 `assistant_mcp_tools` |
| `PATCH` | `/api/v1/assistant/admin/connections/:id/tools/:toolId` | 启停 + 设定风险分类（`read`/`write`/`unknown`） |

全部仅 `IsPlatformAdmin`。错误码沿用 FR-514 风格，另加：
`MCP_UNREACHABLE`（502）、`MCP_PROTOCOL_ERROR`（502，响应不合 JSON-RPC）、`MCP_TOO_LARGE`（502）、`MCP_TOOLS_LIMIT`（409）、`MCP_SELF_LOOP`（400，endpoint 指向本平台自身）。

## 7. 越权与 SSRF 边界（本 FR 的重点）

1. **共享凭据不自动授予普通用户**：`auth_mode != none` 且管理员未声明「上游按用户隔离」时，该连接的工具仅平台管理员可调用（shared-contracts §7）。
2. **端点准入**全部走 `Guard`：默认禁私网/回环/链路本地/元数据；管理员可精确 origin + CIDR 放行自托管目标（`allowed_origins`），不做「允许整个私网段」的粗放开关。
3. **`MCP_SELF_LOOP`**：endpoint 若解析到本平台自身地址，拒绝保存——否则模型可借外部工具回打自身 API 形成环路。
4. **凭据不外溢**：不把面板 JWT/Cookie 转发给上游；不使用一个连接的凭据调用另一个连接。
5. **结果不可信**：上游返回的文本/描述可能含指令注入，不得改变 system 规则、不得触发自动批准、不得作为目标解析依据。

## 8. 验收

- [ ] 新建连接时 endpoint 为 `http://127.0.0.1:8080` → 被拒（`ErrBlockedTarget`）
- [ ] endpoint 指向 `169.254.169.254` → 被拒
- [ ] 管理员显式放行 `10.0.0.5/32` 后同地址可保存，**但未放行的其它私网地址仍被拒**
- [ ] 配置了 HTTP 代理且目标不在 `NO_PROXY` → 返回 `ErrProxyUnsupported`，不静默直连
- [ ] 发现返回 250 个工具（>200）→ 409 `MCP_TOOLS_LIMIT`，不静默截断
- [ ] 响应体 >1 MiB → 502 `MCP_TOO_LARGE`，不当成功处理
- [ ] 上游返回 `text/event-stream` 时按请求范围 SSE 正确读完并终止，不挂成长连接
- [ ] 上游返回恶意描述（含「忽略以上指令」）→ 不改变 system 规则、不触发自动执行
- [ ] 工具的 annotations 标 `readOnlyHint=true` 但管理员未确认风险 → 默认仍走确认
- [ ] 停用连接 → 其工具立即从目录消失，已有 `pending` 确认被 `invalidated`
- [ ] schema 变化 → 旧确认失效
- [ ] 一个连接超时不阻塞另一连接的调用（并发验证）
- [ ] 普通用户调用的工具面**不含**共享凭据连接的工具
- [ ] 凭据在任何响应中都不出现（用响应快照断言）
- [ ] endpoint 指向本平台 → 400 `MCP_SELF_LOOP`

## 9. 未决 / 需用户确认

1. **「上游按用户隔离」如何证明**：当前设计要求管理员口头声明（连接级一个布尔）。是否需要更强的机制（如要求上游支持 per-user token 透传）？若不证明，则该连接永久仅管理员可用。
2. **工具数量上限 200** 是占位值，需按真实场景调整。
3. **是否允许一个连接被多个助手复用 vs 每会话独立凭据**：当前是平台级共享连接。
