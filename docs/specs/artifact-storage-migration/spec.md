# 功能规格：制品库存量迁移工具（渠道间搬运 + 幂等续跑）

> 状态：已交付@v0.18.0　·　关联 PRD：FR-348（依赖 FR-347 底座）　·　分支：feature/fr-348-artifact-storage-migration　·　架构决策：ADR-073（已覆盖，无新增 ADR）

## 1. 背景与目标

FR-347 落成外置对象存储底座后，**新上传**的 client-file 制品按活跃渠道落点，但**存量**大制品仍躺在 CP 本地盘（`var/artifacts`）。用户目标：把已存本地的存量制品一键搬到 rustfs，彻底达成「CP 不存大对象」。

本 FR 建「存量迁移工具」：存储渠道页发起「迁移到渠道 X」后台任务——逐制品 源读取→sha256 复核→写入目标→更新记录→删源；进度接入全局任务中心；失败明细逐条落库可查；中断（CP 重启/强停）后重新发起同目标迁移即幂等续跑。**方向双向对称**（local→s3 / s3→local / s3→s3），回退也能用。

可安全增量推进的地基是 ADR-073 的「**位置由记录自述**」（`Asset.StorageBackend + StorageChannelID` 决定读取位置）：迁一条改一条，随时中断不破读取。

## 2. 需求（要什么）

- 渠道页每行「迁移到此渠道」入口（含内置本机存储行——回迁用）：确认模态 → POST 发起后台迁移任务。
- 仅迁 **client-file** 类型（与 FR-347 渠道路由范围一致；其余类型恒本地，无迁移语义）。
- 一次仅允许一个在途迁移任务，并发发起 **409**。
- 逐制品流程**顺序不可变**：源读取（sha256 复核）→ 写入目标 → **先改 Asset 记录** → **再删源对象/文件**。任一步失败该条记为失败、**不删源**、读取不受影响，继续下一条。
- 幂等续跑：中断后重新发起同目标迁移 = 续跑（已在目标渠道的记录直接跳过）。
- 失败明细逐条落库（制品 sha256 + 原因），前端可查、重试 = 重新发起。
- 进度接入全局任务中心（新 TaskKind），计数口径：总数 / 已迁 / 失败 / 跳过；渠道页显示在途进度与失败明细入口。
- 守卫：发起时对目标渠道真连探测，失败即拒（422）；迁移在途禁删任何存储渠道；活跃渠道切换不受迁移影响（写路径独立，天然成立）。

**不做（范围外）**：索引↔存储对账 / 残留清点（FR-349）；迁移限速/带宽控制；按制品子集筛选迁移（一律全量存量，已在目标的天然跳过）；定时/自动迁移；非 client-file 类型迁移。

## 3. 设计（怎么做）

### 3.1 任务形态：CP 侧后台 goroutine（复用 FR-323 任务底座）

迁移执行体在 CP 进程内（源=本地盘/S3、目标=本地盘/S3，均由 CP 直连，不涉 Worker）——沿 `TaskKindProvision/Import/Clone` 的 CP 直写形态：

- 新任务种类 `model.TaskKindArtifactMigrate = "artifact_migrate"`；`NodeID=0`（无节点归属）。
- 发起路径**手写任务生命周期**（`CreateTask → go run() → MarkRunning → SetStage → MarkSucceeded/Failed`）而非 `RunAsync`：发起时需要预知 taskID **同步**落迁移登记行（见 §3.2），避免「goroutine 已跑、登记行未建」的计数丢失窗口；RunAsync 的 taskID 在 goroutine 已 spawn 后才返回，不满足。
- 执行超时 12h（存量可达数百 GB，30min 默认远不够）；超时 → 任务 failed，重新发起续跑。
- **强停**：复用任务中心 `POST /tasks/:taskId/cancel`。NodeID=0 时 TaskService.Cancel 直接置 canceled 终态（无 Worker 可中断）；迁移循环**每条制品处理前**查询任务行（state=canceled 或 cancel_requested）即退出循环，不再动后续制品。
- **CP 重启孤儿**：goroutine 随进程死，任务行滞留 running。启动装配时 `RecoverOrphans()` 把非终态 `artifact_migrate` 任务批量置 failed（error=「主控重启导致迁移中断，重新发起同目标迁移即续跑」）——保证「DB 非终态 ⇔ 本进程内真在跑」的不变式，409 守卫与渠道删除守卫都建立在此不变式上。

