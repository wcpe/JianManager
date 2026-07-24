# 功能规格：节点离线归档与彻底清理

> 状态：开发中　·　关联 PRD：FR-393 / FR-394　·　增强：FR-048 / FR-309　·　分支：dev  
> 计划：`.tmp/brainstorm-node-archive-2026-07-24.md`

## 1. 背景与目标

FR-048 已支持离线节点软删下线，FR-309 守卫名下实例；GORM 默认 `List` 不返回软删行，主列表会消失。

**缺口**：无归档入口查看已下线节点；无硬删清理路径。用户感知为「删不掉 / 找不到已下线记录」。

本批补齐：

| FR | 目标 |
|----|------|
| **FR-393** | 归档列表 + NodesPage「归档」分段，只读可查已软删节点 |
| **FR-394** | 归档「清理」硬删库记录（可选 force 级联实例记录，不碰远端文件） |

**阶段**：P1。**真机门禁**：docker compose **仅 1 CP + 1 Worker**。

## 2. 需求（要什么）

### 2.1 FR-393 范围内

1. **主列表不变**：`GET /api/v1/nodes` 仅活跃节点（现有 GORM 默认，保持）。
2. **归档列表**：新增只读接口列出已软删（`deleted_at IS NOT NULL`）节点摘要。
3. **归档详情**（可选同列表字段即可）：名称、host、原 status、os/arch、下线时间（`deletedAt`）、创建时间；**不**暴露 secret。
4. **NodesPage UI**：
   - 页面级分段/Tab：`活跃` | `归档`（URL 可寻址，如 `?view=active|archive`，默认 active）。
   - 归档列表只读摘要；点选可看详情面板（精简：无维护/排空/JDK 等写操作）。
   - 文案区分「下线」（软删进归档）与后续「清理」。
5. **在线安全闸沿用** FR-048/309：在线拒绝下线；有实例未 force → 409 + 实例清单。
6. **devmock**：归档列表 mock 数据，供 UI DOM 测。

### 2.2 FR-394 范围内

1. **硬删 API**：仅对**已软删**节点生效；活跃节点调硬删 → 404 或 422（推荐 422「请先下线再清理」）。
2. **有残留实例记录**（含已软删实例若仍挂 node_id——按当前模型：下线 force 已软删实例，清理时 Unscoped 硬删级联）：
   - 未 `force` → 409 + 实例摘要（与 FR-309 同形，便于前端复用）。
   - `force=true` → 同事务 **Unscoped 硬删** 节点 + 名下实例平台记录及组/群组服/群组成员关联行。
3. **不清理远端文件**；UI/API 文案明示。
4. **DangerConfirm**：输入节点名二次确认。
5. **审计**：`node.purge`（或 `node.hard_delete`），detail 含 `instancesPurged`、force。
6. 清理后：主列表与归档列表均不可见；`GET /nodes/:id` 404；Unscoped 查库无行。

### 2.3 不做（范围外）

- 在线一键下线、心跳自动归档  
- 归档「恢复回主列表」  
- 远端文件 / Worker 磁盘清理  
- 节点迁移、重命名归档行  
- 改动 enroll 身份模型（ADR-039 仍有效）

## 3. 设计（怎么做）

### 3.1 数据模型

- **无新表**。继续用 `nodes.deleted_at`（GORM soft delete）。
- 归档读：`db.Unscoped().Where("deleted_at IS NOT NULL")`。
- 硬删：`db.Unscoped().Delete(...)`。
- JSON：归档响应显式输出 `deletedAt`（RFC3339）；活跃节点响应可不带或 `null`。

```go
// 响应加性字段（归档列表项）
type ArchivedNode struct {
    // 复用 Node 公开字段 + DeletedAt 序列化
    DeletedAt time.Time `json:"deletedAt"`
}
```

实现可选：在 handler 映射，或临时 `json:"deletedAt"` 包装，避免改动活跃节点的 `json:"-"` 语义造成前端噪音——**推荐归档专用 DTO**，不把 `DeletedAt` 直接改到活跃 Node 的 `json` 标签。

### 3.2 API

#### GET /api/v1/nodes/archived

- **描述**: 已下线（软删）节点列表，按 `deleted_at` 倒序  
- **关联 FR**: FR-393  
- **权限**: 平台管理员（与 `GET /nodes` 一致）  
- **响应** (200):  
  ```json
  [
    {
      "id": 12,
      "uuid": "…",
      "name": "old-worker",
      "host": "10.0.0.2",
      "grpcPort": 50051,
      "wsPort": 8081,
      "status": 0,
      "os": "linux",
      "arch": "amd64",
      "cpuCores": 4,
      "memoryMb": 8192,
      "maintenance": false,
      "lastHeartbeat": "2026-07-20T10:00:00Z",
      "createdAt": "…",
      "updatedAt": "…",
      "deletedAt": "2026-07-24T12:00:00Z"
    }
  ]
  ```
- **说明**: 不含 `secret`；空列表 `[]`。

#### GET /api/v1/nodes/archived/:id

