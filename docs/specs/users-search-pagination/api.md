# API Spec — FR-336 用户目录服务端搜索分页

> 关联 FR: FR-336（增强 FR-003/156） | 优先级: P2 | 变更类型: 既有端点加性扩展（双形态兼容）

## 概述

`GET /api/v1/users` 增加可选 `q/limit/offset` 查询参数：带 `limit` 返回分页信封，不带保持旧裸数组（兼容 FR-002 既有契约，旧调用方零改动）。无新增/删除端点；`GET/PUT/DELETE /users/:id` 不变。

## GET /api/v1/users

- **描述**: 用户列表。可选服务端搜索（`q` 用户名模糊）与分页（`limit/offset`）；排序统一 `username ASC, id ASC`
- **关联 FR**: FR-336（FR-002 兼容形态）
- **权限**: 平台管理员（`protected` + `RequireRole(RolePlatformAdmin)` 路由组；对应权限节点 `user.read`）
- **Query**:

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `q` | string | 否 | 用户名模糊匹配（子串，`%`/`_` 由服务端转义）；两种响应形态下均生效 |
| `limit` | int | 否 | **形态开关**：携带即返回信封。钳制 [1,500]（>500 按 500） |
| `offset` | int | 否 | 偏移，默认 0，负值归 0；仅信封形态生效 |

### 响应（200，带 `limit` → 分页信封）

```json
{
  "items": [
    { "id": 1, "uuid": "u-xxx", "username": "admin", "role": 10, "status": 0, "createdAt": "2026-06-01T08:00:00Z" }
  ],
  "total": 1042,
  "limit": 50,
  "offset": 0
}
```

- `items[]` 字段与旧裸数组元素完全一致：`id`(number)、`uuid`(string)、`username`(string)、`role`(number：0 成员 / 1 组管理员 / 10 平台管理员)、`status`(number：0 启用 / 1 禁用)、`createdAt`(RFC3339)
- `total`：与 `q` 条件同源的命中总数（非本页行数）
- `limit`：服务端实际生效值（钳制后回显）；`offset`：实际生效偏移

### 响应（200，不带 `limit` → 旧裸数组，兼容形态）

```json
[
  { "id": 1, "uuid": "u-xxx", "username": "admin", "role": 10, "status": 0, "createdAt": "2026-06-01T08:00:00Z" }
]
```

### 错误码

| HTTP | error | 触发 |
|---|---|---|
| 400 | `INVALID_REQUEST` | `limit`/`offset` 非法整数 |
| 401 | `UNAUTHORIZED` | 未认证 |
| 403 | `FORBIDDEN` | 非平台管理员 |
| 500 | `INTERNAL_ERROR` | 查询失败 |

### TypeScript 类型（可直接生成）

```ts
export interface UserInfo {
  id: number
  uuid: string
  username: string
  role: number
  status: number
  createdAt: string
}
export interface UserPage {
  items: UserInfo[]
  total: number
  limit: number
  offset: number
}
export interface UserSearchParams {
  q?: string
  limit?: number
  offset?: number
}
```

## 一致性核对（gate-api）

- 通信协议：浏览器 → CP HTTP，无 Worker/gRPC 参与，符合 ARCHITECTURE.md
- 数据模型：`model.User` 现有字段投影（`id/uuid/username/role/status/created_at`），无表结构变更
- devmock 契约镜像：`packages/devmock/src/handlers/domains/identity.ts` `GET /users` 同步双形态
