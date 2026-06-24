# FR-121: 业务写横切硬化（幂等 + 二次确认 + 审计）

> 状态：🚧 开发中 ｜ 关联 ADR-029 ｜ 依赖 FR-116（CP 编排脊柱）、FR-120（探针经济写 Provider）

## 1. 目标与范围

在 FR-116 的业务下发脊柱（`POST /instances/:id/business`）之上，为**写动作**（manifest `readOnly=false`）叠加三道横切硬化：

1. **端到端幂等**：CP 为每次写动作注入稳定 `taskId` 进 payload → 经探针桥透传 → 直达探针 Provider 的业务幂等键（mce `BusinessOrder(taskId)`，见 FR-120）。同一逻辑操作的重试复用同一 `taskId`，跨节点重试天然防重不双花。
2. **高危写权限 + 二次确认**：写动作需独立 permission node `instance:business:write`；前端写动作走 `DangerConfirm`（FR-059）二次确认才下发。
3. **操作者身份审计贯通**：每条写命令携带操作者上下文（`operator`/`operatorId`/`nodeId`/`reason`）进 payload，供探针 Provider 透传进 mce 流水（`operator`/`reason`）；同时在 JM **既有审计设施**（`AuditService` + `/audit`）记一条 `business.write` 审计。

### 非目标（明确不做）

- **不新建审计表 / 经济镜像表**——复用既有 `audit_logs`；经济事件汇聚/聚合/镜像归 FR-122，本 FR 不侵入。
- **不改 proto**——`PluginCommand.payload_json` 已是任意 JSON 信封，`taskId`/`operator`/`reason` 全部走 payload，无需新增 gRPC 字段。
- **不判定动作语义**——CP 仍插件无关；「哪些 action 是写」由 manifest 的 `readOnly` 标志决定，CP 不硬编码具体业务动作名。
- **不阻塞读路径**——只读动作（`readOnly=true`，含 `jbis.manifest` 元查询）零额外门禁与确认，保持原行为。

## 2. 幂等键设计

### 2.1 为什么不能用 gRPC `request_id`

`SendPluginCommand` 的 `request_id` 在 `BusinessService.Dispatch` 内每次调用 `uuid.NewString()` 新生成，重试 = 新 `request_id`，**不可作幂等键**。FR-120 探针 Provider 明确从 **payload 读 `taskId`** 作 mce `BusinessOrder` 幂等键、缺失即拒绝。故幂等键必须是 payload 内独立、对「同一逻辑操作的重试」稳定的字段。

### 2.2 taskId 生成与稳定性契约

- **前端发起写时生成 `operationId`（UUID v4）** 随请求体带上，作为该逻辑操作的稳定标识。用户对同一动作重试（网络抖动/超时重发）时复用同一 `operationId`，CP 据此得稳定 `taskId`。
- **CP 注入 payload**：CP 解析请求体的 `operationId`，写动作时把 `taskId` 注入 payload JSON（`taskId = operationId`）。前端未提供 `operationId` 时 CP 兜底 `uuid.NewString()`（保证探针不因缺 `taskId` 拒绝），但此时无重试去重保证——故前端写动作**必须**带 `operationId`。
- **只读动作不注入**：`readOnly=true` 的动作 CP 不注入 `taskId`（读无副作用、无需幂等）。

### 2.3 注入规则（CP 侧）

CP 在 `Dispatch` 前需知道目标 `domain.action` 是否为写。判定来源：先取该实例 manifest（`readOnly` 标志）。为避免每次写都额外往返探针取 manifest（增加延迟、且探针可能临时不可用），采用**调用方显式声明 + manifest 校验**的折中：

- 请求体新增 `write: bool`（前端依据 manifest 的 `readOnly` 设置：写动作传 `write=true`）。
- CP 对 `write=true` 的请求：要求 `instance:business:write` 权限、注入 `taskId`/`operator`/`operatorId`/`nodeId`/`reason`、记审计。
- CP 对 `write=false`（或缺省）：维持 FR-116 既有行为（`instance:operate` + 不注入 + 不记业务写审计）。

> 说明：`write` 由前端依据 manifest 声明，是**性能与简洁**取舍。即便前端误标 `write=false` 想绕过确认，探针 Provider 仍因 payload 缺 `taskId` 而拒绝该写（FR-120 硬约束）——即安全底线由探针兜底，CP 的 `write` 标志只决定 JM 侧的权限/确认/审计强度。这一取舍记入 ADR-029。

## 3. 权限模型

新增 permission node：

| 节点 | 含义 | 归属 |
|---|---|---|
| `instance:business:write` | 业务高危写（改余额/改背包等 `readOnly=false` 动作） | 与 `instance:operate` 同级（组管理员/组成员均可持有，资源由 `CanAccessInstance` 收敛） |

- 写动作（`write=true`）要求 `instance:business:write` **且** 实例可访问（`CanAccessInstance`）。
- 读动作 / manifest 维持 `instance:operate` / `instance:read`。
- `HasPermission` 中把 `PermInstanceBusinessWrite` 归入「实例操作类」分支（组内成员可持有，越权由资源隔离兜底），与 `PermInstanceOperate` 同策略。

## 4. 操作者身份透传 + 审计

### 4.1 注入 payload 的操作者上下文（写动作）

CP 在写动作 payload 注入以下字段（与业务参数合并，键名为 JBIS 约定）：

| 字段 | 来源 | 用途 |
|---|---|---|
| `taskId` | `operationId`（前端）/ 兜底 UUID | 幂等键（探针→mce BusinessOrder） |
| `operator` | 当前用户 `username` | 透传进 mce 流水 operator（追责到真人） |
| `operatorId` | 当前用户 ID | 平台侧与插件侧审计对账 |
| `nodeId` | 实例所属节点 UUID | 「哪个节点」维度 |
| `reason` | 请求体 `reason`（可选） | 「为什么」，透传进 mce 流水 reason |

