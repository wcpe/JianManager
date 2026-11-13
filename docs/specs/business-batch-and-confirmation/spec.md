# FR-523 / FR-535 业务批量命令与跨区分级确认

> 状态：**已实现（状态行原为「草拟」，实现早已落地，2026-10 对账修正）**：business_batch.go + business_confirm.go（逐调用确认） · 优先级：P0 · 依赖：FR-522（事件透传）
> 关联：FR-524（写穿）、FR-537（流式进度）、ServerProbe FR-33/34（manifest 与分发）

## 1. 背景与目标

业务台需要一次性对多台服务器执行同一业务动作（如「全区开启活动」）。现有 `InstanceBatchService` 只支持 `instanceIds / nodeId / status / role`，**没有分组维度**，也不区分「同小区 5 台」和「跨两个大区 95 台」的风险差异。

### 目标

- 支持按分组树（集群 → 大区 → 小区）多选目标
- 高风险操作强制二次确认，且确认绑定解析快照
- 100+ 实例下可执行、可观测、可中断

### 非目标

- 不做业务层面的编排（活动之间的依赖顺序由业务台决定）
- 不做跨 system 的事务（每个实例独立成败）

## 2. 目标解析模型

### 2.1 请求结构

```go
type BusinessTarget struct {
    // 以下条件为 AND 语义，从粗到细收敛
    GroupIDs           []uint  `json:"groupIds,omitempty"`
    IncludeDescendants bool    `json:"includeDescendants"`
    InstanceIDs        []uint  `json:"instanceIds,omitempty"`
    Filters            *BatchFilter `json:"filters,omitempty"`
}

type BatchFilter struct {
    NodeID *uint   `json:"nodeId,omitempty"`
    Status *string `json:"status,omitempty"`
    Role   *string `json:"role,omitempty"`
}
```

**解析顺序**：

1. `groupIds` + `includeDescendants` → 展开为实例集合（递归子树）
2. 与 `instanceIds` 取交集（都给了的话）
3. 应用 `filters` 过滤
4. 结果按实例 ID 排序去重

### 2.2 分组树来源

分组树是 JM 自有真源（`InstanceGroupNode` 自引用邻接表），**不从 Beacon 依赖**。Beacon 同步是可选增强（见 Beacon 同步 FR）。

## 3. 两阶段流程

### 3.1 阶段一：预览

```http
POST /api/v1/business/batch/preview
{
  "target": { "groupIds": [3, 7], "includeDescendants": true }
}
```

响应：

```json
{
  "resolved": {
    "instanceCount": 87,
    "online": 84,
    "offline": 3,
    "instanceIds": [1, 2, 5, "..."]
  },
  "groups": [
    { "id": 3, "name": "region1", "path": "cluster-main/region1", "instanceCount": 45 },
    { "id": 7, "name": "region2", "path": "cluster-main/region2", "instanceCount": 42 }
  ],
  "risk": {
    "level": "high",
    "reasons": ["跨 2 个大区", "影响 87 个实例", "含 3 个离线实例"]
  },
  "confirmToken": "bpt_7f3a...",
  "expiresAt": "2026-10-23T12:05:00Z"
}
```

**`instanceIds` 是否返回**：返回（业务台需要展示明细），但对超大集合（> 500）只返回计数 + 前 100 个 ID，避免响应过大。

### 3.2 阶段二：执行

```http
POST /api/v1/business/batch
{
  "confirmToken": "bpt_7f3a...",
  "domain": "activity",
  "action": "stop",
  "payload": { "activityId": "spring-2026" },
  "requestId": "uuid-v4"
}
```

## 4. confirmToken 设计

### 4.1 绑定内容

token 是服务端签名的 opaque 字符串，内部绑定：

| 绑定项 | 用途 |
|---|---|
| 解析后的实例 ID 集合（排序后 hash） | 防止目标在执行前变化 |
| 风险等级 | 防止把 high 降级绕过 |
| 签发时间 + 有效期（默认 5 分钟） | 防重放 |
| 签发者 token ID | 防止跨账号使用 |

### 4.2 校验规则

执行时必须**重新解析目标**并比对 hash：

| 情况 | 行为 |
|---|---|
| hash 一致 | 执行 |
| hash 不一致（分组有增删） | `409 TARGET_CHANGED`，要求重新预览 |
| token 过期 | `409 TOKEN_EXPIRED` |
| token 被跨账号使用 | `403 TOKEN_MISMATCH` |
| risk 为 high/critical 但无 token | `409 CONFIRMATION_REQUIRED` |

**为什么不用「用户点过一次确认」这种布尔标记**：那是会话状态，容易被绕过；绑定快照 hash 才能保证「确认时看到的就是将要执行的」。

