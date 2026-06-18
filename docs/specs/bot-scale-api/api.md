# API Spec — Bot 规模化后端 API（FR-038）

> 关联 FR：FR-038（Bot 规模化后端 API）
> 关联 ADR：ADR-002（gRPC 节点通信）
> 状态：开发中

为 Bot 在上万数量级（≈12,800 = 50/worker × 256 workers）下提供可扩展的查询与操作 API。
替换现有 `GET /bots` 一次性返回扁平数组的实现，新增聚合摘要与批量操作。

## 设计要点

- **不全量序列化**：列表分页、摘要仅做数据库聚合计数，绝不逐条序列化全量 Bot。
- **跨组隔离下沉到 SQL**：非平台管理员的可访问实例集合（`group_instances` ⋈ 用户可访问组）作为 `WHERE instance_id IN (...)` 谓词，避免应用层逐条 `CanAccessInstance` 循环（不可扩展）。平台管理员不加该谓词。
- **`nodeId` 维度经实例联表**：Bot 表无 `node_id` 列，按节点筛选/分组通过 `bots.instance_id → instances.id → instances.node_id` 联表实现。
- **批量委托复用既有 per-bot gRPC**：复用 Worker 既有 `CreateBot/DeleteBot/SetBotBehavior` RPC，Control Plane 按节点分片、并发委托、非阻塞返回；不新增批量 gRPC（详见下文「批量委托」）。

---

## Endpoints

### GET /api/v1/bots

- **描述**: Bot 列表，分页 + 多维筛选。替换原扁平数组返回。
- **关联 FR**: FR-038
- **权限**: `bot:read`（资源级按可访问实例隔离）
- **Query 参数**:

  | 参数 | 类型 | 默认 | 说明 |
  |---|---|---|---|
  | `page` | int | 1 | 页码，从 1 起；< 1 归一为 1 |
  | `pageSize` | int | 20 | 每页条数；范围 [1, 100]，越界裁剪 |
  | `instanceId` | uint | — | 按实例过滤 |
  | `nodeId` | uint | — | 按节点过滤（经实例联表） |
  | `status` | string | — | 按状态过滤（`pending`/`connecting`/`connected`/`error`/`stopped`） |
  | `behavior` | string | — | 按行为过滤 |
  | `q` | string | — | 关键字，匹配 `name` 或 `uuid`（大小写不敏感 `LIKE`） |

- **响应 200**:
  ```json
  {
    "items": [
      {
        "id": 1,
        "uuid": "550e8400-e29b-41d4-a716-446655440000",
        "instanceId": 1,
        "name": "GuardBot",
        "status": "connected",
        "behavior": "guard",
        "config": "{\"server\":\"mc.example.com\",\"port\":25565,\"auth\":\"offline\"}",
        "workerId": "node-uuid",
        "createdAt": "2026-06-18T10:00:00Z",
        "updatedAt": "2026-06-18T10:00:00Z"
      }
    ],
    "total": 1,
    "page": 1,
    "pageSize": 20
  }
  ```
  - `items`：当前页 Bot（按 `id` 升序）。
  - `total`：满足筛选条件（且鉴权可见）的总条数。
  - 非平台管理员：`items`/`total` 仅含其可访问实例下的 Bot。

- **错误**:
  | HTTP | error code | 场景 |
  |---|---|---|
  | 403 | `FORBIDDEN` | 无 `bot:read` 权限 |
  | 500 | `INTERNAL_ERROR` | 查询失败 |

### GET /api/v1/bots/summary

- **描述**: 全局或分组的 Bot 计数聚合，不返回逐条 Bot。
- **关联 FR**: FR-038
- **权限**: `bot:read`（资源级按可访问实例隔离）
- **Query 参数**:

  | 参数 | 类型 | 默认 | 说明 |
  |---|---|---|---|
  | `groupBy` | string | — | `instance`/`node`/`status`/`behavior`，缺省仅返回全局计数 |
  | `instanceId` `nodeId` `status` `behavior` `q` | — | — | 同 `GET /bots`，先过滤再聚合 |

- **响应 200（无 `groupBy`）**:
  ```json
  {
    "total": 12800,
    "byStatus": {
      "connected": 12000,
      "connecting": 500,
      "error": 200,
      "pending": 50,
      "stopped": 50
    }
  }
  ```
  - `total`：可见范围内 Bot 总数。
  - `byStatus`：始终返回，便于概览卡片（总计/在线/连接中/异常）；只含计数 > 0 的状态。

- **响应 200（`groupBy=instance|node|status|behavior`）**:
  ```json
  {
    "total": 12800,
    "byStatus": { "connected": 12000, "connecting": 800 },
    "groupBy": "instance",
    "groups": [
      { "key": "1", "label": "生存服", "total": 50, "online": 48 },
      { "key": "2", "label": "空岛服", "total": 50, "online": 50 }
    ]
  }
  ```
  - `groups[].key`：分组键（instance/node 为其 ID 字符串；status/behavior 为该值本身）。
  - `groups[].label`：可读名（instance→实例名、node→节点名；status/behavior→键本身）。
  - `groups[].total`：该组 Bot 数。
  - `groups[].online`：该组 `status=connected` 的 Bot 数（健康条用）。
  - 仅做 DB 聚合（`COUNT` + `GROUP BY`），不序列化任何 Bot 行。