注入策略：解析既有 payload JSON 为 `map[string]any`，**仅当对应键不存在时**写入（不覆盖业务方显式传入的同名键），再序列化回字符串下发。payload 为空时以 `{}` 起步。

### 4.2 JM 侧审计（既有 AuditService）

- `BusinessHandler` 持有 `*service.AuditService`（构造注入，仿 `PlayerHandler`）。
- 写动作成功下发后记一条：`action="business.write"`、`targetType="instance"`、`targetID=实例ID`、`detail={domain,action,taskId,reason}` JSON、`ip=ClientIP`。
- 审计记录失败不阻断主流程（与 `PlayerHandler.recordAudit` 一致，`_ =` 忽略错误）。
- 中间件 `determineAction`（middleware/audit.go）补 `POST .../business` → `business.dispatch` 映射，使既有审计中间件对业务下发亦有兜底留痕（与 handler 内 `business.write` 互补：中间件覆盖所有下发含读，handler 专记写并带结构化 detail）。

## 5. API 变更

### POST /api/v1/instances/:id/business（增量，向后兼容）

请求体新增两个可选字段：

```jsonc
{
  "domain": "economy",
  "action": "deposit",
  "payload": "{\"player\":\"alice\",\"currency\":\"coin\",\"amount\":\"100\"}",
  "write": true,             // 新增：是否为写动作（前端据 manifest readOnly 取反）
  "operationId": "uuid-v4",  // 新增：写动作幂等标识（稳定于重试）
  "reason": "活动补偿"        // 新增：可选，操作原因（透传进插件流水 + JM 审计）
}
```

- 权限：`write=true` → `instance:business:write`；否则 `instance:operate`（不变）。
- 行为：`write=true` 时 CP 注入 `taskId`/`operator`/`operatorId`/`nodeId`/`reason` 进 payload、记 `business.write` 审计。
- 响应体不变（`BusinessResult`）。
- 新错误：`403 FORBIDDEN`（写动作但无 `instance:business:write`）。
- 向后兼容：旧客户端不带 `write`/`operationId` → 按读路径处理（与改造前一致）。

## 6. 前端变更

`web/src/components/console/BusinessSegment.tsx`：

- 写动作（`action.readOnly !== true`）点击「下发」时弹 `DangerConfirm`（`scope='group'`，`confirmLabel=business.confirmWrite`），确认后才真正 `dispatchBusiness`。
- 写动作面板增「原因」（`reason`）可选输入框。
- 发起写时生成 `operationId = crypto.randomUUID()`，随请求带上；`write` 依据 `readOnly` 取反。
- 读动作维持「点击即下发」无确认。
- i18n：`business.confirmWriteTitle`/`confirmWriteDesc`/`confirmWrite`/`reason`/`reasonPlaceholder`（zh/en）。颜色走主题 CSS 变量，暗/亮自适应（`DangerConfirm` 已满足）。

`web/src/api/business.ts`：

- `dispatchBusiness` 增可选参 `opts?: { write?: boolean; operationId?: string; reason?: string }`，附加进请求体。

## 7. 测试

### 后端（Go 单测）

- `BusinessService.Dispatch` 写动作注入：给定 `write=true` + `operationId`，断言下发 payload 含 `taskId==operationId`、`operator`/`operatorId`/`nodeId`/`reason`；不覆盖业务方已传同名键。
- 缺 `operationId` 兜底：`write=true` 无 `operationId` → payload 仍有非空 `taskId`。
- 读动作不注入：`write=false` → payload 不含 `taskId`/`operator`。
- payload 为空 / 非法 JSON 的注入鲁棒性（空→`{}`；非法→报错或安全包裹）。
- `authz`：`instance:business:write` 在组成员/管理员可持有、平台管理员放行、跨组隔离。
- handler：写动作无 `instance:business:write` → 403；有则放行并记审计（用内存 sqlite + AuditService 断言审计落库）。

### 前端（vitest）

- `business.ts`：`dispatchBusiness` 带 `opts` 时请求体含 `write`/`operationId`/`reason`。
- 纯函数（如有抽出的 `isWriteAction`）单测。

## 8. ADR-029 补完

把以下决策正文补进 `docs/adr/029-business-high-risk-write-confirmation.md`：

- 幂等键经 **payload `taskId`**（非 gRPC `request_id`）承载的理由（request_id 每调用刷新不可作幂等键）。
- `write` 标志由前端依 manifest `readOnly` 声明 + 探针 `taskId` 缺失兜底拒绝的双层防线取舍。
- permission node 定名 `instance:business:write`。
- 操作者字段命名：`operator`/`operatorId`/`nodeId`/`reason`/`taskId`。

## 9. 对下游 FR 的接口约定（影响并行 FR-122/123/125/126）

| 约定 | 值 | 影响 |
|---|---|---|
| 幂等键字段名（payload） | `taskId` | FR-120 已用；FR-125 背包写须同样从 payload 读 `taskId` |
| 操作者字段名（payload） | `operator`（名）/`operatorId`（ID）/`nodeId`（节点 UUID）/`reason` | FR-122/126 审计对账、FR-125 背包写透传须同名 |
| 写权限 node | `instance:business:write` | FR-125 背包写复用同一节点 |
| JM 审计 action | `business.write`（handler）/`business.dispatch`（中间件兜底） | FR-122/126 汇聚/审计页可据此过滤 |
| 请求体写标志 | `write: bool` + `operationId: string` + `reason?: string` | FR-123/127 定制页发起写须按此结构 |
