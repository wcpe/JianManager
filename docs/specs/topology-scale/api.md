# API Spec — FR-335 群组拓扑规模化

> 关联 FR: FR-335（增强 FR-145）| 优先级: P2 | 关联 ADR: ADR-007（M:N 注册模型，沿用）| 状态: 草拟

## 概述

两项只读契约变更，消除群组页双 N+1：

1. 新端点 `GET /api/v1/topology`：一次返回全部 proxy 及其注册关系（含后端概要）与 network 成员归属，供拓扑图单请求渲染。
2. 既有 `GET /api/v1/networks` 概要响应**增列** `memberStatus` 成员健康计数桶（纯增字段），供列表页免详情请求渲染健康分布。

## Endpoints

### GET /api/v1/topology（新增）

- **描述**: 全量群组拓扑聚合——所有 `role=proxy` 实例概要 + 各自注册列表（条目与 `GET /proxies/:id/registrations` 的 `RegistrationView` 同构）+ network 成员归属索引。service 层固定次数 IN 查询组装，无 N+1。
- **关联 FR**: FR-335（消费方：`TopologyGraph` 拓扑视图）
- **权限**: 平台管理员（与 `/proxies/:id/registrations`、`/networks` 同组，`RequireRole(platform_admin)` 中间件）
- **Query**: 无（全量；按 network 过滤为后续预留，本期不做）
- **响应** (200):
  ```json
  {
    "proxies": [
      {
        "id": 30,
        "name": "velocity-main",
        "status": "RUNNING",
        "serverPort": 25565,
        "nodeId": 1,
        "registrations": [
          {
            "id": 5,
            "proxyId": 30,
            "backendId": 21,
            "alias": "lobby",
            "priority": 0,
            "forcedHost": "",
            "restricted": false,
            "enabled": true,
            "backend": {
              "id": 21,
              "name": "lobby",
              "role": "backend",
              "nodeId": 1,
              "serverPort": 25566,
              "status": "RUNNING"
            }
          }
        ]
      }
    ],
    "networks": [
      { "id": 1, "name": "survival", "memberInstanceIds": [30, 21] }
    ]
  }
  ```
  - `proxies[].registrations` 条目与既有 `GET /proxies/:id/registrations` 响应元素同构（`model.ServerRegistration` JSON + `backend` 概要）；后端实例已删时 `backend: null`（既有容错语义）。
  - `registrations` 排序 `priority asc, id asc`（与单代理列表一致）。
  - `networks[].memberInstanceIds` 为实例数值 ID（含 proxy 与 backend），供前端分组布局；悬空成员（实例已删）不出现。
  - 无 proxy 时 `proxies: []`（前端空态）。
- **错误**: `403 FORBIDDEN`（非平台管理员，中间件统一）；`500 INTERNAL_ERROR`。错误体 `{ "error": "<码>", "message": "<中文>" }`。

### GET /api/v1/networks（变更：响应增字段）

- **描述**: 群组概要列表。**新增** `memberStatus`：成员实例按运行状态的计数桶（五态零补齐）；`memberCount` 口径修正为 JOIN 实例表后实际存在的成员数（悬空关系不计，与 `GET /networks/:id` 成员列表口径一致）。
- **关联 FR**: FR-335（原 FR-032）
- **权限**: 平台管理员（不变）
- **响应** (200):
  ```json
  [
    {
      "id": 1,
      "uuid": "9c0e…",
      "name": "survival",
      "description": "生存服群组",
      "memberCount": 3,
      "memberStatus": { "running": 2, "stopped": 1, "crashed": 0, "starting": 0, "stopping": 0 },
      "createdAt": "2026-07-01T08:00:00Z"
    }
  ]
  ```
  - `memberStatus` 键固定五态（`running/stopped/crashed/starting/stopping`），无成员时全 0；`memberCount` = 五桶之和。
- **错误**: `500 INTERNAL_ERROR`（不变）。
- **兼容性**: 纯增字段 + `memberCount` 口径修正（悬空成员剔除，数值只可能变小）；既有消费方（`api/networks.ts` `NetworkSummary`）向后兼容。

## 与数据模型 / 架构一致性

- 读 `instances`（`role`/`status`/`server_port`/`node_id` 均现有列）、`server_registrations`、`networks`、`network_members`，无 schema 变更。
- 纯 CP 内 REST 只读聚合，无 gRPC / Worker 变化，符合 ARCHITECTURE 通信协议约束。
- TS 类型可直接派生：
  ```ts
  interface TopologyResponse {
    proxies: {
      id: number; name: string; status: string; serverPort: number; nodeId: number
      registrations: Registration[]   // 复用 api/registrations.ts 既有 Registration
    }[]
    networks: { id: number; name: string; memberInstanceIds: number[] }[]
  }
  interface MemberStatusCounts { running: number; stopped: number; crashed: number; starting: number; stopping: number }
  // NetworkSummary 增：memberStatus: MemberStatusCounts
  ```
