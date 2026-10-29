# FR-536 / FR-537 SSE 连接治理与批量流式进度

> 状态：**已实现（状态行原为「草拟」，实现早已落地，2026-10 对账修正）**：business_batch_stream.go + business_batch_stream_test.go · 优先级：P0/P1 · 依赖：FR-522（事件透传）、FR-523（批量）

---

# FR-536 SSE 连接治理与慢消费者保护

## 1. 背景

SSE 是**单连接承载全部事件**的模型。100+ 实例、峰值 2000 事件/秒时，存在三类真实风险：

1. **慢消费者拖垮全局**：一个客户端处理慢，发送缓冲无限增长，最终 OOM
2. **连接数失控**：业务台重启/多实例部署时连接堆积
3. **中间设备静默断连**：代理超时导致连接被回收但双方不知

## 2. 目标

- 单个慢消费者不影响其他订阅者和事件接收主链路
- 连接数可控
- 断连可检测、可诊断

## 3. 慢消费者保护（核心）

### 3.1 有界发送队列

```go
type subscriber struct {
    ch       chan *EventFrame  // 容量 1024
    done     chan struct{}
    dropped  int64            // 原子计数
    lastSeen time.Time
}
```

**规则**：

- 每订阅者独立队列，容量默认 1024
- 投递时 `select { case s.ch <- f: default: /* 队列满 */ }`
- 队列满 → **断开该订阅者**，记 `SLOW_CONSUMER`，**不阻塞主链路**

### 3.2 为什么是"断开"而不是"丢弃最旧"

| 方案 | 问题 |
|---|---|
| 丢最旧继续推 | 客户端收到的事件流是**不连续**的，且它不知道丢了什么，会以为数据完整 |
| 阻塞等待 | 一个慢消费者拖垮所有订阅者，甚至阻塞事件接收 |
| **断开** | 客户端重连时会带 `Last-Event-ID`，从缓冲补读 → **数据完整性由补读机制保证** |

**断开的语义是安全的**：因为 FR-522 已有补读能力，断开后重连能补齐窗口内的事件。

### 3.3 断连前的最后努力

断开前发送一个明确的终止帧：

```
event: error
data: {"code":"SLOW_CONSUMER","message":"事件处理过慢，连接已关闭，请重连并指定 Last-Event-ID"}
```

## 4. 连接数限制

| 维度 | 默认 | 说明 |
|---|---|---|
| 单 Token 并发 SSE | 5 | 超出返回 429 |
| 全局并发 SSE | 200 | 保护 CP 内存 |

超限响应：

```http
HTTP/1.1 429 Too Many Requests
Retry-After: 5
Content-Type: application/json

{ "code": "TOO_MANY_CONNECTIONS", "limit": 5, "current": 5 }
```

**为什么要限**：业务台多实例部署或重启风暴时，连接数会短暂暴涨。

## 5. 心跳保活

```
event: ping
data: {"t":1730000000}
```

| 参数 | 值 |
|---|---|
| 间隔 | 20s（可配 15–30s） |
| 客户端超时 | 间隔 × 2.5 = 50s |
| 服务端检测 | 写失败即断开 |

**为什么需要**：Nginx/负载均衡默认 60s 无数据断连，心跳能把连接续上。

## 6. 代理适配

必须设置：

```
X-Accel-Buffering: no      # Nginx 禁用缓冲
Cache-Control: no-cache
Connection: keep-alive
```

**如果代理仍然缓冲**，客户端会表现为「事件成批延迟到达」。检测手段：心跳时间戳与收到时间的偏差 > 5s 即告警。

## 7. 可观测

| 指标 | 类型 | 说明 |
|---|---|---|
| `sse.connections` | Gauge | 当前连接数 |
| `sse.connections.by_token` | Gauge | 按 token 分组 |
| `sse.queue.depth` | Gauge | 各订阅者队列深度（取最大值） |
| `sse.slow_consumer_disconnect` | Counter | 因慢消费断开次数 |
| `sse.write_error` | Counter | 写失败次数 |
| `sse.heartbeat_sent` | Counter | 心跳发送数 |

## 8. 验收

1. 注入慢消费者（客户端 sleep）→ 该连接被断开，其他订阅者正常收事件
2. 断开前的 `SLOW_CONSUMER` 终止帧**尽力发送**：
   - 客户端仍在读取但处理慢 → 能收到该帧
   - 客户端完全停止读取（TCP 接收窗口为 0）→ **物理上送不出去**，此时只保证断开与计数

   > 这不是实现缺陷。对端不读就没有可写的通道，任何协议都无法在此时送达最后一帧。
   > 验收应分别覆盖两种场景：受控 writer 验证帧内容契约；真实 socket 验证断开与不影响其他订阅者。
