# FR-524 / FR-525 / FR-526 写穿、对账与桥能力接入

> 状态：**已实现（状态行原为「草拟」，实现早已落地，2026-10 对账修正）**：business_reconcile.go + bridge.go + bridge_status.go · 优先级：P0/P1 · 依赖：FR-521/522/523

---

# FR-524 写穿与离线禁改

## 1. 目标

保证「面板上的数据」与「插件里的数据」不会出现"看似改了实际没改"的状态。

## 2. 核心原则

> **业务写必须先达插件，插件成功后才认为写入成功。JM 不缓存、不排队、不重试到本地。**

因为 JM 不持有业务数据（FR-521），这条规则**天然成立**——它没有本地副本可以写。

## 3. 前置校验链

下发前依次检查，任一失败即返回，且**不产生任何副作用**：

| 检查 | 失败返回 | HTTP |
|---|---|---|
| 实例存在 | `INSTANCE_NOT_FOUND` | 404 |
| 调用者有该实例权限 | `FORBIDDEN` | 403 |
| 实例处于运行态 | `INSTANCE_OFFLINE` | 409 |
| 节点在线 | `NODE_OFFLINE` | 409 |
| 探针已连接 | `PROBE_UNAVAILABLE` | 409 |
| 域已注册 | `DOMAIN_UNAVAILABLE` | 409 |
| 动作在 manifest 中声明 | `ACTION_UNSUPPORTED` | 422 |

**关键在于顺序**：先检查最便宜的、最能早拒的，避免无谓的跨网络往返。

## 4. 审计

### 4.1 记录内容（通道事实）

```json
{
  "actor_kind": "agent",
  "actor_id": "<token id>",
  "action": "business.dispatch",
  "target_ref": "instance:5",
  "domain": "activity",
  "verb": "start",
  "request_id": "uuid",
  "result": "success",
  "error_code": "",
  "duration_ms": 37,
  "at": "2026-10-23T12:00:00Z"
}
```

### 4.2 绝对不记

| 不记 | 理由 |
|---|---|
| `payload` 内容 | 可能含玩家数据、密钥、PII |
| `output` 内容 | 同上 |
| 业务语义解释 | JM 不理解业务 |

**为什么这条红线必须守**：一旦 JM 开始记录 payload，就会有人问"能不能按 payload 字段搜索"，然后就长出了业务查询能力——回到 FR-521 要解决的问题。

### 4.3 写入方式

- **异步**：审计写库不阻塞转发链路
- **失败不重试**：审计丢了比业务阻塞好（可接受的取舍）
- **失败留日志**：便于事后补查

## 5. 幂等

| 项 | 要求 |
|---|---|
| `requestId` | 必填，UUID 格式 |
| 重复提交 | 返回首次结果，不重复执行 |
| 窗口 | 24 小时（内存 LRU + 可选持久化） |
| 冲突 | 同 `requestId` 但 payload 不同 → `409 IDEMPOTENCY_KEY_REUSED` |

**为什么 payload 冲突要报错**：同一个 requestId 携带不同参数通常是调用方 bug，静默返回首个结果会掩盖问题。

## 6. 验收

1. 实例离线 → `409 INSTANCE_OFFLINE`，库内零变更
2. 探针未连 → `409 PROBE_UNAVAILABLE`
3. 域未注册 → `409 DOMAIN_UNAVAILABLE`
4. 审计可按 `requestId` 精确查到
5. 审计内容**不含** payload/output
6. 同 requestId 重复提交只执行一次
7. 同 requestId 不同 payload → `409`

---

# FR-525 对账协调接口

## 1. 目标

提供「拉取插件全量状态」的通道，供业务台修正镜像偏差。

## 2. 核心原则

> **JM 只做摆渡，不做差异计算。**

因为差异计算需要理解业务语义——那是业务台的职责。

## 3. 接口

```http
POST /api/v1/business/reconcile
{
  "domain": "activity",
  "action": "snapshot",          // 由业务台指定，JM 不预设
  "payload": { "activityId": "spring-2026" },
  "target": { "instanceIds": [1, 2, 3] },
  "requestId": "uuid"
}
```

**注意 `action` 字段**：JM 不假设动作名是 `snapshot`。这是业务台与插件之间的约定，JM 原样转发。

响应：

```json
{
  "requestId": "uuid",
  "results": [
    {
      "instanceId": 1,
      "success": true,
      "output": { /* 插件返回的原始快照，JM 不解析 */ },
      "durationMs": 45
    },
    {
      "instanceId": 2,
      "success": false,
      "error": "INSTANCE_OFFLINE"
    }
  ]
}
```

## 4. 与批量命令的关系

对账本质上是「批量执行一个读动作」。复用 FR-523 的目标解析与并发控制，差异仅在于：

