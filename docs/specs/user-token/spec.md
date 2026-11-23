# 功能规格：用户令牌（user_token）与设备授权登录

> 状态：修订中　·　关联 PRD：FR-560 / FR-561 / FR-562 / FR-563　·　ADR：092（待建）、076（边界沿用）

## 1. 背景与目标

当前 CLI 认证依赖 **Agent Token（`jmat_`）**，它是为 agent/CI 设计的机器凭证，与人类身份严格分离
（ADR-076 决策 1/5）。但 `jm` 重构为 gh 式后，使用者是**人**，需要一套「像 `gh auth login` 那样
登录、能在服务器上看到并吊销」的用户级令牌。

**不复用 `agent_token` 表**：审计主体必须分离（agent vs user），合并会破坏 ADR-076 决策 5。

## 2. 数据模型（FR-560）

```go
type UserToken struct {
    ID       uint
    UserID   uint   // 所属用户
    Name     string // 用户可读名称，如「我的笔记本」
    // 设备标识，device flow 时由 CLI 上报（hostname + os）
    DeviceLabel string

    TokenHash   string // sha256 十六进制，库内不存明文
    TokenPrefix string // jmut_ab12，列表识别用（char(16)）

    // 独立 scope：与用户自身 RBAC 权限取交集，不能放大
    ScopedNodeIDs     string // JSON 数组
    ScopedInstanceIDs string // JSON 数组
    Capabilities      string // JSON 数组，复用 agent 的 capability 常量

    ExpiresAt  time.Time
    Revoked    bool
    LastUsedAt *time.Time
    CreatedAt  time.Time
}
```

### 令牌校验链

```
1. token_hash 命中
2. !Revoked
3. now < ExpiresAt
4. 所属 User.Status == Active
5. token.AuthVersion == user.AuthVersion   ← 新增字段，改密码即全部失效
→ 通过后得到 UserTokenPrincipal{UserID, Capabilities, Scopes}
```

### 权限取交集（关键语义）

```
有效能力 = token.Capabilities ∩ 用户当前 RBAC 权限节点
有效 scope = token.ScopedNodeIDs ∩ 用户可见节点
```

- token **不能**放大用户权限
- 用户被降权 → token 自动收窄（不需要重新签发）
- 用户 `AuthVersion` 递增（改密码/禁用）→ token 全部失效

## 3. 管理 API（FR-561）

| Method | Path | 说明 |
|---|---|---|
| POST | `/api/v1/user/tokens` | 手工创建，返回明文一次 |
| GET | `/api/v1/user/tokens` | 列出本人全部（无明文） |
| DELETE | `/api/v1/user/tokens/:id` | 吊销 |

约束：
- 仅操作本人 token；平台管理员可吊销任意（写审计）
- 越权访问返回 403（与其他 scope 外统一收敛）
- 与 `/api/v1/agent/tokens` 完全隔离，不共用 handler

## 4. Device Authorization Grant（FR-562，RFC 8628）

| Method | Path | 说明 |
|---|---|---|
| POST | `/api/v1/oauth/device/code` | 返回 device_code / user_code / verification_uri / expires_in / interval |
| POST | `/api/v1/oauth/device/token` | CLI 轮询；pending / slow_down / approved |
| POST | `/api/v1/oauth/device/authorize` | 用户在已登录会话下确认（需 JWT） |

### 状态机

```
pending ──approve──▶ approved ──(CLI 取走一次)──▶ consumed
   │
   ├──deny──▶ denied
   └──600s──▶ expired
```

### 参数

- `user_code`：8 位易读字符（去掉 0/O/1/I/l），形如 `ABCD-1234`
- `interval`：5s；轮询快于该值返回 `slow_down`（RFC 要求，防轮询风暴）
- `expires_in`：600s
- `device_code` 与 `user_code` 都入库，用后即删（或标记 consumed）

### 前端授权页（FR-563 的一部分）

- 路由 `/device`（或 `/oauth/device`），未登录先跳登录
- 展示：user_code、申请方 device_label、将获得的 scope 摘要
- 两个按钮：授权 / 拒绝
- 授权成功后调用 `authorize` 并签发 user_token（明文只返回给 CLI 那条通道）

## 5. 前端 Token 管理页（FR-563）

- 路径 `/settings/tokens`
- 列表：名称、设备、前缀、scope 摘要、最后使用、过期、状态
- 操作：创建（含 scope 选择器）、吊销（二次确认）
- 明文仅创建后展示一次，提供复制按钮与「关闭后不可再查看」提示
- 空态与 i18n（`userTokens.*`）

## 6. 安全要求

| 要求 | 说明 |
|---|---|
| 明文只返回一次 | 创建响应与 device token 轮询成功响应各一次 |
| 库内只存 hash | sha256 十六进制，不存明文、不存可逆密文 |
| 前缀可识别 | `jmut_` 与 agent 的 `jmat_` 明确区分 |
| 常量时间比较 | hash 比对用 `subtle.ConstantTimeCompare` |
| 速率限制 | device/token 轮询按 user_code 限流 |
| 审计 | 创建/吊销写 audit；调用写 `agent_call_logs` 并标 `actor_kind=user_token` |
| 不走 JWT 中间件 | user_token 需要独立中间件，不能塞进 `JWTAuth` |

## 7. 不做

- ❌ 复用 `agent_token` 表
- ❌ token 在线编辑 scope（沿用 ADR-080 决策 6：改权限 = 吊销重发）
- ❌ 第三方 IdP / OIDC
- ❌ 刷新令牌（user_token 是长期凭证，过期就重登）

## 8. 任务拆分

- [ ] `model/user_token.go` + AutoMigrate
- [ ] `service/user_token.go`：签发 / 校验 / 吊销 / 交集计算
- [ ] `middleware/user_token_auth.go`：与 `JWTAuth` 并列，不混用
- [ ] `router/user_token.go`：三个管理端点
- [ ] `model/device_code.go` + `service/device_flow.go`（状态机与限流）
- [ ] `router/oauth_device.go`：三个 device 端点
- [ ] 前端：`/settings/tokens` 管理页 + `/device` 授权页
- [ ] 测试：签发/校验/过期/吊销/交集收窄/AuthVersion 失效/越权/轮询限流
- [ ] 真机：浏览器完成一次 device flow，`jm api` 调通，Web 吊销后立即 401

## 9. 验收要点

- 用户 A 无法列出/吊销用户 B 的 token（403）
- 用户降权后，其 token 的有效能力同步收窄（不需重新签发）
- 改密码后该用户全部 token 立即失效
- Web 上吊销某个 token 后，用该 token 的 `jm api` 立刻返回 401
- device flow 全流程 < 60 秒完成（不含用户操作时间）