## 5. 风险分级规则

| 条件 | 级别 | 行为 |
|---|---|---|
| 单实例 | `low` | 无需确认 |
| 同小区多实例（≤ 10） | `low` | 无需确认 |
| 同小区多实例（> 10） | `medium` | 前端提示 |
| 跨小区、同大区 | `medium` | 前端提示 |
| **跨大区** | `high` | **强制 confirmToken** |
| **跨集群** | `high` | **强制 confirmToken** |
| **实例数 > 50** | `high` | **强制 confirmToken** |
| 含离线实例 | `medium` | 提示，可继续 |
| 动作 `risk=DESTRUCTIVE` | `critical` | **强制 token + 必填 reason** |

**多条件命中取最高级别。**

`risk` 来自 ServerProbe 的 manifest（ServerProbe FR-33 的 `ActionSpec.risk`）。**JM 不推断风险**——如果插件没声明，默认按 `WRITE` 处理（需要确认但非 critical）。

## 6. 执行语义

### 6.1 并发控制

| 参数 | 默认 | 说明 |
|---|---|---|
| 单批并发 | 20 | 同时向多少个实例下发 |
| 单实例超时 | 30s | 可配，独立于业务命令 5s |
| 整体超时 | 无硬上限，但客户端可中断 | |

### 6.2 结果语义

| 结果 | 含义 |
|---|---|
| `success` | 探针回执成功 |
| `failed` | 探针回执失败（含错误码） |
| `offline` | 目标实例离线/探针未连（未下发） |
| `timeout` | 下发后超时未回执 |
| `skipped` | 前置校验失败或目标已不可达 |

**部分失败不整体失败**：HTTP 状态码仍为 200，由 `results` 表达逐实例结果。

### 6.3 幂等

- `requestId` 必填
- 同 `requestId` 重复提交 → 返回首次结果，不重复执行
- 幂等窗口：24 小时（内存 + 可选持久化）

## 7. 接口定义

### 7.1 预览

```http
POST /api/v1/business/batch/preview
```

无副作用，可重复调用。限流：每 token 每分钟 30 次。

### 7.2 执行（同步）

```http
POST /api/v1/business/batch
Accept: application/json
```

返回完整结果集（实例数 > 200 时建议改用流式，见 FR-537）。

### 7.3 执行（流式）

```http
POST /api/v1/business/batch
Accept: text/event-stream
```

见 FR-537。

## 8. 错误码

| 码 | HTTP | 场景 |
|---|---|---|
| `TARGET_EMPTY` | 400 | 解析后无实例 |
| `TARGET_CHANGED` | 409 | 目标集合与 token 绑定不一致 |
| `TOKEN_EXPIRED` | 409 | confirmToken 过期 |
| `TOKEN_MISMATCH` | 403 | token 不属于当前调用者 |
| `CONFIRMATION_REQUIRED` | 409 | 高风险但未提供 token |
| `REASON_REQUIRED` | 400 | critical 动作未填 reason |
| `INSTANCE_OFFLINE` | — | 逐实例结果，非整体错误 |
| `IDEMPOTENT_REPLAY` | 200 | 重放，返回首次结果 |

## 9. 可观测

| 指标 | 说明 |
|---|---|
| `business.batch.preview` | 预览次数 |
| `business.batch.execute` | 执行次数（按 risk 维度，不按 domain） |
| `business.batch.targets` | 目标数分布直方图 |
| `business.batch.duration` | 执行耗时分布 |
| `business.batch.token_rejected` | token 校验失败次数 |

## 10. 验收标准

### 目标解析

1. 单选 / 多选分组均可正确解析
2. `includeDescendants=false` 时不含子分组实例
3. 分组 + 实例 ID 组合时取交集
4. 空结果返回 `TARGET_EMPTY` 而非执行

### 风险与确认

5. 跨大区操作无 token 时返回 `CONFIRMATION_REQUIRED`
6. 预览后分组新增实例 → 执行返回 `TARGET_CHANGED`
7. token 过期返回 `TOKEN_EXPIRED`
8. 他人 token 返回 `TOKEN_MISMATCH`
9. `DESTRUCTIVE` 动作未填 reason 返回 `REASON_REQUIRED`

### 执行

10. 100 实例批量，5 台离线 → 95 成功 + 5 `offline`，HTTP 200
11. 同 `requestId` 重复提交不重复执行
12. 单实例超时不阻塞其他实例

### 规模

13. 100+ 实例预览响应 < 1s
14. 并发 20 时 CP 无 goroutine 泄漏

### 真机