### 3.2 数据模型（新表 ×2，`internal/controlplane/model/artifact_migration.go`）

```go
// ArtifactMigration 一次迁移任务的登记与实时计数（task_id 1:1 关联 tasks 行）。
// 计数不塞 Task.Result（失败任务无 result）也不解析 Detail 文本（Detail 被 SetStage 覆写），
// 独立行保证「总数/已迁/失败/跳过」四计数随迁移逐条持久推进、前端精确展示。
type ArtifactMigration struct {
    ID              uint   `gorm:"primaryKey" json:"id"`
    TaskID          string `gorm:"type:varchar(64);uniqueIndex;not null" json:"taskId"`
    TargetChannelID uint   `gorm:"not null;index" json:"targetChannelId"`  // 重试=对同目标重新发起的解析源
    Total           int    `gorm:"not null;default:0" json:"total"`     // 全部 client-file 存量数
    Migrated        int    `gorm:"not null;default:0" json:"migrated"`  // 本次任务实际搬运成功数
    Failed          int    `gorm:"not null;default:0" json:"failed"`
    Skipped         int    `gorm:"not null;default:0" json:"skipped"`   // 发起时已在目标渠道数（续跑的「已迁」体现于此）
    CreatedAt       time.Time `json:"createdAt"`
    UpdatedAt       time.Time `json:"updatedAt"`
}

// ArtifactMigrationFailure 迁移失败明细（逐条落库可查，FR-348 幂等续跑的失败面）。
type ArtifactMigrationFailure struct {
    ID        uint   `gorm:"primaryKey" json:"id"`
    TaskID    string `gorm:"type:varchar(64);not null;index" json:"taskId"`
    AssetID   uint   `json:"assetId"`
    SHA256    string `gorm:"type:char(64);not null" json:"sha256"`
    Filename  string `gorm:"type:varchar(255)" json:"filename"`
    Size      int64  `json:"size"`
    Reason    string `gorm:"type:text" json:"reason"`
    CreatedAt time.Time `json:"createdAt"`
}
```

两表只增不改既有表；AutoMigrate 追加注册。失败行按 task_id 隔离（每次任务独立一批，历史留存与 tasks 表同口径）。

### 3.3 迁移服务（新 `service.ArtifactMigrationService`）

```go
type ArtifactMigrationService struct {
    db       *gorm.DB
    root     *dataroot.Root                  // 中转临时文件落 cache/（同 Ingest）
    channels *ArtifactStorageChannelService  // StoreFor/StoreForAsset/TestSaved 复用
    tasks    *TaskService                    // 任务生命周期
    mu       sync.Mutex                      // 发起临界区（与 DB 在途检查合成防并发）
}
```

**`Start(targetID, createdBy uint) (taskID string, err error)`**（mu 全程持锁）：

1. 目标渠道存在性 → `ErrArtifactStorageNotFound`（404）。
2. 在途检查：`tasks` 表 `kind=artifact_migrate AND state IN (pending,running)` 计数 >0 → `ErrArtifactMigrationInFlight`（409）。启动孤儿清扫（§3.1）保证该查询即真相。
3. 目标真连探测：复用 `channels.TestSaved(targetID)`（s3=写探测对象、local=数据根可写；顺带刷新渠道 LastTest*）；`!ok` → `ErrArtifactMigrationTargetUnavailable`（422，带探测 message）。
4. 目标 BlobStore 解析一次（`channels.StoreFor`，凭证解密失败在此快失败 422）；在途期间编辑目标渠道不影响已解析连接（下一次发起才生效）。
5. `CreateTask(taskID=uuid, nodeID=0, kind, title="制品存量迁移 → <渠道名>", createdBy)` + **同步建 `ArtifactMigration` 登记行** → `go run(...)` → 返回 taskID。

**`run(taskID, target, targetStore)`**（后台 goroutine，ctx 超时 12h）：

