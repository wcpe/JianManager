# ADR-029: 业务高危写操作的权限与二次确认模型

- **日期**: 2026-06-24（FR-121 落地补完 2026-06-25）
- **状态**: accepted
- **上下文**: JBIS 让平台能远程改余额、改背包——这些是高危写：bug 或误操作会刷钱/吞物品/刷物品。需要在便捷与安全间立一道闸：谁能写、写之前要不要确认、出事能不能追责。既有权限体系是 permission node + 审计日志（FR-009/FR-053）。

## 决策

**高危业务写 = 显式权限 + 阈值二次确认 + 操作者身份审计贯通。**

1. **权限**：高危写动作（改余额/改背包）挂独立 permission node，与只读/低危动作分离；按可访问实例资源隔离。
2. **二次确认**：高危写经阈值触发前端二次确认（如金额 > 阈值、任意背包写）。阈值可配，默认从严。
3. **操作者身份透传 + 审计贯通**：每条业务写携带"哪个 JM 管理员、哪个节点、为什么"，经桥透传为插件写入的 `operator`/`reason`，**映射进插件自身审计流水**（mce ledger.operator / 背包 AuditService），平台侧与插件侧审计可对账追责。
4. **降级即默认**：权限不足/确认未过/域不可用，一律明确拒绝，绝不静默写、绝不 5xx。

## 理由
- 高危写的代价（刷钱/吞物品）远高于读，必须比读更重的闸。
- 阈值确认挡误操作；权限节点挡越权；审计贯通保事后追责。
- 操作者身份打通 JM↔插件两侧审计，避免"插件审计只见适配器、查不到真人"。

## 后果
- 新增高危写 permission node + 确认阈值配置（FR-121）。
- 适配器写入必须接受并透传 operator/reason（经济 FR-120 / 背包 FR-125）。
- 端到端幂等（ADR-027 dedupKey/jm_task_id）与本 ADR 协同：确认通过的写带稳定幂等键，重试不双花。

## 关系
- **FR-009/FR-053（权限/审计）**：本 ADR 复用其 permission node + 审计底座，加业务高危维度。
- **ADR-027**：操作者身份与幂等键经桥随命令透传。
- **设计总纲** §5 原则 5 / 建议 ADR-029。

## 落地细则（FR-121）

> 以下为 FR-121 实施时定下的具体契约，供下游业务域（经济/背包）一致遵循。

### 幂等键经 payload `taskId` 承载（不复用 gRPC `request_id`）

`SendPluginCommand` 的 `request_id` 在 CP 每次调用 `uuid.NewString()` 新生成——重试 = 新 `request_id`，**不可作幂等键**。故幂等键独立走 **payload JSON 的 `taskId` 字段**：CP 据请求体 `operationId`（前端生成、对同一逻辑操作的重试稳定）注入 `payload.taskId`，经桥透传直达探针 Provider 作 mce `BusinessOrder(taskId)`（FR-120 缺 `taskId` 即拒绝）。前端未带 `operationId` 时 CP 兜底生成 UUID（保证探针不因缺键拒绝，但失去跨重试去重）——故前端写动作**必须**带稳定 `operationId`。

### `write` 标志的双层防线取舍

CP 插件无关、不硬编码「哪些 action 是写」。判定来源是 manifest 的 `readOnly`，但每次写都额外往返探针取 manifest 会增延迟且探针可能临时不可用。取舍：**请求体显式 `write: bool`（前端依 manifest `readOnly` 取反声明）**决定 JM 侧权限/确认/审计强度；安全底线由探针兜底——即便前端误标 `write=false` 想绕 JM 闸，探针 Provider 仍因 payload 缺 `taskId` 拒绝该写。两层防线（JM 侧权限确认 + 探针侧幂等键硬约束）共同成立。

### permission node 与注入字段命名（下游须一致）

- **权限节点**：`instance:business:write`（与 `instance:operate` 同级，组成员/管理员可持有，资源由 `CanAccessInstance` 收敛）。写动作要求此节点；读/manifest 维持 `instance:operate`/`instance:read`。
- **注入 payload 的操作者上下文键**（仅当未被业务方显式入参覆盖时写入）：
  - `taskId`：幂等键。
  - `operator`：操作者用户名（透传进 mce 流水 operator）。
  - `operatorId`：操作者用户 ID（平台侧与插件侧审计对账）。
  - `nodeId`：实例所属节点 UUID。
  - `reason`：操作原因（可选）。
- **JM 侧审计**：`AuditService` 记 `business.write`（handler，detail 含 domain/action/operationId/reason/available）；审计中间件兜底记 `business.dispatch`（覆盖读+写）。复用既有 `audit_logs`，不新建审计表（经济镜像/审计表属 FR-122）。