- **描述**: 单个归档节点详情（可选；若实现成本低则做，否则列表字段足够可省略，UI 用列表缓存）  
- **404**: 非归档或不存在  

#### DELETE /api/v1/nodes/archived/:id

- **描述**: 彻底清理（硬删）归档节点  
- **关联 FR**: FR-394  
- **权限**: 平台管理员  
- **Query**: `force`（bool，可选）— 级联硬删名下实例平台记录  
- **响应** (200): `{ "message": "已清理", "instancesPurged": 0 }`  
- **错误**:  
  - `404 NOT_FOUND` — 无此归档行  
  - `409 NODE_HAS_INSTANCES` — 有实例且未 force（体同 FR-309）  
  - `422 BUSINESS_ERROR` — 节点仍活跃（未下线）误调本接口  
- **审计**: `node.purge`

路由注册注意：`/nodes/archived` 必须注册在 `/nodes/:id` **之前**，避免 `archived` 被解析为 id。

### 3.3 Service

| 方法 | 行为 |
|------|------|
| `ListArchived()` | Unscoped + `deleted_at IS NOT NULL`，倒序 |
| `GetArchived(id)` | Unscoped 且 `deleted_at != null`，否则 ErrNodeNotFound |
| `Purge(id, force)` | 仅归档行；实例守卫同 Delete；Unscoped 级联硬删 |

`Delete`（软删）**不改语义**。`Purge` 不接受在线/活跃节点。

实例级联硬删顺序建议与 `Delete` 事务一致，但全部 `Unscoped()`：

1. group_instances / server_registrations / network_members  
2. instances（WHERE node_id）  
3. node  

若下线时已 soft-delete 实例，归档清理时仍须 Unscoped 扫到这些行（`Unscoped().Where("node_id=?", id)`）。

### 3.4 前端

- `api/nodes.ts`：`useArchivedNodes`、`usePurgeArchivedNode`  
- `NodesPage`：顶部分段 Active / Archive  
  - Active：现有列表与详情  
  - Archive：归档列表 + 只读详情 +「清理」按钮 → 有实例时先展示阻断清单 → force 时 DangerConfirm 输入节点名  
- i18n：`zh.json` / `en.json` 补文案  
- DOM 测：分段切换、归档可见、清理确认文案含「不清理远端文件」

### 3.5 模块边界

- 仅 Control Plane + Web + devmock  
- **不**改 Worker / gRPC / Bot  
- **不**新 ADR（软删/硬删均在既有 FR-048/309 模型内）

## 4. 任务拆分

### FR-393

- [ ] Service `ListArchived`（+ 可选 `GetArchived`）+ 单测  
- [ ] Router `GET /nodes/archived`（路由顺序）+ 集成/router 测  
- [ ] API.md 节  
- [ ] Web：api hook + NodesPage 归档分段 + i18n + DOM 测  
- [ ] devmock 归档列表  
- [ ] PRD 状态 → 开发中；CHANGELOG 未发布段  
- [ ] 真机 compose：离线 → 下线 → 主列表消失 → 归档可见  

### FR-394（依赖 393 UI 入口；API 可并行）

- [ ] Service `Purge` + 单测（无实例硬删 / 有实例 409 / force 级联 / 活跃拒绝）  
- [ ] Router `DELETE /nodes/archived/:id` + 审计  
- [ ] API.md  
- [ ] Web：清理按钮 + DangerConfirm + force 流 + DOM 测  
- [ ] PRD / CHANGELOG  
- [ ] 真机：清理后归档与主列表均不可见  

## 5. 验收标准

### FR-393

1. 离线节点「下线」成功 → 主列表不再出现  
2. 「归档」分段可见该节点（name/host/status/deletedAt）  
3. 在线下线仍拒；有实例未 force 仍 409  
4. `GET /nodes/archived` 仅软删行；活跃不出现  
5. 单测/DOM 测绿  
6. **真机 compose 1CP+1Worker** 走通 1–2  

### FR-394

1. 归档「清理」DangerConfirm 输入节点名  
2. 清理后主列表 + 归档均不可见；`GET /nodes/:id` 404  
3. 未 force 有实例 → 409；force 后级联删实例记录，文案明示不清理远端文件  
4. 审计可查 `node.purge`  
5. 单测/DOM 测绿  
6. **真机** Unscoped 或 API 双侧确认物理删除  

## 6. 风险 / 待定

| 项 | 建议默认 |
|----|----------|
| 归档详情独立 GET | **做** `GET /nodes/archived/:id`，实现薄，便于深链 |
| 软删实例是否仍占 node_id | 是；Purge 必须 Unscoped 查实例 |
| 同名新节点 enroll | 已有部分唯一索引（活跃名唯一）；归档行不挡新名 |
| Agent 硬拒绝 | 删节点已在 agent 硬拒绝面；Purge 同样不对 agent 开放 |

## 7. 执行序

```
写/审本 spec → 实现 FR-393 → 真机先验
                → 实现 FR-394 → 真机再验
```
