# API Spec — FR-032 节点资源分配与群组服关系模型

> 关联 FR: FR-032 | 优先级: P0 | 关联 ADR-007 | 状态: 🔨 in-progress

## 概述

FR-032 将实例角色化，并引入 proxy↔backend M:N 注册、Network 软标签、端口/工作目录系统分配的查询。
是搭建代理（FR-035）和复制子服（FR-036）的关系模型底座。
注册记录的「写入代理配置 + Velocity secret 下发」属 **FR-035**；本 FR 只负责关系数据与 CRUD。

## 权限

与 FR-034 一键搭建一致，本 FR 全部端点位于 **平台管理员** 路由组（`RequireRole(RolePlatformAdmin)`）。
群组服拓扑（角色/注册/网络/端口）属基础设施级操作，V2 暂不下放到组管理员。

## 数据模型增量

### Instance.role（新增字段）

| 值 | 含义 |
|---|---|
| `backend` | 后端子服（Paper/Spigot/Purpur，FR-034 搭建） |
| `proxy` | 代理（BungeeCord/Waterfall/Velocity，FR-035 搭建） |
| `universal` | 通用实例（默认；非群组服角色，保留自由命令） |

- 旧实例 grandfather 为 `universal`（AutoMigrate 默认值）。
- FR-034 bukkit provision 落 `backend`；FR-035 proxy provision 落 `proxy`。
- 实例 JSON 新增 `role`；`GET /instances` 支持 `?role=` 过滤。

### server_registrations（M:N proxy↔backend）

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | uint | 主键 |
| `proxyId` | uint | 代理实例 ID（role=proxy），索引 |
| `backendId` | uint | 后端实例 ID（role=backend），索引 |
| `alias` | string | 在该代理 `servers{}` 中的本地名（`[a-z0-9_-]{1,64}`） |
| `priority` | int | try/优先级顺序，小值优先（默认追加到末尾） |
| `forcedHost` | string | 可选；forced-host 域名 → 该后端 |
| `restricted` | bool | Velocity restricted（仅可经 forced-host/命令访问） |
| `enabled` | bool | 是否启用（默认 true） |

- 唯一索引 `(proxyId, alias)`：同一代理内别名唯一。
- 同一 backend 可注册进多个 proxy（M:N）。
- 删除 proxy/backend 实例时级联删除其注册记录。

### networks + network_members（非独占软标签）

- `networks`：`id, uuid, name, description, createdAt, updatedAt, deletedAt`
- `network_members`：`id, networkId, instanceId, createdAt`，唯一索引 `(networkId, instanceId)`
- 一个实例可属于多个 network；删除 network 不影响实例与 server_registrations（ADR-007）。

---

## Endpoints

### 注册（proxy↔backend）

#### GET /api/v1/proxies/:id/registrations
- 列出某代理已注册的后端。`:id` 为 proxy 实例 ID。
- **响应** (200): `[{ id, proxyId, backendId, alias, priority, forcedHost, restricted, enabled, backend: { id, name, role, nodeId, serverPort, status } }]`
- **错误**: `404 INSTANCE_NOT_FOUND`；`422 NOT_A_PROXY`（role≠proxy）

#### POST /api/v1/proxies/:id/registrations
- FR-032 关系落库；FR-035 同步写代理配置 + Velocity secret 下发。
- **请求**: `{ "backendId": int, "alias": "string", "priority": int?, "forcedHost": "string?", "restricted": bool?, "enabled": bool? }`
- **响应** (201): 创建的 registration（含 backend 概要）
- **错误**: `404 INSTANCE_NOT_FOUND`；`422 NOT_A_PROXY` / `422 NOT_A_BACKEND`；`409 ALIAS_CONFLICT`；`409 ALREADY_REGISTERED`

#### PATCH /api/v1/proxies/:id/registrations/:rid
- **请求**: 可选 `{ alias?, priority?, forcedHost?, restricted?, enabled? }`
- **响应** (200): 更新后的 registration
- **错误**: `404 REGISTRATION_NOT_FOUND`；`409 ALIAS_CONFLICT`

#### DELETE /api/v1/proxies/:id/registrations/:rid
- **响应** (204) — FR-035 同步从代理配置移除该 server。
- **错误**: `404 REGISTRATION_NOT_FOUND`

### 群组（Network 软标签）

#### GET /api/v1/networks
- **响应** (200): `[{ id, uuid, name, description, memberCount, createdAt }]`

#### POST /api/v1/networks
- **请求**: `{ "name": "string", "description": "string?" }`
- **响应** (201)；**错误**: `409 NETWORK_NAME_CONFLICT`

#### GET /api/v1/networks/:id
- **响应** (200): `{ id, uuid, name, description, members: [{ instanceId, name, role, nodeId, status }] }`
- **错误**: `404 NETWORK_NOT_FOUND`

#### PATCH /api/v1/networks/:id
- **请求**: `{ name?, description? }` → **响应** (200)

#### DELETE /api/v1/networks/:id
- 软删除 network。**不影响**成员实例与其 server_registrations（ADR-007）。**响应** (204)

#### POST /api/v1/networks/:id/members
- **请求**: `{ "instanceIds": [int, ...] }` → **响应** (200): `{ added, members: [...] }`（已存在幂等跳过）

#### DELETE /api/v1/networks/:id/members/:instanceId
- **响应** (204)

#### POST /api/v1/networks/:id/actions
- 对群组成员批量执行生命周期操作（按标签批量运维）。
- **请求**: `{ "action": "start" | "stop" | "restart" }`
- **响应** (200): `{ action, total, succeeded, failed, results: [{ instanceId, ok, error }] }`

### 端口/资源占用

#### GET /api/v1/nodes/:id/ports
- 查看某节点端口占用（系统分配端口的可视化）。
- **响应** (200):
  ```json
  {
    "nodeId": 1,
    "ranges": { "serverPortBase": 25565, "rconPortBase": 25575, "rangeSize": 2000 },
    "occupied": [
      { "instanceId": 3, "name": "lobby", "role": "backend", "serverPort": 25565, "rconPort": 25575, "queryPort": 25565 }
    ]
  }
  ```
- **错误**: `404 NODE_NOT_FOUND`

---

## 与既有约束一致性

- 工作目录系统分配：复用 `allocWorkDirRel`（已实现），创建对话框移除 workDir 输入，改只读展示（取代 BUG-004 必填 UI）。
- 端口系统分配：复用 `allocPortsForNode`（已实现）。
- 数据所有权：所有读写在 Control Plane；批量启停经既有 `InstanceService` → gRPC 委托 Worker。
- TS 类型：响应体 JSON 可直接映射前端 `api/networks.ts`、`api/registrations.ts`。
