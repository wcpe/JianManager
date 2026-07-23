# 功能规格：Agent Token 与 CP 侧鉴权真源

> 状态：草拟（并行前置，随 §4 预对齐落 dev）　·　关联 PRD：FR-384　·　关联 ADR：076　·　分支：dev / feature 实现期

## 1. 背景与目标

让 AI 助手 / 常驻 agent / 脚本·CI 经统一入口操控 JianManager 后台，且**危险权限默认拒绝、资源 scope 可收敛、写操作白名单、审计可区分 agent 与人**。

CP 为策略唯一真源；CLI / MCP / curl 只持 Token 调同一 Agent API。

**阶段**：P1 · 三入口地基。

## 2. 需求（要什么）

### 范围内
- 表 `agent_tokens`：id、name、token_hash、scoped_instance_ids、scoped_node_ids、write_allowlist、created_by、created_at、expires_at、revoked_at、last_used_at
- API：`POST/GET /api/v1/agent/tokens`、`DELETE /api/v1/agent/tokens/:id`（吊销）
- 明文 Token **仅创建响应返回一次**；库只存 hash
- 中间件 `agent_token`：`Authorization: Bearer <token>` → `principal.kind=agent`
- 策略引擎 `ResolveAction(action, principal) → allow|deny`：
  - **硬拒绝**（永不对 agent 开放）：用户/组/权限变更、平台设置、DB 浏览、自更新、删节点/实例、kill 强杀、制品/密钥管理、审计日志删除
  - **默认只读**：列表/状态/指标/日志（受 scope 收敛）
  - **MVP 写白名单默认**：`instance.life`（start/stop/restart，无 kill）、`node.maintenance`（enter/leave）
  - scope：实例 ID / 节点 ID 白名单；越界 403
- 审计：`actor_kind=agent`、`actor_id=token_id`、`actor_name=<name>`
- Agent 运维 API 面（与 CLI/MCP 对齐）：whoami、list nodes/instances、instance status/metrics/logs、instance start|stop|restart、node maintenance enter|leave

### 不做
- 不复用人类 JWT 当 PAT；不做 mTLS；不做 agent 内置审批流
- 不开放业务高危写（经济/背包 ADR-029）
- 不经 jmctl / daemon socket（紧急 CLI 与本 FR 正交）

## 3. 设计（怎么做）

- 模块：`internal/controlplane/model`、`service`、`middleware`、`router`
- Token 生成：密码学安全随机；hash 存库（bcrypt/argon2 或 HMAC-SHA256，与项目既有密钥习惯对齐）
- 与 JWT 中间件并存：Bearer 先尝试 agent token，失败再 JWT（或按前缀路由，实现时择一写清）
- 策略表硬编码硬拒绝集合 + DB 可配置 write_allowlist / scope
- UI 契约见 §7（FR-387 消费）

架构决策见 **ADR-076**。

## 4. 任务拆分

- [ ] model + 迁移 `agent_tokens`
- [ ] Token 颁发/吊销/列表 service + 单元测试
- [ ] 中间件 + ResolveAction + 硬拒绝表
- [ ] 路由：token CRUD + agent 运维读/写端点
- [ ] 审计 actor_kind 贯通
- [ ] 文档：API.md 节、PRD 状态开发中

## 5. 验收标准

1. 管理员可颁发/列表/吊销 Token；明文仅创建时返回
2. scope 内读 200；scope 外 / 硬拒绝 / 写白名单外 → **403**（非 5xx）
3. 吊销或过期后立即失效
4. 审计可按 `actor_kind=agent` 过滤
5. 单测覆盖：越权、吊销、过期、写白名单外
6. **真机**：docker-compose 或本机 CP 上完整走一遍颁发→调用→吊销

## 6. 风险 / 待定

- Token 轮换与多活跃 Token 并存策略（MVP：吊销旧的再发新的即可）
- 组管理员能否颁 Token：默认仅平台管理员（若放宽须再收 scope）

## 7. UI 契约（FR-387，免独立 spec）

- 设置 → Agent Token：列表 name/scope/写白名单/有效期/状态
- 新建：name、实例多选、节点多选、写白名单多选、有效期
- 创建成功弹窗展示一次性明文 + 复制 `JM_AGENT_TOKEN` / `jm-agent` 示例
- 吊销确认；非管理员 403
