# 实施计划 — FR-027 API 集成测试

> 关联 FR: FR-027 | 优先级: P1 | 状态: 🔨 in-progress

## 背景

当前项目无任何 API 测试。需要为核心 REST API 编写集成测试，使用 httptest + 真实 SQLite 数据库。

## 任务拆解

### Phase 1: 测试基础设施

- [ ] 创建 `internal/controlplane/router/testhelper_test.go`
  - `setupTestDB(t)` — 创建临时 SQLite 文件，自动迁移
  - `setupTestRouter(db)` — 初始化完整路由（含 auth 中间件）
  - `getAuthToken(t, r)` — 注册管理员 + 登录获取 token
  - `makeRequest(r, method, path, body, token)` — 发送 HTTP 请求

### Phase 2: 认证 API 测试

- [ ] 创建 `internal/controlplane/router/auth_test.go`
  - TestRegister_Success
  - TestRegister_DuplicateUsername
  - TestLogin_Success
  - TestLogin_WrongPassword
  - TestRefresh_Success
  - TestProtectedEndpoint_NoToken

### Phase 3: 实例 API 测试

- [ ] 创建 `internal/controlplane/router/instance_test.go`
  - TestCreateInstance
  - TestListInstances
  - TestGetInstance
  - TestStartInstance
  - TestStopInstance
  - TestDeleteInstance

### Phase 4: 节点 API 测试

- [ ] 创建 `internal/controlplane/router/node_test.go`
  - TestListNodes
  - TestGetNode
  - TestDeleteOfflineNode

### Phase 5: 用户组 API 测试

- [ ] 创建 `internal/controlplane/router/group_test.go`
  - TestCreateGroup
  - TestAddGroupMember
  - TestSetQuota
  - TestQuotaExceeded

### Phase 6: 验证

- [ ] `go test ./internal/controlplane/router/...` 全部通过
- [ ] `go test -race ./internal/controlplane/router/...` 无竞态

## 产出文件范围

| 文件 | 操作 |
|---|---|
| `internal/controlplane/router/testhelper_test.go` | 新增 |
| `internal/controlplane/router/auth_test.go` | 新增 |
| `internal/controlplane/router/instance_test.go` | 新增 |
| `internal/controlplane/router/node_test.go` | 新增 |
| `internal/controlplane/router/group_test.go` | 新增 |
