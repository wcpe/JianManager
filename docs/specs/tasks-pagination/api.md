# API Spec — FR-337 任务中心列表分页信封

> 关联 FR: FR-337（增强 FR-183，筛选沿 FR-227，轮询沿 FR-329） | 优先级: P2 | 变更类型: **既有端点破坏性响应形态变更**（裸数组 → 信封，无双形态过渡）

## 概述

`GET /api/v1/tasks` 响应由裸 `[]Task` 切换为分页信封 `{items,total,limit,offset}`，新增 `offset` 参数。`GET /tasks/:taskId`、`POST /tasks/:taskId/cancel` 不变。前端/devmock/测试同一变更内迁移（消费点清单见 spec §3.3）。

## GET /api/v1/tasks

- **描述**: 任务列表（`created_at DESC, id DESC` 倒序）分页信封。非平台管理员只见自己发起的（`total` 同口径），平台管理员见全部
- **关联 FR**: FR-337（信封）/ FR-183（任务中心）/ FR-227（筛选）
- **权限**: 所有认证用户（归属隔离在 service 层收敛）
- **Query**:

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `limit` | int | 否 | 每窗行数，缺省 100，钳制 [1,500] |
| `offset` | int | 否 | 偏移，缺省 0，负值归 0 |
| `kind` | string | 否 | 任务种类（jdk_install / runtime_install / pkg_install / provision / import / clone / backup_create / backup_restore …） |
| `state` | string | 否 | pending / running / succeeded / failed / canceled |
| `nodeId` | int | 否 | 节点 id |
| `keyword` | string | 否 | 标题/详情模糊 |
| `since` / `until` | string | 否 | RFC3339 创建时间界 |

（筛选参数语义与 FR-227 现状完全一致；非法整数/时间沿既有行为忽略该项。）

### 响应（200）

```json
{
  "items": [
    {
      "id": 1,
      "taskId": "3fa8…-uuid",
      "nodeId": 2,
      "kind": "jdk_install",
      "state": "running",
      "progress": 42,
      "title": "安装 Temurin 21",
      "detail": "下载中…",
      "error": "",
      "result": "",
      "cancelRequested": false,
      "createdBy": 1,
      "createdAt": "2026-07-15T08:00:00Z",
      "updatedAt": "2026-07-15T08:01:00Z"
    }
  ],
  "total": 342,
  "limit": 100,
  "offset": 0
}
```

- `items[]` 元素字段与既有 Task JSON 完全一致（见 API.md `GET /api/v1/tasks` 现行条目），仅外层包裹信封
- `total`：同筛选 + 同归属隔离口径的命中总数
- `limit` / `offset`：服务端实际生效值回显（钳制后）

### 错误码

| HTTP | error | 触发 |
|---|---|---|
| 401 | `UNAUTHORIZED` | 未认证 |
| 500 | `INTERNAL_ERROR` | 查询失败 |

（`limit/offset` 非法整数不 400，沿本端点既有「解析失败忽略取缺省」行为，保持筛选参数容错一致性。）

### TypeScript 类型（可直接生成）

```ts
export interface TaskPage {
  items: Task[]      // Task 同 api/tasks.ts 现有 interface，不变
  total: number
  limit: number
  offset: number
}
export interface TaskListParams {
  limit?: number
  offset?: number
  kind?: string
  state?: TaskState | ''
  nodeId?: number
  keyword?: string
  since?: string
}
```

## 不变端点（列出以示边界）

### GET /api/v1/tasks/:taskId
- 响应 `{ "task": {…}, "logs": [{ id, taskId, seq, line, ts }] }` 不变（FR-183）

### POST /api/v1/tasks/:taskId/cancel
- 语义/错误码不变（FR-227：404 越权或不存在；409 `ALREADY_TERMINAL`）

## 一致性核对（gate-api）

- 通信协议：浏览器 → CP HTTP；任务快照仍经 Worker 心跳 gRPC 汇聚（ADR-040），本变更不触碰
- 数据模型：`model.Task` / `model.TaskLog` 无表结构变更；仅查询增加 COUNT 与 OFFSET
- devmock 契约镜像：`packages/devmock/src/handlers/domains/observ.ts` `GET /tasks` 同步信封
- 破坏性说明：CHANGELOG 标注；仓内消费点（TasksPage / ConsoleHeader TasksMenu / 轮询测试 / devmock）同批迁移
