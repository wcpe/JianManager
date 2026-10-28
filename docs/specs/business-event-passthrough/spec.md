# FR-522 业务事件纯透传与 SSE 事件流

> 状态：草拟 · 优先级：P0 · 依赖：FR-521（退役业务能力）
> 关联：FR-536（SSE 连接治理）、FR-525（对账）、ServerProbe FR-36（事件 seq）

## 1. 背景与目标

JM 目前把探针上报的业务事件落进 `business_events` 等三张表，并做镜像聚合——这属于业务语义，与「JM 不做任何业务」的分层定位冲突（FR-521 已退役）。

本 FR 建立**替代通道**：JM 收到探针业务事件后**不落库、不解析、不聚合**，经 SSE 原样转发给下游业务台。业务台自己决定存不存、怎么存。

### 目标

- JM 侧零业务数据持久化
- 事件可按 domain / topic 订阅过滤
- 断线可在窗口内按位点补读
- 100+ 实例规模下不丢关键事件、不拖垮服务

### 非目标（明确不做）

- 不做业务语义解析、不做领域聚合
- 不做长期存储（真源在插件，镜像在业务台）
- 不定义任何动作名或 payload 字段
- 不保证「至少一次」的持久投递（这是业务台 + 对账的职责）

## 2. 数据流

```
业务插件
  │ BusinessEventSink.emitReliable(domain, dedupKey, topic, fields, seq)
  ↓
ServerProbe（可靠队列 + spill + 主题滤镜）
  │ WS 帧 { type:event, domain, topic, dedupKey, seq, payload, ... }
  ↓
JM Worker（gRPC 通道原样转发，不解析 payload）
  ↓
JM CP  ┌─────────────────────────────────────┐
        │ ① 原样推给所有匹配订阅者（SSE）        │
        │ ② 写入内存环形缓冲（供补读）           │
        │ ③ 递增通道指标（不按业务分类）          │
        └─────────────────────────────────────┘
  │ SSE
  ↓
业务台（落自有库，按 dedupKey 去重，用 seq 检测缺口）
## 2.1 两个 seq 必须区分（关键）

系统里存在**两个不同的序号**，名字相近但语义完全不同，混用会导致补读失效：

| 名称 | 生成方 | 作用域 | 用途 |
|---|---|---|---|
| `seq` | **探针**（ServerProbe FR-36） | **单个 domain 内**单调递增 | 业务台检测"同一插件的事件有无缺失" |
| `streamSeq` | **JM CP** | **CP 全局**单调递增（随缓冲） | SSE 的 `id` 字段，用于 `Last-Event-ID` 补读 |

### 为什么必须是两个

探针的 `seq` 是 per-domain 的：实例 A 的 `activity` 域 seq=1024，实例 B 的 `activity` 域也可能是 seq=1024。**跨实例不可比**。

而 SSE 补读需要一个**全局单调**的位点才能定位"从哪一条之后补发"。所以：

- **SSE 的 `id` 字段 = `streamSeq`**（CP 分配）
- **事件体里的 `seq` = 探针原始序号**（原样透传，供业务台判断单实例单域是否漏事件）

### 业务台该怎么用

| 判断什么 | 用哪个 |
|---|---|
| 断线重连后补发是否完整 | `streamSeq`（框架自动处理，业务台无需关心） |
| 某实例某域是否漏事件 | `seq`，且必须按 `(instanceId, domain)` 分组比较 |
| 调用方要主动拉取某位点后的事件 | `since` / `Last-Event-ID` 传 `streamSeq` |

**反例**：用 `seq` 当 `Last-Event-ID` 会导致跨实例串位——实例 A 的 seq=100 和实例 B 的 seq=100 会被当成同一个位点。

### 字段命名

- SSE 帧的 `id:` 行 → `streamSeq`
- JSON body 里同时有 `streamSeq`（CP 分配）与 `seq`（探针原始）

```

## 3. 接口契约

### 3.1 订阅端

```http
GET /api/v1/business/stream
Authorization: Bearer <agent-token>

Query:
  domains=activity,economy         # 可选，逗号分隔；缺省=全部
  topics=activity.spring-2026      # 可选，支持前缀通配 activity.*
  since=<seq>                      # 可选，等价于 Last-Event-ID
Header:
  Last-Event-ID: <seq>             # 浏览器 EventSource 自动携带
```

响应头必须包含：

```
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
X-Accel-Buffering: no
```

### 3.2 事件帧格式

