# 实施计划 — 首次启动引导流程

> 关联 FR: FR-017 | 优先级: P0 | 状态: 🔨 in-progress
>
> 期验收复核差距（2026-06-17）：功能链路通，但 `POST /setup` 请求体缺「确认密码」字段（前端有、后端 contract 未含）、用户名字符集校验缺。回退为 in-progress 待补齐。

## 背景

当前系统通过 `bootstrapAdmin` 在启动时自动创建管理员账号，密码来自配置文件或环境变量。存在以下问题：

1. 配置文件中的 `${ENV_VAR}` 语法 Viper 不支持，导致密码错误
2. 开发环境硬编码默认密码存在安全隐患
3. 生产环境需要手动配置环境变量，增加部署复杂度

**目标**: 首次启动时在 Web UI 引导管理员设置账号密码，零配置即可使用。

---

## 任务拆解

### Phase 1: 后端

- [x] 在 `internal/controlplane/router/` 新增 `setup.go`
  - `GET /api/v1/setup/status` — 查询 users 表是否存在 role=10 的账号
  - `POST /api/v1/setup` — 创建管理员并返回 JWT Token
  - 请求校验：username 3-64 字符，password 8-128 字符
  - 幂等保护：若已存在管理员则返回 409
- [x] 在 `router.go` 中注册 `/api/v1/setup/*` 路由（无需 auth 中间件）
- [x] 删除 `cmd/control-plane/main.go` 中的 `bootstrapAdmin` 函数
- [x] 删除 `config.go` 中的 `BootstrapConfig` 及相关默认值
- [x] 删除 `configs/control-plane.yaml` 中的 `bootstrap` 配置段

### Phase 2: 前端

- [x] 新建 `web/src/pages/SetupPage.tsx`
  - shadcn/ui Card 表单，包含用户名、密码、确认密码三个字段
  - 密码强度提示（最低 8 字符）
  - 提交后调用 `POST /api/v1/setup`，成功后存储 token 并跳转 Dashboard
  - 布局：居中卡片，品牌 Logo + 欢迎文案
- [x] 新建 `web/src/api/setup.ts`
  - `useSetupStatus()` — TanStack Query，`GET /api/v1/setup/status`
  - `useSetup()` — TanStack Mutation，`POST /api/v1/setup`
- [x] 修改 `web/src/App.tsx`
  - 启动时（无 token 时）LoginPage 检测 setup 状态
  - `setupRequired=true` → 重定向到 `/setup`
  - `setupRequired=false` → 正常显示登录页
  - 新增 `/setup` 路由（无需 AuthGuard）
- [x] 删除 `web/src/pages/LoginPage.tsx` 中的注册功能

### Phase 3: 文档同步

- [x] 更新 `docs/PRD.md`：新增 FR-017，状态 `🔨 in-progress`
- [x] 更新 `docs/API.md`：新增 setup 相关 endpoint
- [x] 更新 `docs/ARCHITECTURE.md`：前端页面结构新增 `/setup`
- [x] 更新 `configs/control-plane.yaml`：移除 bootstrap 配置段

---

## 依赖

- 无外部依赖
- FR-001（用户认证）已完成，复用 JWT 签发逻辑

---

## 风险

| 风险 | 应对方案 |
|---|---|
| 竞态：两个浏览器同时访问 /setup | POST /setup 做数据库唯一约束 + 409 错误处理 |
| 删除注册功能后管理员误操作删除自己 | 后续 FR-002 已有保护：不允许删除最后一个平台管理员 |
| 旧版升级（已有 bootstrap 创建的管理员） | GET /setup/status 返回 false，直接进登录页，无影响 |