1. `MarkRunning`；快照 `SELECT * FROM assets WHERE type='client-file' ORDER BY id ASC`（发起后新上传不进本次快照——按活跃渠道已落对位置，无需迁移语义）。
2. 分拣：**已在目标**的计 skipped——目标=local 时 `StorageBackend=local`；目标=s3 渠道 X 时 `StorageBackend=s3 AND StorageChannelID=X.ID`。登记行落 total/skipped。
3. 逐条循环（每条前查任务行，canceled/cancelRequested → 停止）：`migrateOne` 成功→Migrated++、失败→Failed++ + 落失败行；登记行计数逐条持久化；整数进度百分比变化时 `SetStage`（TaskLog 留阶段轨迹，不逐条刷日志防万条膨胀）。
4. 收尾：`failed==0` → `MarkSucceeded(result JSON {total,migrated,failed,skipped})`；`failed>0` → `MarkFailed("N 条制品迁移失败，详见失败明细；重新发起同目标迁移可重试")`——迁移未达成目标即失败语义，重试=重新发起（成功条已自述新位置，续跑自动跳过）。

**`migrateOne(ctx, asset, target, targetStore)`**（顺序不可变，任一步 err = 该条失败、不删源）：

```
1. src := channels.StoreForAsset(&asset)        // 源由记录自述（渠道删守卫保证可解析）
2. rc := src.Open(ctx, asset.RelPath)           // 缺失 → 「源对象不存在」（FR-349 域的既有损伤，此处只如实上报）
3. io.Copy → cache/ 临时文件，边拷边算 sha256
   size ≠ asset.Size 或 sha256 ≠ asset.SHA256 → 「源内容校验不符」失败（防搬运損毁内容）
4. targetStore.PutFile(ctx, asset.RelPath, tmp, size)   // 键=CAS 相对路径不变（跨后端存储键，ADR-073）
5. UPDATE assets SET storage_backend/storage_channel_id/storage_state
   WHERE id=? AND storage_backend=<读取时值> AND storage_channel_id=<读取时值>   // 乐观守卫
   RowsAffected==0 →「记录已变更或已删除」失败（并发删制品窗口；目标已传对象不回收——内容寻址残留无害，FR-349 对账收口）
6. src.Delete(ctx, asset.RelPath)               // 删源。失败仅记任务日志警告，不判该条失败——
                                                // 记录已指向新位置读取正确，残留归 FR-349
```

- 记录字段语义按 FR-347 spec §3.2/§3.4：目标=local → `{backend:local, channelID:0, state:hot}`（与历史 local 行完全同形）；目标=s3 → `{backend:s3, channelID:目标ID, state:external}`；`RelPath` 恒不变。
- **删源防呆**：源与目标均为 s3 且 endpoint/bucket/prefix 三元组相同（两渠道误配同一物理桶）时**跳过删源**——同键同物理位置，删源即删刚写的目标对象。
- 步骤 5 之后目标不回滚（见上），步骤 5 之前失败时已传目标对象同样不回收：CAS 键内容确定，重传窗口最多在途一条且覆盖写等价；主动回收反而在「双渠道同桶」误配下有删源风险。

### 3.4 守卫

- **渠道删除**：`ArtifactStorageChannelService.Delete` 增一道检查——存在非终态 `artifact_migrate` 任务 → `ErrArtifactStorageMigrationInFlight`（422）。粗粒度（在途期间**所有**渠道禁删）：源集合由逐条记录自述、随迁移动态收敛，静态判定「此渠道无关」不可靠；渠道删除本就是低频运维动作，等迁移完（或强停）再删无伤。既有守卫（内置/活跃/被制品引用）原样在前。
- **活跃渠道切换**：不受迁移影响、迁移也不影响它（写路径读活跃渠道、迁移读记录自述，互不依赖）——无需代码，spec 声明即可。
- **发起期编辑渠道**：不禁止。目标连接在发起时解析（§3.3），源连接逐条现解析（编辑源渠道凭证若致解密/连接失败，仅该条失败，符合逐条失败面语义）。

### 3.5 API（gate-api 完整定义）

全部挂 admin 组（JWT + `RequireRole(RolePlatformAdmin)`），前缀 `/api/v1`。