15. 真实分组树 + 真实探针，跨区批量端到端通过
16. 真实探针离线场景，逐实例结果正确

## 11. 待定

1. **实例数阈值 50** 是否符合你们实际运营？需要按你们「多少台算危险」调整
2. 幂等窗口 24 小时是否够（业务台重试可能跨天）
3. 是否需要对「同一活动重复开启」做业务级防重（当前只有 `requestId` 技术幂等）
4. 预览的 `instanceIds` 返回上限 500 / 前 100 是否合适

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-523 业务批量命令接口（feat，需 spec，spec

**需求补充**：`docs/specs/business-batch-and-confirmation/spec.md`，依赖 FR-522）：新增 `POST /business/batch`，一次请求跨多实例执行同一 `domain.action`，支持 `instanceIds` 或 `groupId` 目标；逐实例返回结果，部分失败不整体失败；`requestId` 幂等（重放返回首次结果）；按批并发受控（默认每批 20）以适配 100+ 实例。验收：12 实例中 1 台离线返回 11 成功 + 1 `INSTANCE_OFFLINE` 且 HTTP 非 5xx；同 `requestId` 重复提交只产生一次副作用；100 实例批量不阻塞其他请求。

**交付状态**：✅ 已交付@v0.24.0

### FR-535 批量目标解析与跨区二次确认（feat，需 spec，spec

**需求补充**：`docs/specs/business-batch-and-confirmation/spec.md`，依赖 FR-523）：把批量命令的目标选择从「instanceIds / nodeId / status / role」扩展到**分组粒度多选**——支持 `groupIds[]` 多选 + `includeDescendants`（含子分组），按分组树（集群→大区→小区）解析出实例集。新增两阶段流程：①`POST /business/batch/preview` 返回解析结果（实例数、在线/离线数、涉及分组及其路径）+ **风险分级** + `confirmToken`（绑定本次解析快照，5 分钟有效）；②`POST /business/batch` 携带 token 执行，token 与当前解析不一致则拒绝。**风险分级规则**：单实例/同小区=low；跨小区同大区=medium；**跨大区或跨集群=high（强制确认）**；实例数 > 阈值（默认 50）=high；含离线实例=medium（提示）；动作 `risk=DESTRUCTIVE`=critical（**强制填写原因**）。验收：跨大区必须两步确认且不可用旧 token 绕过；解析结果与实际执行集一致（分组变动后旧 token 失效）；100+ 实例预览响应 < 1s。

**交付状态**：✅ 已交付@v0.24.0

### FR-523

业务批量命令执行引擎

✅ 代码完成（2026-10-24）：`service/business_batch.go`（执行引擎，并发默认 20、单实例超时 30s 独立于单实例 5s、五种逐实例状态 success/failed/offline/timeout/skipped、部分失败仍 200、requestId 幂等窗口 24h 内存态）；`router/business_batch.go`（`POST /business/batch`，按 `Accept` 分流 JSON/SSE）。执行经 `BusinessService.DispatchWrite` 走插件通道，payload 注入 taskId(=requestId)/operator/operatorId/reason。**FR-524 的 `IDEMPOTENCY_KEY_REUSED` 已顺带实现**（同 requestId + 参数指纹不同即 409）。


### FR-535

批量命令粒度与预览确认

✅ 代码完成（2026-10-24）：`service/business_target.go`（`BusinessTarget` 解析：groupIds 递归展开 → instanceIds 交集 → filters → 去重排序；自引用邻接表带深度上限 64 + 访问集双保护防环；`BusinessTargetScope` 做跨组隔离且存在性隐藏）；`service/business_risk.go`（风险分级；「跨大区」按**分组树深度**判定：1=集群/2=大区/3=小区，祖先链不足时降级取最深可用祖先，层级集中在 `BusinessScopeLevels` 可整体调整）；`service/business_confirm.go`（HMAC-SHA256 签名令牌，绑定实例集指纹 + 风险等级 + 签发者 + 5min 有效期，域分离派生密钥）。令牌正文还携带目标选择条件——契约的 `BatchExecuteRequest` 不带 target，执行端必须能还原选择才能重新解析比对 hash。


### FR-537

批量命令流式进度

✅ 代码完成（2026-10-24）：同一 `POST /business/batch` 按 `Accept: text/event-stream` 分流；`event: progress` 每 20 个推一次、`event: done` 含完整 results；进度帧非阻塞投递（客户端跟不上就丢帧，**绝不反压执行**）；**token/目标/原因等前置校验全部在流开始前完成**，失败返回 JSON 错误（含正确状态码）而不写任何 SSE 帧；客户端断开 ≠ 取消执行（执行用与请求解耦的 context）。

