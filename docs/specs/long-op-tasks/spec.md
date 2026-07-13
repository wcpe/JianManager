# FR-323: 长操作任务化——导入/克隆/备份纳入任务中心

> 状态：草拟　·　关联 PRD：FR-323（增强 FR-302 导入 / FR-036 克隆 / 备份 FR）　·　关联 FR-319（provision 同族，一并迁共享底座）　·　免新 ADR（沿用 ADR-040 任务中心）

## 1. 背景与目标

用户诉求：「把所有长耗时任务加到任务中心、不堵塞页面、有进度」。现状审计（`.tmp/brainstorm-long-op-tasks-2026-07-13.md`）：
- **导入 migrate 搬迁**（`ImportServerDir`）：同步阻塞，跨盘拷贝可数十分钟，HTTP 请求全程挂起。
- **克隆实例**（`CloneWorkDir`）：同步 unary，大目录数分钟阻塞。
- **备份创建/恢复**：已后台 goroutine（`go executeBackup`）但**无任务 ID、无实时进度**（仅 Backup record 的 Status 字段粗粒度）。
- **provision**（FR-319）：已异步、已在任务中心，但其「CreateTask→goroutine→SetStage→终态」模式是**内联的**，即将被复制 3 遍——值得抽共享底座并让 provision 一并 DRY。

目标：抽一个 CP 侧 `run-as-task` 共享底座；导入 migrate / 克隆 / 备份创建 / 备份恢复接入——提交秒回 `{taskId}` 不阻塞，任务中心显示阶段进度 + 终态 + 失败错误链；provision 迁到同底座（行为不变）。

## 2. 需求与范围

- **共享底座** `TaskService.RunAsync`：登记任务→CP 后台 goroutine 执行→阶段进度→终态（成功/失败）→站内信。业务副作用（statusReason、Backup record 状态等）由 work 函数自己负责，底座只管任务生命周期 + `instance_id` 关联。
- **接入四操作**：导入 migrate、克隆、备份创建、备份恢复。in_place 导入（就地接管，O(1) 无拷贝）保持同步不入任务中心。
- **provision 迁底座**：`ProvisionServerAsync` 改调 `RunAsync`，`provisionOnWorker` 的 taskID 参数换成 `stage` 回调；行为、响应形状、statusReason 语义、既有测试全不变。
- **新 TaskKind**：`import` / `clone` / `backup_create` / `backup_restore`。前端任务中心筛选 + kind 文案。
- **进度模型**：stage 文案为主（搬迁中/拷贝中/打包中/恢复中）+ best-effort 百分比；rename O(1) 直接跳完成段，拷贝/打包取不到真进度时用 stage 文案的不确定态（不强求真%）。
- **范围外**：文件传输进度（FR-324 内联进度条已覆盖）；worker 侧不改（RPC 仍同步返回，「异步」在 CP 后台 goroutine 层实现——搬迁/拷贝/打包本就是 worker 一次 RPC 干完，CP 只是不再同步等它、改后台等）；不引入 worker→CP 的搬迁字节流进度（V1 用 stage 文案）。

## 3. 设计

### 3.1 共享底座 `TaskService.RunAsync`

```go
// RunSpec 一次长操作任务化的参数（FR-323）。
type RunSpec struct {
    NodeID     uint          // 任务归属节点
    InstanceID uint          // 关联实例（0=无）；非 0 写 task.instance_id（启动闸/关联展示复用 FR-319）
    Kind       string        // TaskKind（import/clone/backup_create/backup_restore/provision）
    Title      string        // 任务标题
    Detail     string        // 初始详情（空=「排队中」）
    CreatedBy  uint          // 发起人（归属隔离 + 终态站内信收件人）
    Timeout    time.Duration // 后台执行超时（0=默认 30min）
}

// RunAsync 把长操作跑成后台任务：立即返回 taskID（不阻塞请求）。
// work 收 (ctx, stage)，stage(progress,text) 上报阶段进度；返回 (resultJSON, err)。
// 业务副作用（statusReason/Backup 状态/落库）由 work 自负；底座只管任务生命周期。
func (s *TaskService) RunAsync(spec RunSpec, work func(ctx context.Context, stage func(int, string)) (string, error)) string
```

内部：`CreateTask` → 有 InstanceID 则 `Update("instance_id")` → goroutine{ `context.WithTimeout(Background, timeout)` → `MarkRunning` → `work(ctx, stage)` → err 则 `MarkFailed(err)` 否则 `MarkSucceeded(result)` }。终态站内信由既有 `finalizeTerminal` 按 kind 发（新 kind 走 default 分支通用「标题 完成/失败」）。

### 3.2 provision 迁底座（行为不变）