3. 重连带 `Last-Event-ID` 能补齐窗口内事件
4. 单 token 超 5 连接 → 429
5. 心跳按间隔发送
6. 代理缓冲导致延迟时可检测
7. 100 订阅者压测，CP 内存稳定

---

# FR-537 批量命令流式进度

## 1. 背景

100+ 实例批量执行若等全部完成再返回，调用方可能等几十秒且无进度反馈。

## 2. 设计

同一端点，按 `Accept` 头分流：

```http
POST /api/v1/business/batch
Accept: application/json          → 同步返回完整结果（现有行为）
Accept: text/event-stream         → 流式返回进度
```

## 3. 流式帧

```
event: progress
data: {"done":20,"total":100,"succeeded":19,"failed":1,"offline":0}

event: progress
data: {"done":60,"total":100,"succeeded":58,"failed":1,"offline":1}

event: done
data: {"total":100,"succeeded":98,"failed":1,"offline":1,"results":[...]}
```

| 帧 | 时机 |
|---|---|
| `progress` | 每完成一批（默认每 20 个） |
| `done` | 全部完成，含完整 results |
| `error` | 整体失败（如 token 校验失败），此时不发送任何 progress |

## 4. 与 FR-535 的关系

流式模式**同样受 `confirmToken` 与风险分级约束**：

- 高风险操作仍需先 `preview` 拿 token
- token 校验在流开始前完成，失败直接返回 JSON 错误（不是 SSE 帧）

**为什么**：确认是"执行前"的门槛，不该进入流式阶段才校验。

## 5. 中断处理

| 情况 | 行为 |
|---|---|
| 客户端断开 | 服务端**继续执行**（已下发的不可撤回），结果不推送 |
| 服务端重启 | 在途批量丢失，客户端凭 `requestId` 可查幂等结果（若已部分完成） |

**明确语义**：断开 ≠ 取消。如需取消，应使用独立的 cancel 接口（当前不做）。

## 6. 验收

1. `Accept: application/json` 行为与现状一致
2. `Accept: text/event-stream` 能收到 progress 与 done
3. progress 单调递增，`done` 的数值等于最后一次 progress
4. 高风险无 token 时直接返回 409（不进 SSE）
5. 客户端断开后服务端继续执行完成
6. 同 `requestId` 重入不重复执行

## 7. 待定

1. `progress` 的触发粒度（每批 vs 每完成 N 个 vs 时间间隔）
2. 是否需要 `cancel` 接口
3. 同步模式在实例数 > 200 时是否强制拒绝并提示用流式

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-536 SSE 连接治理与慢消费者保护（feat，需 spec，spec

**需求补充**：`docs/specs/sse-governance-and-streaming/spec.md`，依赖 FR-522）：SSE 是单连接承载全部事件的模型，100+ 实例峰值约 2000 事件/秒时存在真实风险，必须显式治理：①**每订阅者有界发送队列**（默认 1024 条），队列满即断开该消费者并记 `SLOW_CONSUMER`，绝不阻塞其他订阅者与事件接收主链路；②**单 Token 并发 SSE 连接上限**（默认 5），超出返回 429；③**心跳保活**每 15–30s 发 `event: ping`，防中间设备超时断连；④**反向代理适配**强制 `X-Accel-Buffering: no` + `Cache-Control: no-cache`；⑤**连接与丢弃可观测**（当前连接数、每连接积压深度、因慢消费断开的次数）。验收：注入慢消费者不影响其他订阅者收到事件；连接数超限被明确拒绝；心跳可见；代理缓冲导致的事件延迟有检测手段；断开原因可查。

**交付状态**：✅ 已交付@v0.24.0

### FR-537 批量命令流式进度（feat，需 spec，spec

**需求补充**：`docs/specs/sse-governance-and-streaming/spec.md`，依赖 FR-523/535）：100+ 实例批量执行若等全部完成再返回，调用方需长时间等待且无法感知进度。新增流式返回：请求带 `Accept: text/event-stream` 时按批回报 `event: progress`（`done/total/succeeded/failed`），全部结束发 `event: done`（含逐实例 results 汇总）；`Accept: application/json` 保持同步返回（现有行为不变）。流式模式同样受 FR-535 的 `confirmToken` 与风险分级约束。验收：同一请求两种 Accept 都能正确完成；流式进度与实际执行进度一致且单调递增；连接中断后重入不重复执行（`requestId` 幂等）；同步模式行为无变化。

**交付状态**：✅ 已交付@v0.24.0

### FR-536

事件流背压与慢消费者

✅ 代码完成（2026-10-24）：每订阅者有界队列（1024），满即断开并尽力发 `SLOW_CONSUMER` 终止帧，**绝不阻塞主链路与其他订阅者**；单 Token 并发 SSE 上限 5（超出 429）；心跳 20s；断开时归还配额（否则重连被自己的旧连接挡住）。真机验收确认「客户端完全停读时终止帧物理上送不出去」，验收标准已务实化为两层。

