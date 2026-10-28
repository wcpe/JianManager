# FR-521 退役 JM 业务能力

> 状态：草拟 · 优先级：P0 · 无依赖（其它业务通道 FR 的前置）

## 1. 背景

JM 第五期 JBIS（FR-115~127）把经济/背包的业务语义、镜像表、定制页收进了平台：

- `BusinessEvent` / `EconomyBalanceMirror` / `EconomyLedgerEntry` 三张业务表
- `service/business_events.go` 业务事件落库与聚合
- `router/business_event.go` 四个业务读接口
- `docs/specs/business-integration/` 全套规格

这与「JM 不做任何业务」的分层定位冲突。本轮整体退役。

## 2. 目标

- 删除全部业务语义与业务数据表
- 保留并强化「纯透传通道」
- 消除"两套业务能力"的歧义

## 3. 非目标

- 不删除 `POST /instances/:id/business` 与 `manifest`（这是通道，不是业务）
- 不删除桥、幂等、审计等横切能力
- 不删除 ServerProbe 的 Provider 机制（这是数据面）

## 4. 退役清单

### 4.1 代码

| 文件 | 处置 | 说明 |
|---|---|---|
| `internal/controlplane/model/business_event.go` | **整文件删除** | 三张业务表定义 |
| `internal/controlplane/service/business_events.go` | **整文件删除** | 事件落库、聚合、镜像维护 |
| `internal/controlplane/service/business_events_test.go` | 删除 | |
| `internal/controlplane/router/business_event.go` | **整文件删除** | 四个业务读接口 |
| `internal/controlplane/router/business_event_handler_test.go` | 删除 | |
| `internal/controlplane/router/business.go` | **保留** | 只留 `Dispatch` / `Manifest` |
| `internal/controlplane/service/business.go` | **保留** | 清理与具体域相关的注入键 |

### 4.2 要删除的接口

| 接口 | 现状 |
|---|---|
| `GET /api/v1/business/events` | 通用业务事件列表 |
| `GET /api/v1/business/economy/mirror` | 经济余额镜像 |
| `GET /api/v1/business/economy/aggregate` | 经济聚合 |
| `GET /api/v1/business/economy/leaderboard` | 经济排行 |

全部删除，前端无调用（已确认前端无业务页）。

### 4.3 数据库

按用户决策：**直接删表，不备份**。

```sql
DROP TABLE IF EXISTS business_events;
DROP TABLE IF EXISTS economy_balance_mirrors;
DROP TABLE IF EXISTS economy_ledger_entries;
```

迁移文件注释写明：

```
-- V1 业务镜像已退役（FR-521）。
-- 业务数据由下游业务台持有，JM 不再存储任何业务语义数据。
-- 决策依据：ADR-106。
```

### 4.4 文档

| 文档 | 处置 |
|---|---|
| `docs/specs/business-integration/` | 移到 `docs/specs/archive/business-integration/`，每份加退役头 |
| `docs/adr/025~029` | **保留原文**（ADR 只追加不删），各追加一行「已被 ADR-106 取代」 |
| `docs/adr/106-business-capability-retirement.md` | 新建，记录退役决策 |
| PRD FR-115~127 | 已标 `⛔ 已退役` |
| `docs/API.md` | 移除四个业务读接口 |

### 4.5 MCP 工具

检查 `agentActionCatalog` 与 MCP 工具注册表：

- 如存在 `business.events` / `economy.*` 类工具 → 移除
- `business.dispatch` / `business.manifest` 保留

## 5. 保留能力清单

| 保留 | 理由 |
|---|---|
| `POST /api/v1/instances/:id/business` | 通用 `domain.verb` 转发，不认识任何 domain |
| `GET /api/v1/instances/:id/business/manifest` | 通用能力发现，原样透传 |
| `bridge/plugin_bridge.go` | 传输层 |
| 幂等（`requestId`）、二次确认、审计 | 横切能力，与业务无关 |

## 6. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 有用户依赖经济聚合接口 | 已确认前端无调用；如外部有调用方，提前通知 |
| 数据删了无法恢复 | 用户明确要求；迁移前可手工 `mysqldump` 一次（非代码行为） |
| 删表后旧代码残留引用 | 全仓 grep 校验 + 编译通过 |

## 7. 验收

### 功能

1. 三张表在迁移后不存在
2. 四个读接口返回 404
3. `POST /instances/:id/business` 行为不变（可正常下发与回执）
4. `GET .../business/manifest` 行为不变

### 清洁度

5. 全仓 grep `economy` / `inventory` / `business_events` 无业务语义残留
6. 编译通过、现有测试全绿
7. `docs/API.md` 无四个废弃接口

### 文档

8. `business-integration/` 已归档
9. ADR-106 已创建
10. ADR-025~029 各有取代标注

## 8. 待定

1. 是否需要保留一条「业务数据已迁至业务台」的提示接口（当前：不需要）
2. 删表前是否需要一次自动归档（用户已明确：不备份）

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-521 退役 JM 业务能力（ref，需 spec，spec

**需求补充**：`docs/specs/business-capability-retirement/spec.md`，退役第五期 JBIS 业务域）：删除 `BusinessEvent` / `EconomyBalanceMirror` / `EconomyLedgerEntry` 三表、`service/business_events.go`、`router/business_event.go`（4 个读接口），存量数据**直接删表不备份**；保留 `POST /instances/:id/business` 与 `GET .../business/manifest` 作为纯透传通道。验收：业务语义零残留（全仓无 economy/inventory 域引用），`business_events` 等三表在迁移后不存在，注销的读接口返回 404，通道接口行为不变。

**交付状态**：✅ 已交付@v0.24.0