- **错误**: 同 `GET /bots`；`groupBy` 非法值 → 400 `INVALID_REQUEST`。

### POST /api/v1/bots/batch

- **描述**: 按 id 列表或筛选条件批量执行操作，经 gRPC 委托对应 Worker，返回成功/失败计数。
- **关联 FR**: FR-038
- **权限**: `bot:manage`（资源级按可管理实例隔离）
- **请求体**:
  ```json
  {
    "action": "set-behavior",
    "ids": [1, 2, 3],
    "filter": {
      "instanceId": 1,
      "nodeId": 2,
      "status": "connected",
      "behavior": "idle",
      "q": "guard"
    },
    "behavior": "follow",
    "target": "PlayerName"
  }
  ```
  - `action`（必填）：`set-behavior` | `start` | `stop` | `delete`。
  - 选择目标二选一：`ids`（显式 Bot id 列表）**或** `filter`（同 `GET /bots` 的筛选维度）。二者皆空 → 400。`ids` 与 `filter` 同时给出时以 `ids` 为准。
  - `behavior`：`action=set-behavior` 时必填；其余忽略。
  - `target`：`action=set-behavior` 时可选（follow 目标玩家名）。
  - 目标上限 `maxBatchTargets = 5000`，超出 → 400（避免单请求过载；超大规模请分多次或用更窄 filter）。

- **响应 200**:
  ```json
  {
    "action": "set-behavior",
    "requested": 3,
    "succeeded": 2,
    "failed": 1,
    "skipped": 0,
    "errors": [
      { "botId": 3, "error": "Worker node-x 未连接" }
    ]
  }
  ```
  - `requested`：鉴权过滤后实际尝试的目标数。
  - `succeeded` / `failed`：Worker 委托成功 / 失败计数。
  - `skipped`：请求 `ids` 中因不存在或无权限被静默剔除的数量（存在性隐藏，不泄露具体 id）。
  - `errors`：失败明细（截断至前 100 条），`botId` + 原因。
  - DB 侧状态变更（如 `delete` 软删、`stop` 置 `stopped`、`set-behavior` 改 `behavior`）即使 Worker 委托失败也按现有「失败记 warning 不阻塞」语义处理；`failed` 仅统计 Worker 委托结果。

- **动作 → Worker 委托映射**（复用既有 per-bot RPC）:
  | action | Worker RPC | DB 变更 |
  |---|---|---|
  | `set-behavior` | `SetBotBehavior` | `behavior = <behavior>` |
  | `start` | `CreateBot`（重建连接） | `status` ← RPC 返回 |
  | `stop` | `DeleteBot`（断开连接，保留行） | `status = stopped` |
  | `delete` | `DeleteBot` | 软删除该行 |

- **批量委托执行**:
  - Control Plane 先按目标 Bot 所属节点（`instance.node`）分片。
  - 各分片内并发委托（有界 worker pool，`batchConcurrency = 16`），单 Bot 委托超时 10s。
  - 请求 goroutine 等待全部分片完成后返回计数（请求处理本身不被单个慢 Worker 阻塞——超时受控；超大批次通过分片摊薄）。

- **错误**:
  | HTTP | error code | 场景 |
  |---|---|---|
  | 400 | `INVALID_REQUEST` | `action` 非法 / `ids` 与 `filter` 皆空 / `set-behavior` 缺 `behavior` / 超过 `maxBatchTargets` |
  | 403 | `FORBIDDEN` | 无 `bot:manage` 权限 |
  | 500 | `INTERNAL_ERROR` | 目标解析查询失败 |

---

## 鉴权与隔离（沿用 `bot:*` + 跨组隔离）

- `bot:read` → 列表/摘要；`bot:manage` → 批量。
- 平台管理员：全量可见，查询不加实例谓词。
- 组成员/组管理员：可见实例 = `group_instances` 中 `group_id ∈ 用户可访问组` 的 `instance_id` 集合。
  列表/摘要/批量目标解析统一以该集合做 `WHERE instance_id IN (...)` 收敛。
- 空集合（用户不属于任何含实例的组）→ 列表 `items=[] total=0`、摘要 `total=0`、批量 `requested=0`。
- 批量 `ids` 中越权/不存在的 id 计入 `skipped`，不返回 404、不泄露存在性（与单 Bot `GET`/`DELETE` 的 404 存在性隐藏一致）。

## TypeScript 类型映射（web/src/api/bots.ts）

```ts
export interface BotListResponse { items: BotInfo[]; total: number; page: number; pageSize: number }
export interface BotSummaryGroup { key: string; label: string; total: number; online: number }
export interface BotSummary { total: number; byStatus: Record<string, number>; groupBy?: string; groups?: BotSummaryGroup[] }
export type BotBatchAction = 'set-behavior' | 'start' | 'stop' | 'delete'
export interface BotBatchFilter { instanceId?: number; nodeId?: number; status?: string; behavior?: string; q?: string }
export interface BotBatchRequest { action: BotBatchAction; ids?: number[]; filter?: BotBatchFilter; behavior?: string; target?: string }
export interface BotBatchResult { action: string; requested: number; succeeded: number; failed: number; skipped: number; errors: { botId: number; error: string }[] }
```
