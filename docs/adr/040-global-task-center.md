# ADR-040: 全局任务中心与长耗时任务进度/日志上报

- **日期**: 2026-06-27
- **状态**: accepted
- **上下文**: 长耗时操作目前**同步阻塞、无可见性**。典型：`JDKService.Install`（`internal/controlplane/service/jdk.go`）发起一个 **20 分钟超时的阻塞 gRPC `InstallJDK`**，把 HTTP 请求一直挂到 Worker 下载+解压完成；`downloadAndExtract` 用 `io.Copy` 一把梭、**零进度回调**。运营者点「一键下载」后**看不到进度、看不到日志、不知道装完没**，长安装还可能在反代处超时。全库**无任何 task/job/notification 模型**——长任务的状态无处沉淀、无处展示。需要一套**可复用的长任务可见性机制**：实时进度 + 日志 + 历史，配一个完成/失败的站内信提醒。

## 决策

引入**全局任务中心（Task Center）**：CP 侧 `Task` 模型沉淀长任务状态，长任务**异步化**（发起即返回 task id），Worker 侧任务进度/日志**搭车心跳上报**，前端**轮询**展示任务中心，任务**完成/失败时推一条站内信**。JDK 安装为首个接入方，框架可复用、其余长任务按需接入（YAGNI，不一次性 retrofit）。

### 1. Task 模型（CP DB）

- 字段：`id`、`type`（如 `jdk.install`）、`scope`（node/instance/global）、`target_ref`（如 nodeId）、`status`（queued/running/succeeded/failed/canceled）、`progress`（0–100）、`title`、`created_at`/`updated_at`/`finished_at`、`error`。
- 日志：每任务一段**有上限的日志缓冲**（capped，超限滚动；存为独立 `task_logs` 行或任务行内的环形缓冲，spec 定）。
- 仅 CP 可读写（架构不变量：数据库仅 CP）。

### 2. 异步发起（取代阻塞调用）

- CP 收到「装 JDK」请求 → 建 `Task`（queued）→ **令 Worker 启动**安装（Worker 立即返回 worker 侧任务句柄，不阻塞）→ CP 返回 **task id**。原 20 分钟阻塞 RPC 拆为「启动（立即返回）+ 进度上报（搭心跳）+ 完成落库」。
- Worker 侧维护**运行中任务登记表**（内存），按 CP 传入的 task id 关联，后台 goroutine 执行下载/解压并更新进度与日志。

### 3. Worker→CP 上报：搭车心跳（不新增流式 RPC）

- Worker 已有**周期性心跳流**上报实例状态 + 指标（`HeartbeatRequest`，CP `IngestHeartbeat`）。任务进度**复用此通道**：`HeartbeatRequest` **加性新增** `tasks` 重复字段，携带运行中任务的 `{taskId, state, progress, recentLogLines}` 快照；CP 心跳处理时 upsert 到 `Task`。
- 选搭车心跳而非新建 `TaskEvent` 流式 RPC：与「实例状态/指标已搭心跳」一致、零新协议面、最小侵入。代价是进度粒度=心跳周期（秒级），对「下载/解压」类任务足够。
- 纯 CP 侧任务（如未来自更新 rollout、已有独立面板）由 CP 直接更新 `Task`，不经 Worker。

### 4. 浏览器→CP：轮询（不引入 user→CP WS）

- 前端**全局任务中心**（抽屉/页）轮询 `GET /tasks`（列表+筛选）、`GET /tasks/:id`（详情+日志），运行中短轮询、空闲停——与既有 rollout/告警铃铛的轮询模式一致。
- **不引入浏览器→CP 的 WebSocket**（那是 V1.1 FR-106、架构不变量之外）。

### 5. 站内信（Notification 收件箱）

- 轻量 `Notification` 模型：`id`、`level`（info/success/warning/error）、`title`、`body`、`link`（指向 task）、`read`、`created_at`。
- 任务**完成/失败**时 CP 生成一条站内信。前端在顶栏放一个收件箱入口（与告警铃铛并列或合并，spec 定）显示未读 + 最近。**最小范围**：只做完成/失败这类事件消息 + 已读未读，**不做**全功能消息系统。

### 6. 边界（YAGNI）

- 框架 + **JDK 异步安装首个接入**。rollout（自更新）/全文索引（FR-113）/备份等其它长任务**可按需接入**（建 Task + 上报进度），**本期不 retrofit**。
- 不做任务取消的强保证（首版 JDK 安装可不支持中途取消；spec 定是否提供 best-effort cancel）。

## 理由

- **复用既有通道与模式**：进度搭心跳、展示走轮询、与实例状态/指标/rollout/告警同构，避免为长任务另起一套割裂的实时机制。
- **异步化解决根因**：把「阻塞 20 分钟、无反馈」拆成「即时返回 + 持续上报」，运营者全程看得见。
- **框架先行、单点接入**：先把 JDK 这一最痛的场景接通，框架设计为可复用，其余长任务自然演进，不过度预建。

## 后果

- 新增表：`tasks`、`task_logs`（或内联日志）、`notifications`；AutoMigrate 建表。
- proto：`HeartbeatRequest` 加性新增 `tasks` 字段（向后兼容：旧 Worker 不发即空）；Worker 心跳组装任务快照。
- `JDKService.Install` 改为异步（返回 task id，JDK 记录在任务成功时落库）；Worker `InstallJDK` 由「阻塞到完成」改为「启动即返回 + 后台执行 + 心跳上报」（或新增 `StartJDKInstall`，spec 定）。
- 新增 CP `TaskService` + `NotificationService` + 路由（`/tasks`、`/notifications`）+ RBAC。
- 前端新增全局任务中心 + 站内信收件箱（FR-183）。
- 真机验收：真机装一次 JDK，任务中心可见进度推进 + 日志，完成后收到站内信。

## 替代方案

- **新增专用 `TaskEvent` 双向流 RPC**：进度更实时（不受心跳周期限制），但多一套流式连接与生命周期管理；放弃（搭心跳够用、更省，必要时后续再升级）。
- **浏览器经 WebSocket 实时收任务进度**：体验最佳，但引入 user→CP WS（FR-106，V1.1 范围外 + 架构不变量约束）；放弃（轮询与既有模式一致）。
- **不建模型、仅靠 toast/日志**：最省，但长任务无历史、刷新即丢、无站内信、无法跨页查看；放弃（用户明确要「全局队列 + 看得见过程 + 完成提醒」）。
- **直接接管 rollout/索引/备份全部长任务**：一步到位统一，但范围过大、回归面广；放弃（YAGNI，框架可扩展、按需接入）。

## 关系

- **ADR-002（gRPC）**：进度上报搭既有心跳流，未新增 RPC 协议（仅加性扩 message）。
- **架构不变量**：数据库仅 CP 读写；不引入浏览器→CP WS（轮询）；Worker 不直接写库（经心跳上报、CP 落库）——本 ADR 全部遵循。
- **FR-033（JDK 与运行时管理）/ FR-178**：JDK 异步安装为首个接入方。
- **FR-081 自更新 rollout / FR-113 全文索引**：未来可接入任务中心的候选长任务（本期不接）。
- **FR-183（全局任务中心）**：本 ADR 的落地 FR。