- `provisionOnWorker(ctx, inst, core, onlineMode, taskID string)` → `provisionOnWorker(ctx, inst, core, onlineMode, stage func(int,string))`：内部 `p.tasks.SetStage(taskID,...)` 换成 `stage(...)`。
- `ProvisionServerAsync` 的内联 goroutine 换成 `RunAsync`；statusReason「搭建中→清空/搭建未完成」逻辑搬进 work 函数（成功前清、失败前置）。
- 既有 `provision_fr319_test.go` 全绿不改（响应仍 `{instance, taskId}`、失败 statusReason 仍标「搭建未完成」）。

### 3.3 导入 migrate 接入

- `ImportServerService.Import`：`mode=in_place` 保持同步（O(1)，无拷贝，秒回）；`mode=migrate` 改 `RunAsync`（kind=`import`，InstanceID=新实例 id，stage「搬迁中…」），端点响应加 `taskId`。
- 失败 work 内清理半成品 + 实例 statusReason 标「导入未完成：…」（沿用导入既有清理语义）。

### 3.4 克隆接入

- `CloneService.Clone`：dryRun 分支不变（同步预览）；实拷分支 `CloneWorkDir` 改 `RunAsync`（kind=`clone`，InstanceID=克隆出的实例 id，stage「拷贝工作目录…」）。`CloneResult` 加 `taskId`。

### 3.5 备份创建/恢复接入

- `BackupService.CreateWithOptions`：`go s.executeBackup(backup)` 改 `s.tasks.RunAsync(spec, work)`，work 包住 executeBackup 逻辑——**仍更新 Backup record 的 Status**（备份列表页不回归）+ stage「打包中…」。kind=`backup_create`。
- `BackupService.Restore`：同样 `RunAsync`（kind=`backup_restore`，stage「恢复中…」）。
- Backup record Status 与任务终态双写：record 供备份列表页，任务供任务中心统一视图；两者独立不耦合。

### 3.6 前端

- import/clone/backup 的 mutation onSuccess 追加 `invalidateQueries(['tasks'])`；wizard/对话框提示「已提交，进度见任务中心」（导入/克隆已有 partialFailure 文案，补 submitted 语义）。
- 任务中心 `TasksPage` kind 筛选加 import/clone/backup_create/backup_restore + `tasks.kind.*` 文案（zh/en）。

## 4. 任务拆分

- [x] `TaskService.RunAsync` + RunSpec（底座 + 单测：成功/失败/instance_id 关联/超时默认）
- [x] provision 迁底座（provisionOnWorker 换 stage 回调；provision_fr319 测试不改仍绿；顺带修复 FR-319 时漏改的 provision_fr034 router 测试断言）
- [x] 导入 migrate 接入（in_place 仍同步；migrate 异步 + 端点 {instance,taskId} + 失败 statusReason）
- [x] 克隆接入（dryRun 不变；实拷异步 finishCloneWork + CloneResult taskId）
- [x] 备份创建/恢复接入（executeBackup/executeRestore 返 error+stage 包进 work，仍写 record status）
- [x] 新 TaskKind 常量（import/clone/backup_create/backup_restore）+ finalizeTerminal default 覆盖 + 前端 kind 筛选/文案
- [x] 前端 import 解包 {instance,taskId} + import/clone/backup mutation invalidate tasks
- [ ] 文档同步：API.md（import/clone 响应加 taskId）、ARCHITECTURE（tasks kind 扩充）、CHANGELOG、PRD 状态
- [ ] 真机：导入/克隆/备份 秒回 + 任务中心跟踪

## 5. 验收标准

- 四操作提交即使耗时数分钟也秒回、HTTP 不阻塞（bufconn/单测断言立即返回 taskId）。
- 任务中心可见、可筛（新 kind）、running 有 stage 文案、失败带错误链、终态发站内信。
- provision 迁底座后 `provision_fr319_test.go` 全绿（行为/响应/statusReason 不变）。
- **真机**：导入一个真实大目录（或克隆 bot-arena2）→ 秒回 → 任务中心跟到 succeeded → 产物正确落地；备份创建 → 任务中心见进度 → 备份列表 Status 同步 completed。
- 真机项需用户确认；单测全绿不替代。

## 6. 风险 / 待定

- 备份 record Status 与任务终态双写一致性：work 内先更 record 再返回，RunAsync 再 MarkSucceeded——两次写非原子，但 record 与 task 各自独立消费，短暂不一致无害（都最终一致）。
- migrate 真进度：worker `ImportServerDir` 一次 RPC 干完不回字节进度，V1 只能 stage 文案不确定态；真%需 worker 流式上报（列后续增强，不在本 FR）。
- 并发：RunAsync 每次 goroutine 独立，无共享可变态；uuid 生成用 `github.com/google/uuid`（服务上下文可用）。
