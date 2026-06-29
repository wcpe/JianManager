# 任务强制停止 + 筛选（FR-227）

> 状态：开发中 · 增强 FR-183（任务中心，见 ADR-040）· **改 proto**（HeartbeatResponse 加 cancel_task_ids）

## 1. 需求

任务中心两项缺口（原始诉求 #6）：

1. **强制停止卡死任务**：在跑/卡死的长任务（如 JDK 安装）可一键强停，**Worker 侧操作真被中断**（下载中断 + 临时文件清理），任务转**新终态 canceled**。
2. **筛选查询**：任务列表按 `kind / state / node / 时间 / 关键词` 过滤。

## 2. 设计

### 2.1 真中断（复用心跳双向流，不新增 RPC）

任务进度本就经心跳上报（worker→CP `HeartbeatRequest.tasks`，ADR-040）；取消走反向（CP→worker `HeartbeatResponse`）：

- **proto**：`HeartbeatResponse` 加 `repeated string cancel_task_ids = 5`（CP 每拍把该节点「请求取消且未终态」的任务 id 下发）。
- **Worker `taskreg`**：`task` 加 `cancel context.CancelFunc`；`Start(id, cancel)` 登记；新增 `Cancel(id)`（调 cancel + 置 `canceled` 终态）；新增 `stateCanceled`；`Succeed/Fail` 加终态守卫（已 canceled 不被覆盖）。
- **Worker InstallJDK 异步路径**：`ctx,cancel := WithCancel(Background())` → `tasks.Start(taskID, cancel)` → 把 ctx 透传到 `InstallWithProgress`→`downloadAndExtractWithProgress`，内部 stall 看门狗的 `WithCancel(Background())` 改 `WithCancel(ctx)`（外部取消即中断 HTTP 请求；临时文件由既有 `defer os.Remove` 清理）。
- **Worker 心跳**：收到 `HeartbeatResponse.cancel_task_ids` → 逐个 `registry.Cancel(id)`。

### 2.2 CP

- **model.Task**：加 `TaskStateCanceled="canceled"`（`IsTerminal` 含之）+ 字段 `CancelRequested bool`（取消意图，驱动心跳下发）。
- **service.TaskService.Cancel(id, uid)**：终态→409；**pending 未起 / 节点离线** → 直接置 canceled（无 worker 操作可中断）；running 在线 → 置 `CancelRequested=true`（等 worker 确认）。
- **heartbeat handler**：构建 `HeartbeatResponse` 时查该节点 `cancel_requested && state∈(pending,running)` 的任务 id 填 `cancel_task_ids`；处理 `TaskSnapshot` 时接受 `canceled` 终态（落库 + 不发「成功」站内信）。
- **service.TaskService.List**：加筛选参数 `kind/state/nodeId/keyword/since/until`。
- **router**：`POST /tasks/:id/cancel`；`GET /tasks` 加 query 筛选。

### 2.3 前端

- TasksPage：pending/running 任务行加「强制停止」（`DangerConfirm` 二次确认）；`CancelRequested && !terminal` 显「取消中」、`canceled` 显「已取消」。
- 列表筛选条：kind（下拉）/ state（下拉，含 canceled）/ node（下拉）/ 关键词（搜标题/detail）/ 时间（可选）。
- api/tasks：`useCancelTask` + List 透传筛选 params；mock 域补 cancel + 筛选。

## 3. 验收

- [ ] `POST /tasks/:id/cancel`：终态 409；pending/离线直接 canceled；running 在线置 CancelRequested、心跳下发 → worker 中断下载 + 清临时 → 上报 canceled → CP 落 canceled 终态。
- [ ] `canceled` 为终态；不触发「安装成功」副作用（不落 NodeJDK）。
- [ ] `GET /tasks` 按 kind/state/node/关键词/时间筛选生效。
- [ ] 前端：强制停止二次确认 + 取消中/已取消态 + 筛选条；mock 全站可点。
- [ ] 后端单测（registry Cancel 终态守卫 / 离线直取消 / 心跳下发 cancel_task_ids / 筛选）+ 前端 tsc/lint/vitest 绿。
- [ ] **真机验**：真机起 JDK 安装→强停→下载真中断、任务转 canceled、临时文件清理。

## 4. 关联

proto `HeartbeatResponse`；worker `taskreg/registry.go`、`grpc/server.go`、`jdk/{install,manager}.go`、`heartbeat`；CP `model/task.go`、`service/task.go`、`grpc/handler.go`、`router/task.go`；web `pages/TasksPage.tsx`、`api/tasks.ts`。