```
id: 9001
event: business.event
data: {"streamSeq":9001,"node":"node-main","instance":5,"instanceName":"game-01","domain":"activity","topic":"activity.spring-2026","dedupKey":"e-8891","seq":1024,"occurredAt":1730000000,"payload":{...}}
```

**关键点**：

- SSE 原生 `id` 字段承载 **`streamSeq`**（CP 分配的全局位点，见 §2.1）——浏览器 `EventSource` 断线重连会自动带 `Last-Event-ID`，补读逻辑零成本。**不要用探针的 `seq` 做补读位点**，它只在单 domain 内单调。
- `payload` 是**不透明对象**，JM 原样透传，不解析、不校验内容
- `instance` / `instanceName` / `node` 是 JM 补充的信封元数据（便于业务台定位来源）

### 3.3 补读语义

| 情况 | 行为 |
|---|---|
| `Last-Event-ID` 在缓冲窗口内 | 从该位点后补发，然后接续实时流 |
| `Last-Event-ID` 早于窗口起点 | 先发 `event: replay_gap`（含 `oldestAvailableSeq`），再从窗口起点补发 |
| `Last-Event-ID` 晚于当前（异常） | 忽略，直接进实时流并记 WARN |
| 未带 `Last-Event-ID` | 只推实时流，不补历史 |

`replay_gap` 帧：

```
event: replay_gap
data: {"requestedSeq":888,"oldestAvailableSeq":1002,"reason":"BUFFER_EXPIRED"}
```

业务台收到 `replay_gap` 后**必须触发一次全量对账**（FR-525），不能只靠事件补齐。

### 3.4 心跳

```
event: ping
data: {}
```

间隔 15–30 秒（可配，默认 20s）。用途：防中间设备超时断连，同时让客户端能判断连接活性。

## 4. 环形缓冲设计

### 4.1 容量模型（100+ 实例）

| 参数 | 取值 | 推导 |
|---|---|---|
| 实例规模 | 100+ | 用户明确要求 |
| 单实例事件率 | 峰值 20 条/秒 | 活动高峰期保守估计 |
| 总峰值 | ~2000 条/秒 | 100 × 20 |
| 单条事件大小 | ~500 B | 含 payload 与信封 |
| 时间窗口 | **30 秒** | 覆盖常见断线重连（浏览器/网络抖动） |
| 容量上限 | **200,000 条** | ≈100 MB |
| 触发规则 | 时间或容量**先到者生效** | 双阈值 |

### 4.2 数据结构

```go
type eventRing struct {
    mu       sync.RWMutex
    buf      []*EventFrame   // 固定容量环形
    head     int             // 最旧位置
    size     int             // 当前条数
    oldestSeq uint64         // 窗口内最旧 seq
    newestSeq uint64         // 已写入的最新 seq
    byDomain map[string][]int // 可选索引，加速 domain 过滤
}
```

**选型理由**：

- 固定大小切片 + 头尾指针，避免 GC 压力（20 万对象频繁回收会拖慢 CP）
- 写入 O(1)，读取按 seq 二分定位 O(log n)
- 不需要通用库，标准库即可

### 4.3 溢出行为

缓冲满时**丢最旧**，并：

1. 更新 `oldestSeq`
2. 递增 `droppedCount` 指标
3. **不逐个通知订阅者**（避免放大）；订阅者下次补读时会收到 `replay_gap`

### 4.4 明确的性质

| 性质 | 说明 |
|---|---|
| **非持久** | 进程重启即失，不做任何落盘 |
| **非真源** | 真源在插件，镜像在业务台 |
| **可丢** | 窗口外必然补不到，靠对账兜底 |

这三条必须写进 API 文档，防止业务台误以为 JM 是可靠消息队列。

## 5. 转发路径的性能约束

| 约束 | 要求 |
|---|---|
| 不解析 payload | 只做 JSON 透传，不 unmarshal 内容 |
| 不阻塞接收主链路 | 每个订阅者独立有界队列，见 FR-536 |
| 不做同步 IO | 无数据库写入、无磁盘操作 |
| 指标为内存计数 | 定期快照，不逐条写库 |

**禁止事项**：

- ❌ 转发前校验 payload 结构（校验是插件的职责）
- ❌ 缓存 payload 供后续查询
- ❌ 按 domain 做限流或业务优先级判断

## 6. 可观测指标

| 指标 | 类型 | 说明 |
|---|---|---|
| `business.events.received` | Counter | 收到的事件总数 |
| `business.events.forwarded` | Counter | 成功推给至少一个订阅者 |
| `business.events.dropped` | Counter | 缓冲溢出丢弃数 |
| `business.events.gap_served` | Counter | 返回 `replay_gap` 的次数 |
| `business.buffer.size` | Gauge | 当前缓冲条数 |
| `business.buffer.oldest_seq` | Gauge | 窗口最旧位点 |
| `business.subscribers` | Gauge | 当前 SSE 连接数 |

