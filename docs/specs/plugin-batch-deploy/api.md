# API Spec — FR-053 插件批量部署多服

> 关联 FR：FR-053 | 优先级：P1 | 状态：✅ accepted

## 概述

FR-053 在既有单服插件管理和制品库之上新增批量部署 API。调用方从制品库选择 `type=plugin` 的插件资产，再指定目标实例集合，Control Plane 按权限收敛目标并经 Worker `WriteFile` 扇出写入各实例 `plugins/` 目录。

## REST API

### POST /api/v1/plugins/batch-deploy

- **描述**：从制品库批量部署一个或多个插件到多个实例。
- **权限**：平台管理员或具备目标实例管理权限的用户。后端按可管理实例集合收敛目标。
- **审计动作**：`plugin.batchDeploy`。
- **Content-Type**：`application/json`。

#### 请求体

```json
{
  "assetIds": [101, 102],
  "ids": [1, 2, 3]
}
```

或使用筛选：

```json
{
  "assetIds": [101],
  "filter": {
    "nodeId": 1,
    "status": "STOPPED",
    "role": "backend"
  }
}
```

字段说明：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `assetIds` | `number[]` | 是 | 制品库插件资产 ID，至少一个，均必须为 `type=plugin`。 |
| `ids` | `number[]` | 与 `filter` 二选一 | 显式目标实例 ID。越权或不存在的 ID 计入 `skipped`。 |
| `filter` | `object` | 与 `ids` 二选一 | 批量目标筛选条件；若同时提供 `ids` 与 `filter`，返回 400。 |
| `filter.nodeId` | `number` | 否 | 限定节点。 |
| `filter.status` | `string` | 否 | 限定实例状态，取现有实例状态枚举。 |
| `filter.role` | `string` | 否 | 限定实例角色；前端首期使用显式实例选择，不默认改写 role。 |

#### 成功响应

```json
{
  "total": 3,
  "success": 2,
  "failed": 1,
  "skipped": 0,
  "results": [
    { "id": 1, "name": "lobby", "skipped": false },
    { "id": 2, "name": "survival", "skipped": false },
    { "id": 3, "name": "proxy", "skipped": false, "error": "节点未连接" }
  ]
}
```

字段说明：

| 字段 | 类型 | 说明 |
|---|---|---|
| `total` | `number` | 本次请求计入统计的实例数，包含 skipped。 |
| `success` | `number` | 所有插件均写入成功的实例数。 |
| `failed` | `number` | 任一插件写入失败的实例数。 |
| `skipped` | `number` | 显式 ID 模式下被权限/存在性收敛剔除的目标数。 |
| `results` | `array` | 每实例结果；失败写入 `error`，跳过写入 `skipped/reason`。 |

#### 结果计数语义

- 汇总按“实例”计数。
- 同一实例需要写入多个插件时：
  - 全部插件成功：该实例 `success + 1`。
  - 任一插件失败：该实例 `failed + 1`，逐实例 `error` 记录失败插件。
- 已经成功写入的插件不回滚。

#### 错误响应

| HTTP 状态 | error | 场景 |
|---|---|---|
| 400 | `INVALID_REQUEST` | 请求体格式错误、`assetIds` 为空、`ids/filter` 均为空、`ids/filter` 同时存在、目标全被收敛。 |
| 400 | `INVALID_ASSET` | 资产文件名非法、不是 jar、包含路径分隔符、指定资产不是 `type=plugin`。 |
| 404 | `NOT_FOUND` | 指定资产不存在。 |
| 409 | `BUSINESS_ERROR` | 资产物理文件不可读或不可用。 |
| 500 | `INTERNAL_ERROR` | Worker 写入编排之外的未预期错误。 |

示例：

```json
{
  "error": "INVALID_ASSET",
  "message": "插件资产文件名非法"
}
```

## Worker gRPC

首期不新增 Worker RPC。Control Plane 对每个目标实例调用既有：

| RPC | 用途 |
|---|---|
| `WriteFile` | 写入 `plugins/<asset.filename>` 到实例工作目录。 |

约束：

- `Path` 由 Control Plane 拼出固定前缀 `plugins/` + 安全文件名。
- Worker 仍执行实例工作目录内路径校验。
- Control Plane 不直接读写实例工作目录。

## 前端契约

- 入口：运行时资产页插件资产操作区。
- 交互：选择插件 → 搜索并选择目标实例 → 二次确认 → 调用 `POST /plugins/batch-deploy` → 展示汇总。
- 成功 toast：显示成功/失败/skipped 汇总。
- 部分失败：保留对话框结果区，展示逐实例 `error` 明细。
- 错误注入：mock 支持权限拒绝、资产类型错误、空目标等基础错误路径，用于 DOM 测试扩展。

## 验收接口契约

- `assetIds` 必须只接受 `type=plugin` 资产。
- `ids` 模式必须把越权或不存在目标计入 `skipped`，不暴露目标是否存在。
- `filter` 模式必须在权限范围内查询目标。
- `POST /plugins/batch-deploy` 的审计动作必须是 `plugin.batchDeploy`。
- 响应计数必须满足：`total = success + failed + skipped`。