| 维度 | 批量命令 | 对账 |
|---|---|---|
| 语义 | 写操作 | 读操作 |
| 结果处理 | 逐实例成败 | 逐实例快照 |
| 是否流式 | 可选（FR-537） | 建议流式（快照可能很大） |

**实现上可共用同一套执行引擎**，只是结果的呈现方式不同。

## 5. 规模考虑

100+ 实例一次对账，快照可能很大（每个实例几百 KB）：

| 措施 | 说明 |
|---|---|
| 并发限制 | 默认 20，避免打爆 Worker |
| 流式返回 | 建议用 SSE，逐实例返回而非等全部完成 |
| 不缓存 | JM 不在内存里堆积快照，拉完即转 |
| 超时 | 单实例 60s（比命令宽松，因为快照可能慢） |

## 6. 触发方式（由业务台决定）

JM 不主动触发对账，只提供接口。业务台可以：

- 定时（每天/每小时）
- 手动
- 收到 `replay_gap` 时
- 探针 `connected` 事件后

**JM 不感知这些策略**，避免长出业务编排能力。

## 7. 验收

1. 多实例对账，逐实例返回原始快照
2. JM 不解析 `output` 内容
3. 离线实例返回明确错误
4. 100 实例对账，单实例超时不阻塞其他
5. 流式模式下逐实例返回

---

# FR-526 桥能力接入

## 1. 背景

ServerProbe 已交付多项桥能力，JM 尚未消费：

| 能力 | ServerProbe 侧 | JM 侧 |
|---|---|---|
| 主题订阅 | ✅ `core.subscribe` | ❌ 未接 |
| 配置重载 | ✅ `ConfigReloadRoute` | ❌ 未接 |
| 桥状态 | ✅ `BridgeStatusRoute` | ❌ 未接 |
| 域状态 | ✅ `BusinessStatusRoute` | ❌ 未接 |
| 分片 | ✅ `chunk` 包 | ❌ 未接 |

## 2. 逐项接入

### 2.1 主题订阅

```http
PUT /api/v1/instances/:id/bridge/subscription
{ "topics": ["activity.*"], "domains": ["activity"] }
```

JM 转成 `core.subscribe` 下发。用途：减少上行流量，只上报关心的主题。

**注意**：未订阅时探针全量上报（兼容现状）；一旦订阅，只上报匹配主题 + 所有非业务事件。

### 2.2 配置重载

由 FR-532 使用，不经业务通道。JM 侧需要：

- 独立超时配置（默认 10s）
- 独立线程池，不与业务命令争用

### 2.3 桥状态

```http
GET /api/v1/instances/:id/bridge/status
```

返回探针上报的重连次数、队列深度、丢弃计数等。用于排障。

### 2.4 域状态

```http
GET /api/v1/instances/:id/business/status
```

返回已注册域清单与健康状态（ServerProbe FR-35）。用于 FR-534 的插件面板。

### 2.5 分片重组

接收 `chunk` 帧，按 `groupId` 重组：

| 参数 | 默认 |
|---|---|
| 单实例最大并行重组数 | 8 |
| 重组超时 | 30s |
| 不完整组分片保留 | 超时即丢弃并记 `INCOMPLETE_CHUNK` |

## 3. 验收

1. 订阅后只收到匹配主题的事件
2. reload 超时不阻塞业务命令
3. 桥状态可查且数据准确
4. 域状态可查（配合 FR-534）
5. 分片能正确重组；缺帧能检出

---

## 4. 实现后收敛（2026-10-24）

### 4.1 已定

| 项 | 结论 |
|---|---|
| **分片重组层级** | **Worker 层**（传输层分片，被切的既可能是 command_result 也可能是业务事件；放 CP 会让传输细节泄漏到应用层） |
| **分片有界性** | 单实例并行组 8、单组片数 4096、单组解码后 8 MiB、30s 超时整组丢弃记 `INCOMPLETE_CHUNK` |
| **订阅动态变更** | 支持，`replace` 语义（重复 PUT 覆盖不追加） |
| **桥状态采集** | 每次查询实时拉，失败时回退最后已知（不看 TTL）并置 `stale` |
| **reload 超时** | 独立常量 10s，可经 `timeout_ms` 覆盖，Worker 夹到 60s 上限；与业务命令 5s 无耦合 |

### 4.2 实现发现的问题（需契约同步）

**1. `domains` 在探针侧无对应维度**

ServerProbe 的过滤只看 `OutboundFrame.topic`，`core.subscribe` 只收 `topics`。实现由 Worker 把 `domains` 展开成 `<domain>.*` 并入 topics。

**关键约束**：**绝不能用裸 `*` 表达"全部"** —— `TopicMatcher` 只认精确匹配或 `.*` 后缀前缀匹配，裸 `*` 匹配不到任何主题，用它等于静默吞掉全部事件。

**2. `lastDisconnectReason` 有两套字段**

