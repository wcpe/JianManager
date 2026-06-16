# API Spec — FR-027 API 集成测试

> 关联 FR: FR-027 | 优先级: P1

## 概述

为核心 REST API 编写集成测试，使用 `httptest` + 真实 SQLite 数据库，验证 HTTP handler 的请求/响应正确性。

## 测试范围

### 认证 API

| 测试用例 | Endpoint | 验证 |
|---|---|---|
| 注册成功 | POST /auth/register | 201, 返回用户信息 |
| 注册重复用户名 | POST /auth/register | 409 |
| 登录成功 | POST /auth/login | 200, 返回 accessToken + refreshToken |
| 登录密码错误 | POST /auth/login | 401 |
| 刷新 token | POST /auth/refresh | 200, 返回新 token |
| 无 token 访问受保护接口 | GET /users | 401 |

### 实例 API

| 测试用例 | Endpoint | 验证 |
|---|---|---|
| 创建实例 | POST /instances | 201 |
| 查询实例列表 | GET /instances | 200, 数组 |
| 查询实例详情 | GET /instances/:id | 200 |
| 启动实例（无 Worker） | POST /instances/:id/start | 状态变更成功 |
| 停止实例 | POST /instances/:id/stop | 状态变更成功 |
| 删除实例 | DELETE /instances/:id | 200 |

### 节点 API

| 测试用例 | Endpoint | 验证 |
|---|---|---|
| 节点列表 | GET /nodes | 200 |
| 节点详情 | GET /nodes/:id | 200 |
| 删除离线节点 | DELETE /nodes/:id | 200 |

### 用户组 API

| 测试用例 | Endpoint | 验证 |
|---|---|---|
| 创建用户组 | POST /groups | 201 |
| 添加组成员 | POST /groups/:id/members | 200 |
| 设置配额 | PUT /groups/:id/quota | 200 |
| 配额超额拒绝 | POST /instances（超额时） | 422 |

## 测试架构

```
internal/controlplane/
  router/
    auth_test.go      # 认证 API 测试
    instance_test.go   # 实例 API 测试
    node_test.go       # 节点 API 测试
    group_test.go      # 用户组 API 测试
    testhelper_test.go # 共用测试辅助函数
```

## 测试辅助函数

```go
// testhelper_test.go
func setupTestDB(t *testing.T) *gorm.DB  // 创建临时 SQLite
func setupTestRouter(db *gorm.DB) *gin.Engine  // 初始化路由
func getAuthToken(t *testing.T, r *gin.Engine) string  // 获取认证 token
func makeRequest(r *gin.Engine, method, path string, body interface{}, token string) *httptest.ResponseRecorder
```
