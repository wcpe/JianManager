# API Spec — MCP Token 活动视图（FR-489）

> 关联 FR：**FR-489**（活动视图端点；数据源 FR-390，取代 FR-389 的会话列表/踢线端点）
> 关联 ADR：**ADR-096**（MCP 端点在 Streamable HTTP 路径上无状态化）
> 状态：开发中
> 同步落点：[../../API.md](../../API.md)「CP 内嵌 MCP」章节

本文件记录无状态化后 MCP 管理面的 API 契约：`GET /api/v1/agent/mcp/activity` 为**新增**端点，`GET/DELETE /api/v1/agent/mcp/sessions` 为**移除**端点，MCP 自身路由的语义变化见文末「同批端点变化」。

---

## GET /api/v1/agent/mcp/activity

- **描述**: 按 **Agent Token** 聚合窗口内的 MCP 活动（谁在用 MCP、最近何时用、用了什么、失败多少）。替代原「列出当前内存中的 MCP 会话」——数据源为 `agent_call_logs`（FR-390），不依赖任何进程内状态，故 CP 重启后视图连续。
- **关联 FR**: FR-489（数据源 FR-390）
- **权限**: 平台管理员 JWT；权限节点 **`agent.mcp.read`**，或 `agent.token.manage` —— 二者任一即放行（沿用原会话端点的权限门，本 FR 未改动）
- **Query 参数**:

  | 参数 | 类型 | 必填 | 说明 |
  |---|---|---|---|
  | `window` | string | 否 | Go duration 字符串（如 `24h`、`90m`）。缺省 `24h`；允许区间 **1h ~ 168h**，越界或不可解析回 400 |
- **响应 200**:

  ```json
  {
    "window": "24h0m0s",
    "generatedAt": "2026-09-28T12:00:00Z",
    "items": [
      {
        "tokenId": 7,
        "tokenName": "运维自动化",
        "tokenPrefix": "jmat_ab12",
        "lastActivityAt": "2026-09-28T11:59:00Z",
        "lastAction": "agent.whoami",
        "callCount": 128,
        "failureCount": 3,
        "clientIPs": ["10.0.0.5"],
        "clients": { "mcp": 120, "curl": 8 }
      }
    ]
  }
  ```

- **字段语义**:

  | 字段 | 类型 | 说明 |
  |---|---|---|
  | `window` | string | 回显实际生效的窗口（Go duration 的字符串形态，如 `24h0m0s`；客户端传入 `90m` 即回显 `1h30m0s`） |
  | `generatedAt` | string | 视图生成时刻（RFC3339） |
  | `items[].tokenId` | uint | Agent Token ID |
  | `items[].tokenName` | string | Token 名称：优先取 `agent_tokens` 当前值；该 Token 行已被硬删时回退为流水里签发时的名称快照 |
  | `items[].tokenPrefix` | string | Token 明文前缀（如 `jmat_ab12`），便于人工比对而不暴露明文；取自 `agent_tokens`，Token 行已硬删时为空 |
  | `items[].lastActivityAt` | string | 窗口内该 Token 的最近一次调用时刻（RFC3339） |
  | `items[].lastAction` | string | 最近一次调用的 action（如 `agent.whoami`、`agent.instance_start`） |
  | `items[].callCount` | int64 | 窗口内调用总数（含失败） |
  | `items[].failureCount` | int64 | 窗口内 `success=false` 的条数（策略拒绝记 `success=false`，故一并计入） |
  | `items[].clientIPs` | string[] | 窗口内出现过的来源 IP（去重、升序） |
  | `items[].clients` | object | 客户端标识 → 调用次数；键为 `X-JM-Agent-Client` 归一后的取值（`mcp` / `jmagent` / `curl` / `unknown`）；客户端自报名不在白名单时计为 `unknown`，故 `claude-code` 一类自报名不会作为键出现 |

- **排序**: `items` 按 `lastActivityAt` 降序；同一时刻以 `tokenId` 降序兜底（输出稳定，便于前端与测试断言）
- **无数据**: 窗口内无任何调用时返回 `items: []`（空数组，非 `null`）；`clientIPs` 与 `clients` 同理不返回 `null`
- **数据源与聚合**: `agent_call_logs`（FR-390）在窗口内 `GROUP BY token_id`；`callCount`/`failureCount` 为计数聚合，`lastAction`/`lastActivityAt` 取窗口内最新一条，`clientIPs`/`clients` 由 `GROUP BY token_id, client, ip` 的结果在服务层合并。Token 名称与前缀补自 `agent_tokens`。**不改**流水表定义与保留策略（默认保留 14 天）。
- **错误**:

  | HTTP | 错误码 | 场景 |
  |---|---|---|
  | 400 | `BAD_REQUEST` | `window` 非时长字符串（`window 须为时长字符串（如 24h）`），或超出 1h~168h（`window 允许区间 1h~168h`） |
  | 401 | `UNAUTHORIZED` | 未认证 / JWT 无效 |
  | 403 | `FORBIDDEN` | 已认证但权限节点不足 |
  | 404 | `NOT_FOUND` | 调用流水服务未启用（端点不可用） |
  | 500 | `INTERNAL_ERROR` | 聚合查询失败 |

- **类型生成**: 上述 JSON 结构可直接用于生成 TypeScript 类型（前端 hook `useMcpActivity(window)` 消费 `GET /agent/mcp/activity?window=`）。

---

## 同批端点变化

### 移除（关联 FR：FR-489；原属 FR-389）

| 方法 | 路径 | 原语义 | 替代 |
|---|---|---|---|
| GET | `/api/v1/agent/mcp/sessions` | 列出当前内存中的 MCP 会话 | `GET /api/v1/agent/mcp/activity` |
| DELETE | `/api/v1/agent/mcp/sessions/:id` | 强制踢线指定会话 | 无（无会话可踢）；需要立即阻断时吊销 Token（下一个请求即失效） |

移除后不再产生 `mcp.session.kick` 审计与 `mcp.session.open/close/kick` 流水。

### MCP 自身路由（关联 FR：FR-489 / FR-389；ADR-077/096）

| 方法 | 路径 | 变化 |
|---|---|---|
| POST | `/api/v1/mcp` | 去会话：不读也不写 `Mcp-Session-Id`；`initialize` 直接返回结果；每请求独立鉴权与授权 |
| GET | `/api/v1/mcp` | 由「会话保活」改为 **405** + `Allow: POST` |
| DELETE | `/api/v1/mcp` | 由「不注册 → 通用 404」改为 **405** + `Allow: POST`（无会话可终止；遵循 Streamable HTTP 规范对「不支持客户端终止会话」的建议，避免客户端误判为路径写错） |
| GET | `/api/v1/mcp/sse` | 不变（SSE 传输连接登记） |
| POST | `/api/v1/mcp/message` | 不变（`?sessionId=` 或 `Mcp-Session-Id` 头为 SSE 传输连接标识；缺参 400、连接不存在 404 `CONN_GONE`、不属于当前 Token 403） |

- 不再响应 `Mcp-Session-Id` 响应头；请求携带旧 session id 被**忽略而非拒绝**（唯一例外是 SSE `/message`：那里它是传输连接标识，仍须有效）。
- `404 SESSION_GONE` 与超限 `429` 不再出现；无凭据仍 401；策略拒绝仍 HTTP 200 + `result.isError=true`（中文 message）。
- 权限门与鉴权链不变：仅 Agent Token `jmat_`，人类 JWT 不得充当 MCP 凭据。
