# 功能规格：受管资源归因与首页仪表 Tooltip

> 状态：草拟　·　关联 PRD：FR-400　·　优先级：P1　·　依赖：FIX-观测刷新　·　下游：FR-401、FR-402

## 1. 背景与目标

首页已有跨节点 CPU、负载和内存总量，但无法解释数值来源，也会把已失联节点的旧值误认为实时值。既有实例进程 TopN 只在监控页可见，Bot Worker 与 Go Worker 当前资源没有安全、常驻的采集入口。

本 FR 建立**唯一的受管运行时实时快照契约**：Worker 在既有反向隧道 Heartbeat 中上报自身与已运行 Bot Worker 的只读快照，CP 保存当前值；首页以有界 Tooltip 展示采样时间、鲜度、节点/实例/进程 TopN，并可下钻。它只观测平台受管资源，绝不枚举或控制任意 OS 进程。

## 2. 需求（要什么）

### 范围内

1. 首页总览的 CPU、负载、内存仪表均可用鼠标与键盘打开 Tooltip；显示总览采样时间、90 秒鲜度、在线/陈旧/离线状态及可用节点数。
2. Tooltip 最多展示 10 条受管资源归因项：节点当前资源、受管实例进程树聚合及其 TopN 进程；每项可跳转到带相同节点或实例筛选的监控页。
3. Heartbeat 加性上报 Worker 与 Bot Worker 的真实 RSS、CPU，以及 Bot Worker 活跃/连接中数、事件循环 P95、可用状态、不可用原因和观测时间。Worker 未启动 Bot Worker 时不启动它；旧 Worker 缺字段时明确为不可用。
4. CP 只保留当前快照；后续 FR-401 才把此契约写入 ADR-013 历史时序。
5. 进程条目只来自现有的 RUNNING 受管实例根 PID 及子进程树快照；陈旧超过 90 秒、离线或无样本时不得计入实时归因。

### 不做

- 不扫描全机任意进程、端口、命令行或提供进程控制。
- 不调用会拉起 Bot Worker 的 `GetBotCapacity` 作为首页轮询来源。
- 不伪造单 Bot RSS、CPU 或共享 RSS 均摊值。
- 不写 Bot Worker 历史、压测会话历史或首页全量实体表（分别由 FR-401、FR-402 负责）。

## 3. 设计（怎么做）

### 3.1 唯一实时快照契约

`proto/worker.proto` 的 `HeartbeatRequest` 追加可选 `ManagedRuntimeSnapshot`。字段全部加性：

| 字段 | 语义 |
|---|---|
| `worker_process_rss_bytes` | Go Worker 自身 RSS；未知不填 |
| `worker_process_cpu_pct` | Go Worker 进程 CPU 百分比；首帧或采样间隔无效时不填 |
| `bot_worker_rss_bytes` | 共享 Bot Worker RSS；不是任一 Bot 的 RSS |
| `bot_worker_cpu_pct` | 共享 Bot Worker 进程 CPU 百分比；不是任一 Bot 的 CPU |
| `bot_active_count` / `bot_connecting_count` | Bot Worker 当前聚合数 |
| `bot_event_loop_p95_ms` | Bot Worker 事件循环 P95 |
| `bot_available` / `bot_unavailable_reason` | 本地 Bot Worker 快照是否可用及原因 |
| `observed_at_unix_ms` | Worker 实际观测时刻 |

Worker 从已存在的本地进程管理器读取只读快照，不创建、重启或探测 Bot Worker。CPU 以同一受管 PID 在相邻采样时刻的进程 CPU 时间差除以单调时间间隔计算；首帧、PID 更换、进程退出或间隔无效时对应值为 `null`，不得代填 0。CP 的 Heartbeat handler 仅在节点认证成功后写入 `Node` 的当前运行时字段；字段缺失或 `bot_available=false` 时清空数值并保存原因，禁止保留旧值冒充实时。

### 3.2 归因查询

新增平台管理员只读端点 `GET /api/v1/metrics/resource-attribution`。它从以下受管真源组织有界响应：

- 节点：当前 `Node` 资源与运行时快照，按 Heartbeat 90 秒窗口标记 `fresh/stale/offline/unavailable`；
- 实例：只聚合 RUNNING 实例的最新受管进程树快照；
- 进程：复用 `ProcessMetricSnapshot`，按 CPU 或 RSS 排序并限制 TopN。

RSS 是进程观察值，可能含共享页，**不能**与节点已用内存相加或宣称精确分账。命令摘要沿用现有脱敏结果。端点不扩展原 `/metrics/overview`，避免向普通成员扩大跨节点/跨实例可见性。

### 3.3 首页

`OverviewPage` 仅在用户打开仪表 Tooltip 时查询归因端点；Tooltip 采用项目既有可访问组件，触发器可 Tab 聚焦、Escape 关闭。首页仍只显示聚合与 TopN，不渲染任意 OS 进程表。跳转使用既有监控页的 node/instance URL 参数，不新建页面。

## 4. 任务拆分

- [ ] 为 Heartbeat 运行时快照写 proto/Worker/CP 红测与兼容测试。
- [ ] 实现 Worker 本地只读快照；确保未启动 Bot Worker 不会被采集触发。
- [ ] 持久化节点当前快照，并实现 90 秒鲜度与不可用清空规则。
- [ ] 实现归因服务和平台管理员 API（排序、TopN、实例聚合、脱敏）。
- [ ] 实现首页三类仪表 Tooltip 与监控页下钻。
- [ ] 补齐 API、ARCHITECTURE、PRD、CHANGELOG 与中英文文案。

## 5. 验收标准

1. 页面可见时总览每 10 秒更新，隐藏页面暂停；超过 90 秒的心跳或进程快照不会被标为实时。
2. 三个首页仪表均有键盘可达 Tooltip，显示采样时间、鲜度和有界归因列表。
3. 归因 API 仅平台管理员返回 200；其他角色返回 403，且既有 `/metrics/overview` 权限不变。
4. Bot Worker 未启动、首帧 CPU、旧 Worker、反向隧道断开或字段缺失时明确显示不可用/断点，绝不显示伪造的零值。
5. 仅返回受管实例进程树；测试证明任意非受管 OS 进程不会出现在响应中。
6. Tooltip 条目能带相同节点或实例条件进入监控页；不出现首页全量实体列表。
7. Go 服务/路由、Worker/proto、前端单测均通过；真 CP+Worker 环境中改变受管实例负载后，页面无需手刷即可更新，需用户确认。

## 6. 风险 / 待定

- Heartbeat 字段新增必须重新生成 protobuf 代码，并保持老 Worker 可连接。
- Worker/Bot Worker RSS 与节点已用内存口径不同，前端必须持续展示“观察值，非可加总分账”。
- FR-401 只能消费本 FR 的实时快照契约，禁止另建一套 RPC 轮询或再次修改 Heartbeat 语义。
