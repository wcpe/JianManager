# ADR-104: 用户令牌与 Agent Token 的凭证分离

- **日期**: 2026-11-12
- **状态**: accepted
- **关联**: [ADR-076](076-agent-token-and-mcp-authority.md)（Agent Token 与策略真源）· [ADR-080](080-agent-capability-policy-v2.md)（能力策略 v2）· [ADR-101](091-agent-knowledge-and-risk-annotation.md)（知识分发）· spec `docs/specs/user-token/`

## 上下文

`apps/jmagent` 原定位是「仿 jmctl 的 Agent 管理 CLI」，认证走 **Agent Token（`jmat_`）**——
它是为 agent / CI 设计的机器凭证，与人类身份严格分离（ADR-076 决策 1/5）。

但 `jm` 重构为 gh 式（FR-517）后，使用者变成**人**：日常在终端里登录、执行 `jm api`、在服务器上
查看并按需吊销。Agent Token 不适合承担这个角色：

1. **审计主体错位**：Agent Token 的调用记为 `actor_kind=agent`，人类操作应记为 `user`
2. **无法自助管理**：Agent Token 由管理员通过 `/api/v1/agent/tokens` 签发，普通用户拿不到、
   也看不到自己的有哪些
3. **语义混淆**：一个用户在 CI 里的机器凭证与在自己笔记本上的登录凭证混在同一张表，
   吊销时无法区分「我换电脑了」和「这条 CI 管道要停了」

同时，现有 JWT 会话机制也不适合 CLI：

- access token 只有 15 分钟，CLI 需要长期凭证
- 无按 token 吊销，只有全局 `AuthVersion` 递增（改密码踢掉所有设备）

## 决策

1. **新建 `user_token` 表，不复用 `agent_token`**。
   两张表、两套 handler、两个中间件并存：
   - `agent_token`（`jmat_`）：机器凭证，管理员签发，供 MCP / CI / curl
   - `user_token`（`jmut_`）：人类凭证，用户自助登录/吊销，供 `jm`

2. **`user_token` 采用独立 scope，与用户 RBAC 取交集**。
   令牌自身可声明 `scoped_node_ids` / `scoped_instance_ids` / `capabilities`，
   但**有效权限 = token scope ∩ 用户当前权限**。因此：
   - token 不能放大于所属用户的权限
   - 用户被降权时，其所有 token 自动收窄，无需重新签发
   - 用户 `AuthVersion` 递增（改密码 / 禁用）时，该用户全部 token 立即失效

3. **首个令牌通过 Device Authorization Grant（RFC 8628）获取**。
   不做「用户名密码换 token」的中间态，直接实现 `gh auth login` 式体验：
   CLI 显示 `user_code`，用户在浏览器（已登录会话）确认，CLI 轮询取回明文一次。

4. **凭证存系统 keyring，不落明文文件**。
   抽象 `internal/platform/keyring` 接口，三平台各自实现：
   Windows Credential Manager / macOS Keychain / Linux Secret Service。
   keyring 不可用时**报错并提示**，不静默降级为明文。

5. **`jm auth logout` 必须远端吊销**。
   与 `gh auth logout` 只删本地不同，我们的 logout 同时调
   `DELETE /api/v1/user/tokens/:id`，满足「能在服务器上取消授权」的要求。

## 理由

- **审计合规**：agent 与 user 的行为必须可区分（ADR-076 决策 5 已明确要求，
  合并两张表会直接破坏这条既有决策）。
- **最小惊讶**：用户在 Web 上看到的「我的令牌」与 `jm auth status` 显示的是同一批记录。
- **降权即收窄**优于「降权即失效」：前者不打断用户，后者会导致一次权限调整把所有设备踢下线。
- **Device flow 优于密码**：不在 CLI 里处理密码，不给钓鱼留面；用户可复用已有 Web 登录态。
- **keyring 优于明文**：CLI 凭证泄露的直接后果是 CP 全权访问，值得多写三套平台代码。

## 后果

- 新增两套认证路径并行：`JWTAuth`（浏览器）、`UserTokenAuth`（CLI）、`AgentAuth`（MCP/agent）。
  三者**不得**在同一路由组混用，各自独立分组。
- `user_token` 需要新增 `AuthVersion` 字段以支持「改密码全失效」。
- 前端新增两页：`/settings/tokens`（管理）与 `/device`（授权确认）。
- Device flow 需要 `device_code` 表与限流逻辑（轮询过频返回 `slow_down`）。
- 权限交集计算需在两处保持一致：中间件校验与 `whoami` 输出，
  否则用户会看到与实际不符的能力列表。

## 关系

- **不推翻** ADR-076 决策 1（专用 Agent Token）、决策 5（审计主体分离）；
  本 ADR 只是为人类 CLI 场景新增一套并行的凭证，agent 侧语义完全不变。
- **沿用** ADR-080 决策 6（能力与 scope 不在线编辑）：用户令牌同样「改权限 = 吊销重发」。
- 与 ADR-101 正交：知识分发不涉及鉴权。