**注意**：这些都是**通道维度**指标，不按业务语义分类（符合 FR-527 的边界）。

## 7. 与其他模块的边界

| 模块 | 关系 |
|---|---|
| Worker | 只做帧转发，不解码 payload |
| FR-525 对账 | 收到 `replay_gap` 后由业务台主动触发 |
| FR-536 SSE 治理 | 本 FR 定义数据语义，FR-536 定义连接保护 |
| ServerProbe FR-36 | `seq` 由探针生成（per-domain），JM 只透传；`streamSeq` 由 JM 分配 |
| FR-527 统计 | 通道指标暴露给外部只读接口 |

## 8. 验收标准

### 功能

1. 订阅后可收到全部匹配 `domains` / `topics` 的事件
2. 未匹配的事件不下发
3. 断开后带 `Last-Event-ID` 重连，窗口内事件可补齐
4. 窗口外重连返回 `replay_gap` 且含 `oldestAvailableSeq`
5. 心跳按配置间隔发送
6. `id` 字段与 `streamSeq` 一致（**不是**探针的 `seq`）

### 边界

7. 100 实例 × 20 事件/秒压测下，CP 内存增长受控（缓冲不超 200k 条）
8. 慢消费者不影响其他订阅者（见 FR-536）
9. 缓冲溢出后 `droppedCount` 递增且下次补读返回 `replay_gap`
10. 进程重启后缓冲清空，`oldestSeq` 从新值开始

### 只读性

11. 全链路无数据库写入（`business_events` 等表删除后功能正常）
12. `payload` 内容不在任何日志、审计、指标中出现

### 真机

13. 真实 ServerProbe + 真实业务插件 + 真实 CP，端到端事件可达
14. 断开浏览器 DevTools 网络后重连，事件不重复、不丢失（窗口内）

## 9. 风险与待定

| 风险 | 缓解 |
|---|---|
| 2000 事件/秒下 Go GC 压力 | 固定切片池化 + 避免 payload 二次分配 |
| SSE 单连接成为瓶颈 | FR-536 连接治理 + HTTP/2 多路复用 |
| 业务台处理慢导致积压 | 有界队列 + 慢消费者断开 |
| `seq` 跨实例不连续 | 补读一律用 `streamSeq`；业务台判缺口时才用 `seq` 且按 `(instanceId, domain)` 分组 |

**待定**：

1. 缓冲容量是否需要按实例数动态调整（当前固定 200k）
2. `topics` 通配符是否支持多级（`activity.*.progress`）——当前建议只支持单级后缀
3. 是否需要按 `domain` 分多条 SSE 连接由服务端自动拆分（当前由客户端决定）

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-522 业务事件纯透传 + SSE 事件流（feat，需 spec，spec

**需求补充**：`docs/specs/business-event-passthrough/spec.md`，依赖 FR-521）：CP 不落任何业务数据，收到探针 `event` 帧后经 `GET /business/stream`（SSE）原样转发 `domain/topic/dedupKey/seq/payload/instanceId`；保留**内存环形缓冲**支持断线补读，**双阈值 30 秒窗口或 200000 条（先到者生效）**，溢出丢最旧并置 `gap` 标记；超出窗口回 `REPLAY_GAP` 触发上层全量对账。按 100+ 实例规模设计（峰值约 2000 事件/秒）。**转发路径不做 payload 解析与转换**；缓冲深度、丢弃数、`gap` 次数必须暴露可观测计数。验收：SSE 可订阅并过滤 domain/topic；`Last-Event-ID=seq` 窗口内可补读；窗口外明确返回 `REPLAY_GAP` 而非静默丢；缓冲不落库、重启即失，文档标注「非持久非真源」；积压与丢弃指标可查。

**交付状态**：✅ 已交付@v0.24.0

### FR-522

业务事件流（SSE 透传）

✅ 代码完成+真机验收（2026-10-25）：`service/business_stream_buffer.go`（环形缓冲 20 万条/30 秒双阈值，预渲染 SSE 字节使序列化移出扇出热路径）+ `business_stream.go`（订阅扇出/过滤/连接治理）+ `router/business_stream.go`。**真机**：SSE 心跳按 20s 到达、`id` 承载 CP 全局 `streamSeq`（与探针 per-domain `seq` 分离，spec §2.1 已澄清）。