探针回执同时给 `lastDisconnectReason`（原因码）和 `lastDisconnectDetail`（人可读）。OpenAPI 只定义了一个且描述为"可读原因"。当前取的是**原因码**，`Detail` 未暴露。

→ **FR-528 需明确**：契约要原因码还是可读说明，或两者都要。

**3. `queueDepth` / `droppedFrames` 是 JM 侧求和**

探针给的是 sender 三池各自计数，没有"总队列深度"。JM 做了求和。这属于**通道层聚合**，不涉及业务语义，但严格说与"只渲染不解释"有轻微张力。

**4. `BridgeStatus` 需要 error 字段**

探针未连时若严格按 schema 返回，前端无法区分"连接正常但全为 0"和"根本没连上"。实现加了加性 `error` 字段。

→ **FR-528 需把 `error` 同步回 OpenAPI**。

### 4.3 仍待定

1. 分片重组的单组内存上限（当前 8 MiB）是否需要按实例数动态调整

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-524 写穿与离线禁改（feat，需 spec，spec

**需求补充**：`docs/specs/business-write-reconcile-bridge/spec.md`，依赖 FR-523）：业务写一律先达插件，探针未连或实例未运行即返回 `409 INSTANCE_OFFLINE` / `PROBE_UNAVAILABLE`，**不产生任何本地副作用**（因 JM 不持业务数据，此规则天然成立）；所有写操作入审计（`actor_kind=agent`，含 domain/action/requestId/结果），失败亦留痕。**审计只记通道事实**（actor / target / domain / verb / requestId / result / durationMs / 错误码），**payload 与 output 一律不入审计**（可能含玩家数据或密钥，且一旦解析即退回业务实现）；审计异步写入，不阻塞转发链路。验收：离线实例写入被拒且库内零变更；审计可按 requestId 精确追溯且不含 payload/output；复写同 requestId 不重复执行。

**交付状态**：✅ 已交付@v0.24.0

### FR-525 对账协调接口（feat，需 spec，spec

**需求补充**：`docs/specs/business-write-reconcile-bridge/spec.md`，依赖 FR-522）：新增 `POST /business/reconcile`，按 `domain` + 目标集向各实例拉取插件全量快照并**原样透传**（含 `snapshotVersion` / `seq`）；**JM 不做差异计算**（不理解业务语义），差异由业务台自行比对后经写接口回写。支持 `full` / `diff` 两种模式标记。验收：逐实例返回快照与位点；快照缺失或超时返回明确错误；JM 侧不缓存、不落库快照内容。

**交付状态**：✅ 已交付@v0.24.0

### FR-526 桥能力接入（feat，需 spec，spec

**需求补充**：`docs/specs/business-write-reconcile-bridge/spec.md`，依赖 FR-521）：接入 ServerProbe 已具备但 JM 未消费的能力——`core.subscribe`（主题订阅）、`core.config_reload`（配置重载，**超时独立配置不复用业务命令 5s**）、`core.bridge_status`（重连/队列/丢弃计数）、`core.business_status`（Provider 健康）、`chunk` 分片帧重组（按 `groupId` 重组并超时清理）。验收：五类能力均可经 JM 调用并回执；分片重组在丢帧时能被检出；reload 超时不阻塞业务命令。**已交付**：三条 bridge 路由（`GET /instances/:id/bridge/status`、`PUT /instances/:id/bridge/subscription`、`GET /instances/:id/business/status`）+ Worker 侧四条独立 RPC（`QueryBridgeStatus`/`SetBridgeSubscription`/`QueryBusinessStatus`/`ReloadProbeConfig`，reload 默认 10s 独立超时）+ WS 读循环内的分片重组（并行上限 8、30s 超时、`INCOMPLETE_CHUNK` 记日志）。域状态 30s 内存缓存、探针不可达回退 stale。

**交付状态**：✅ 已交付@v0.24.0

### FR-524

业务写穿与离线禁改

✅ 代码完成（2026-10-24）：写穿经 `DispatchWrite`；审计只记通道事实（actor/target/domain/action/requestId/result/duration），**payload/output 不入审计**（并清掉了 `inventory.writeBasicAttrs` 的 payload 解析特判）。离线禁改由 `resolveTarget` 前置校验链保证零副作用。


### FR-525

业务对账协调

✅ 代码完成（2026-10-24）：`service/business_reconcile.go` + `router/business_reconcile.go`。复用批量引擎的目标解析/并发/幂等，但**不走**其写语义流程（confirmToken/风险分级/payload 必对象均不适用于只读）。`action` 由调用方指定，JM 不预设；只调一台的 manifest 校验「ACTION 声明为 WRITE 则拒绝」（manifest 取不到时放行+warn，理由见 spec）。过程中修掉真实缺陷：`sendToProbe` 硬编码 8s 让 60s 超时形同虚设 → 新增 `DispatchWithTimeout`。