| Endpoint | 方法 | 请求体 | 成功响应 | 错误 | FR |
|---|---|---|---|---|---|
| `/artifact-storages/:id/migrate` | POST | —（目标=路径 :id） | **202** `{taskId}` | 404 `NOT_FOUND`（渠道不存在）；**409 `MIGRATION_IN_FLIGHT`**（已有在途迁移）；422 `BUSINESS_ERROR`（目标探测失败/凭证解密失败） | FR-348 |
| `/artifact-storages/migration` | GET | — | 200 `{task: Task\|null, migration: ArtifactMigrationInfo\|null}`（最近一次迁移，含实时计数；从未迁移过则双 null） | 500 `INTERNAL_ERROR` | FR-348 |
| `/artifact-storages/migration/:taskId/failures` | GET | — | 200 `ArtifactMigrationFailure[]`（按 id 升序，**上限 500 条**；总失败数看计数行 failed） | 500 | FR-348 |
| `DELETE /artifact-storages/:id`（既有，扩展） | DELETE | — | 200 不变 | 新增 422 `BUSINESS_ERROR`（存量迁移在途，禁止删除存储渠道） | FR-348 |
| `POST /tasks/:taskId/cancel`（既有，复用） | POST | — | 200 不变（NodeID=0 直接置 canceled；迁移循环逐条检查退出） | 不变 | FR-348 |

`Task` 结构即任务中心既有形态（`kind="artifact_migrate"`）。TypeScript 类型（前端 `src/api/artifactStorages.ts` 与 devmock contracts 同源）：

```ts
interface ArtifactMigrationInfo {
  taskId: string
  targetChannelId: number
  /** 目标渠道当前名称（迁移后渠道被删时为空串）。 */
  targetName: string
  total: number; migrated: number; failed: number; skipped: number
}
interface ArtifactMigrationStatus {
  task: Task | null            // api/tasks.ts 的 Task
  migration: ArtifactMigrationInfo | null
}
interface ArtifactMigrationFailure {
  id: number; taskId: string; assetId: number
  sha256: string; filename: string; size: number
  reason: string; createdAt: string
}
```

**任务状态机**（复用 `model.TaskState`）：

```
pending ──MarkRunning──▶ running ──全部成功──▶ succeeded（result=计数 JSON）
                            │──存在失败条──▶ failed（error=「N 条失败…重试=重新发起」）
                            │──强停/超时──▶ canceled / failed
（CP 重启）running 滞留 ──启动 RecoverOrphans──▶ failed（重新发起即续跑）
任何终态后再 POST migrate = 新任务新 taskID（续跑语义由记录自述天然达成，无「恢复旧任务」概念）
```

### 3.6 前端（渠道页扩展 + 任务中心登记）

- **迁移入口**：渠道表每行操作区加「迁移到此」（ghost 按钮，含内置行；在途迁移存在时禁用）。点击弹确认模态（`Dialog sm:max-w-md`，确认类非表单，ui-modals 合规）：说明「全部存量 client-file 逐条搬运→校验→更新记录→删源；已在该渠道的自动跳过；可随时强停，重新发起即续跑」。确认 → POST；409/422 用后端 message toast。
- **在途进度卡**：`GET /artifact-storages/migration` 轮询（任务非终态时 2s，复用 `tasksRefetchInterval` 启停规则；终态停轮询）。任务非终态时表格上方渲染进度卡：目标名、进度条（task.progress）、计数行（共 N · 已迁 X · 失败 Y · 跳过 Z）、「强制停止」按钮（`useCancelTask`）。
- **上次迁移摘要**：最近任务终态时显示一行摘要（状态 + 计数）；`failed>0` 时给「失败明细」按钮 → 模态列失败行（文件名/sha256 缩写/大小/原因），footer「重新发起」（对同 targetChannelId 再 POST，即重试）。
- **任务中心**：`TASK_KIND_LABEL_KEYS` 加 `artifact_migrate` → `tasks.kind.artifactMigrate`（zh「制品存量迁移」/ en「Artifact migration」）；任务在任务中心与页眉任务下拉自然可见可停。
- i18n 仅加自己的 key（`artifactStorages.migrate.*` + `tasks.kind.artifactMigrate`，zh/en 成对）；devmock 在 `packages/devmock/src/handlers/domains/artifact-storage.ts` 内扩展迁移 handler（POST 发起/在途 409/GET 状态推进模拟/失败明细）+ contracts 类型。
- **冲突面自律**：不动 `RuntimeAssetsPage.tsx`、不动对账命名空间（FR-349 地盘）。

