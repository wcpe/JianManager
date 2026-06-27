# FR-183 spec：全局任务中心 + 完成站内信

> 状态：待审　·　关联 PRD：FR-183　·　关联 ADR：040（任务中心）　·　分支：feature/fr-183-task-center

## 1. 背景与目标

长耗时任务（JDK 安装首当其冲）目前**同步阻塞、无进度/日志/完成提醒**（`JDKService.Install` 阻塞 20min、`downloadAndExtract` 零回调）。全库无 task/notification 模型。

**目标**（ADR-040）：可复用的全局任务中心——长任务异步化 + 实时进度 + 日志 + 历史，配完成/失败站内信；JDK 安装为首个接入方。

## 2. 范围

### 范围内
- `Task` 模型 + `TaskService` + 路由（list/get）。
- `Notification`（站内信）模型 + `NotificationService` + 路由（list/read）。
- `HeartbeatRequest` **加性新增** `tasks` 字段；CP 心跳 ingest 更新 Task。
- **JDK 安装异步化接入**：CP 建 Task → 令 Worker 启动（即返回）→ Worker 后台执行 + 心跳上报进度/日志 → CP 更新 Task → 完成落 `NodeJDK` + 发站内信。
- 前端：全局任务中心（轮询，进度+日志+历史）+ **独立 `NotificationInbox` 组件**（完成/失败 + 已读未读）。

### 非目标
- 不接入其它长任务（rollout/索引/备份）——框架可扩展，本期只接 JDK（YAGNI）。
- 不引入 user→CP WS（轮询，FR-106 范围外）。
- 不做任务中途取消的强保证（首版不做或 best-effort，落地定）。

### ⚠️ 解耦约束（避免与 FR-179 撞 ConsoleHeader）
本 FR **只建独立 `NotificationInbox` 组件 + 任务中心入口**，**不修改 `ConsoleHeader.tsx`**。收件箱 mount 进顶栏留作 FR-179 落 main 后的微集成（归 FR-179 通知槽或一条收尾接线提交）。git diff 须自证未碰 `ConsoleHeader.tsx`。

## 3. 设计

### 3.1 数据模型（CP DB）
- `Task`：id、type(`jdk.install`)、scope(node/instance/global)、targetRef、status(queued/running/succeeded/failed/canceled)、progress(0–100)、title、error、createdAt/updatedAt/finishedAt。
- `TaskLog`：taskId、seq、ts、line（capped 滚动；或 Task 内联环形缓冲，落地定）。
- `Notification`：id、level(info/success/warning/error)、title、body、link、read、createdAt。
- AutoMigrate 建表。

### 3.2 进度上报（ADR-040 §3，搭心跳）
- proto `HeartbeatRequest` 加 `repeated TaskProgress tasks`（taskId、state、progress、recent_log_lines）。**加性兼容**（旧 Worker 不发即空）。
- Worker：运行中任务内存登记表；后台 goroutine 执行（JDK 下载用带进度计数的 reader 包 `io.Copy`），每心跳带快照。
- CP `IngestHeartbeat`：按 taskId upsert Task + 追加日志。

### 3.3 JDK 异步化（接 FR-033/178）
- `JDKService.Install` 改为：建 Task(queued) → gRPC 令 Worker 启动安装（**即返回 taskId，不再阻塞 20min**）。
- Worker `InstallJDK`（或新增 `StartJDKInstall`）：启动即返回，后台下载/解压 + 上报；完成时心跳标 done + 携带 JDK 详情，CP 落 `NodeJDK` + 发**成功**站内信（失败发**失败**站内信）。
- 前端 `useInstallJDK` 改为拿 taskId、引导去任务中心看进度（FR-178 面板可内嵌该任务进度）。

### 3.4 API（CP，登录用户 + RBAC）
- `GET /tasks`（筛选 status/type/scope）、`GET /tasks/:id`（含日志）。
- `GET /notifications`、`GET /notifications/unread-count`、`POST /notifications/:id/read`、`POST /notifications/read-all`。

### 3.5 前端
- 任务中心：全局抽屉/页，轮询 `/tasks`（运行中短轮询、空闲停），进度条 + 状态 + 展开看日志 + 历史。
- `NotificationInbox`：独立组件，轮询未读数 + 最近列表 + 标已读；**不挂 ConsoleHeader**（见解耦约束）。

## 4. 任务拆分
- [ ] model：Task/TaskLog/Notification + migration
- [ ] proto：HeartbeatRequest +tasks（加性）+ 重新生成
- [ ] Worker：任务登记表 + 带进度的下载 + 心跳带任务快照
- [ ] CP：IngestHeartbeat upsert Task + 日志；TaskService/NotificationService + 路由 + RBAC + 审计
- [ ] JDK 异步化：Install 改异步、Worker 启动即返回、完成落 NodeJDK + 发站内信
- [ ] 前端：任务中心（轮询）+ NotificationInbox（独立，不挂 header）+ i18n + 明暗/双主题
- [ ] doc-sync：API.md、ARCHITECTURE（任务中心 + 心跳 tasks 字段）、CHANGELOG、PRD FR-183 行
- [ ] 收尾（落 main 后微集成，非本 worktree）：NotificationInbox mount 进 FR-179 重设计的 ConsoleHeader

## 5. 验收
- [ ] Task/Notification 模型 + API + RBAC + 审计
- [ ] HeartbeatRequest tasks 字段加性、旧 Worker 不发不报错
- [ ] JDK 安装异步：发起即返回 taskId、不再阻塞；任务中心见进度推进 + 日志滚动
- [ ] 安装完成落 NodeJDK + 发成功站内信；失败发失败站内信
- [ ] NotificationInbox 独立可用（未读数/列表/标已读）；**未触碰 `ConsoleHeader.tsx`**（git diff 自证）
- [ ] 轮询模式、无 user→CP WS
- [ ] i18n + 明暗 + 双主题
- [ ] **真机闸（用户验）**：真机装一次 JDK，任务中心实时进度 + 日志，完成收到站内信
