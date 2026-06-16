# API — 首次启动引导流程

> 关联 FR: FR-017

## 概述

Control Plane 首次启动时数据库中无管理员账号。前端检测到此状态后，强制跳转到 `/setup` 引导页，由用户设置管理员用户名和密码。设置完成后自动登录进入 Dashboard。

---

## GET /api/v1/setup/status

- **描述**: 查询系统是否需要初始化（是否存在管理员账号）
- **关联 FR**: FR-017
- **权限**: 无需认证
- **响应** (200):
  ```json
  {
    "setupRequired": true
  }
  ```
- **说明**: `setupRequired=true` 表示数据库中无平台管理员（role=10）账号

---

## POST /api/v1/setup

- **描述**: 创建初始管理员账号（仅首次启动可用）
- **关联 FR**: FR-017
- **权限**: 无需认证，但仅当 `setupRequired=true` 时可用
- **请求**:
  ```json
  {
    "username": "admin",
    "password": "securePassword123"
  }
  ```
- **字段校验**:
  - `username`: 3-64 字符，仅允许字母数字下划线
  - `password`: 8-128 字符
- **响应** (201):
  ```json
  {
    "accessToken": "string",
    "refreshToken": "string",
    "expiresIn": 900
  }
  ```
- **错误码**:
  - `409` — 管理员已存在（setup 已完成，不允许重复创建）
  - `400` — 参数校验失败
- **副作用**: 创建成功后自动登录，返回 JWT Token，前端直接跳转 Dashboard

---

## 前端路由

| 路径 | 组件 | 认证要求 | 说明 |
|---|---|---|---|
| `/setup` | `SetupPage` | 无需认证 | 首次启动引导页 |

---

## 前端行为逻辑

```
App 启动
  │
  ├─ localStorage 有 token?
  │   ├─ 是 → 进入 Dashboard（AuthGuard 正常拦截）
  │   └─ 否 → GET /api/v1/setup/status
  │       ├─ setupRequired=true → Navigate to /setup
  │       └─ setupRequired=false → Navigate to /login
  │
  └─ /setup 页面
      ├─ GET /api/v1/setup/status（二次确认）
      │   └─ setupRequired=false → Navigate to /login
      └─ 提交 POST /api/v1/setup
          └─ 成功 → 存储 token → Navigate to /
```