### 3.7 装配（main.go / router.go）

- main：`NewArtifactMigrationService(db, root, artifactStorageSvc, taskSvc)` → `RecoverOrphans()`（启动孤儿清扫）→ 注入渠道服务删除守卫无需注入（渠道服务直查 tasks 表）→ Services 挂新字段。
- router：新 `ArtifactMigrationHandler` 注册于 admin 组（与渠道路由同处）；`svcs.ArtifactMigration != nil` 才挂。
- gin 路由树核对：POST 树 `:id/migrate` 挂既有 `:id` 参数节点下；GET 树新增静态 `migration`（GET 树无 `:id` 兄弟，无冲突）。

## 4. 任务拆分

- [ ] spec（本文档）+ gate-api 自审
- [ ] Go 测试先行：迁移服务测试（local→s3 全量/顺序断言、单条失败不删源+续跑跳过、sha256 不符、并发 409、探测失败拒、s3→local 回迁、计数正确、孤儿清扫、渠道删除守卫）
- [ ] Go：model ×2 + AutoMigrate + ArtifactMigrationService + 渠道 Delete 守卫 + TaskKind
- [ ] Go：router + 装配（main.go/router.go）
- [ ] Web：api hooks + 渠道页迁移入口/进度卡/失败明细 + tasks kind 登记 + i18n + DOM 测试
- [ ] devmock 契约同步
- [ ] 文档同步：API.md、ARCHITECTURE.md 数据模型与外置存储节（PRD/CHANGELOG 由主会话统一处理）

## 5. 验收标准

- [ ] `go build ./... && go vet ./...` 过；`go test ./internal/controlplane/...` 全绿（预存失败 TestBotStressSession_StartCreatesAssociatedBots 除外）。
- [ ] local→s3：逐条成功后记录 `{s3, 渠道ID, external}`、对象在目标（bucket/prefix/relPath）、本地 CAS 文件已删；计数 total=migrated；任务 succeeded。
- [ ] 顺序保证：单条失败（目标 PUT 拒）时该条记录不变、源文件仍在、失败行含 sha256+原因；其余条正常迁；任务 failed 带计数。重新发起：已迁条跳过（skipped 计入、不重传）、失败条重试成功 → succeeded。
- [ ] sha256 不符（源文件被篡改）→ 该条失败「校验不符」、不删源、记录不变。
- [ ] 并发发起 → 第二个 409；孤儿任务（DB running 但进程重启）→ RecoverOrphans 置 failed 后可重新发起。
- [ ] 目标渠道探测失败（endpoint 不可达）→ 发起被拒 422，不建任务。
- [ ] s3→local 回迁：记录回 `{local, 0, hot}`、CAS 文件就位、S3 对象删除。
- [ ] 迁移在途 DELETE 渠道 → 422；终态后恢复可删（其余守卫不变）。
- [ ] 前端 vitest：迁移入口+确认模态发起、在途进度卡计数展示、失败明细模态+重新发起、409 toast；`tsc -b --noEmit` + lint 过（预存失败 ui-package-boundary.test.ts 除外）。
- [ ] **真机（需用户确认）**：存量若干制品在本地 → 渠道页对 rustfs 渠道点「迁移到此」→ 进度卡推进 → 完成后 rustfs 桶内对象齐全、本地 CAS 已清、玩家端点下载 302 到 rustfs；中途强停再发起续跑不重传已迁条；回迁到本机存储同样闭环。

## 6. 风险 / 待定

- **双渠道误配同一物理桶**：删源防呆（§3.3）跳过删源避免删目标；残留与孤儿对象一律归 FR-349 对账。
- **超大存量超 12h**：任务超时 failed，重新发起续跑（已迁条跳过），多轮可完成；限速/断点续传单对象不做（单对象 GB 级在内网可承受）。
- **迁移中源渠道凭证失效/编辑坏**：仅相应条失败并入明细，修复后重新发起重试。
- **失败明细上限 500 条展示**：极端大规模失败时明细截断（总数看计数行）；根因通常单一（渠道级故障），前 500 条足以定位。
