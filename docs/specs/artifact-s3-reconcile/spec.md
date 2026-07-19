# 功能规格：制品索引可视化与 S3 一致性对账

> 状态：已交付@v0.18.0　·　关联 PRD：FR-349（依赖 FR-347 外置存储底座）　·　分支：feature/fr-349-artifact-s3-reconcile　·　架构决策：ADR-073（已覆盖，本 spec 不另立 ADR）

## 1. 背景与目标

FR-347 把 client-file 制品外置到 S3 兼容对象存储后，「Asset 索引」与「bucket 里实际对象」成为两份可能漂移的真相：对象被人在存储侧误删 → 索引仍在、下载 302 必 404（高危）；上传成功但记录回滚残留、或他工具误写 → 对象在、索引无（白占存储）。ADR-073 后果节已把「补齐 S3 侧观测」归入本 FR 对账域。

本 FR 建「对账 + 显式处置」闭环：制品列表可视化存储位置与对账状态；对账任务（手动 + 定期）比对索引与对象清单产出差异报告落库；处置走显式按钮——缺失「标记失效」、孤儿「二次确认一键清理」。**不做全自动修复**（用户拍板：任何删对象/改索引状态的动作必须人点按钮）。

## 2. 需求（要什么）

- 制品列表（RuntimeAssetsPage client-file 段）加「存储位置」（本机 / 渠道名）与「对账状态」（正常 / 失效）列；失效红标。
- 对账任务：逐 s3 渠道比对「该渠道 Asset 索引的对象键集合」vs「渠道 prefix 下实际对象清单」；产出缺失（索引有 S3 无）/ 孤儿（S3 有索引无）差异明细，随对账运行记录落库；面板可查最近 N 次。
- 触发：手动按钮（单渠道或全局）+ 定期（默认每日、周期可配、可关）；同渠道在途去重（单渠道并发触发 409，全局触发跳过在途渠道）。
- 处置（显式按钮）：
  - 缺失 → 「标记失效」：`Asset.StorageState` 置 `lost`，制品列表红标，下载端点对失效制品返回明确错误（不再 302 到必 404 的预签名 URL）；提示运营**重传同内容文件即自愈**（CAS 去重命中 + 对象补传，见 §3.6 恢复语义）。
  - 孤儿 → 报告内列表展示（键 / 大小 / 最后修改）+ 二次确认（DangerConfirm）一键清理（删 S3 对象）。
- 清理孤儿 / 标记失效 / 触发对账 / 改定期设置 走既有审计日志（`AuditService`，沿 runtime_assets.refresh 先例）。
- BlobStore 扩展分页遍历（S3 ListObjectsV2 continuation-token），大 bucket 可全量遍历。
- devmock 契约同步（**独立 handler 文件** `artifact-reconcile.ts`，不改 348 在扩的 artifact-storage.ts）+ 中英 i18n + docs/API.md。

**不做（范围外）**：全自动修复（自动删孤儿/自动标失效/自动重传）；local 渠道对账（本地文件系统由 CAS 自管，缺失即 404 已有明确语义）；对象内容级校验（逐对象拉回验 sha——流量成本不可接受，尺寸不一致检测亦不做）；存量迁移（FR-348）；渠道页改动（348 冲突面，入口放制品页）。

## 3. 设计（怎么做）

### 3.1 对账范围与差异模型

- **对账单位 = 一个 s3 渠道**。内置 local 渠道不参与（触发 local → 422）。
- **索引集合**：`assets` 表 `type=client-file AND storage_backend=s3 AND storage_channel_id=<渠道>` 的 `rel_path`（= 跨后端存储键，ADR-073）。
- **对象集合**：渠道 BlobStore 以键前缀 `var/artifacts/client-file/` 分页全量遍历（BlobStore 视角键已剥渠道 Prefix）。**命名空间外的对象一概不管**：渠道 prefix 外（共 bucket 他方对象）天然不可见；prefix 内但 CAS client-file 命名空间外（如连通测试的 `probe/` 探测残留、他类型目录）不参与比对、不算孤儿。
- **差异分类**：
  - `missing` 缺失：索引有键、对象集合无 → 下载必 404 的高危项。**已标 `lost` 的资产不再重复报缺失**（已处置过、列表红标持续可见），但其键仍参与孤儿排除（防误删）。
  - `orphan` 孤儿：对象集合有键、索引（该渠道**全部** s3 资产，含 lost）无 → 白占存储。
- **一致**：两侧键均在。仅计数（`matched`），不落明细。

### 3.2 数据模型（新表，AutoMigrate 注册）

```go
// ArtifactReconcileRun 一次对账运行（对账运行记录，面板查最近 N 次）。
type ArtifactReconcileRun struct {
    ID          uint   `gorm:"primaryKey"`
    ChannelID   uint   `gorm:"not null;index"`
    ChannelName string `gorm:"type:varchar(128)"` // 快照（渠道后续改名/删除不影响历史报告可读）
    Status      string `gorm:"type:varchar(16);not null"` // running | succeeded | failed
    TriggeredBy string `gorm:"type:varchar(16);not null"` // manual | scheduled
    StartedAt   time.Time
    FinishedAt  *time.Time
    IndexCount   int    // 索引侧扫描条数（该渠道 s3 资产数，含 lost）
    ObjectCount  int    // 对象侧扫描条数（CAS client-file 命名空间内）
    MatchedCount int    // 两侧一致数
    MissingCount int
    OrphanCount  int
    ErrorMessage string `gorm:"type:varchar(512)"` // failed 时的原因
    CreatedAt    time.Time
}

// ArtifactReconcileDiff 差异明细（缺失/孤儿），随 run 落库、处置后翻 resolved。
type ArtifactReconcileDiff struct {
    ID        uint   `gorm:"primaryKey"`
    RunID     uint   `gorm:"not null;index"`
    ChannelID uint   `gorm:"not null;index"`
    Kind      string `gorm:"type:varchar(16);not null;index"` // missing | orphan
    AssetID   uint   // missing：资产 ID；orphan：0
    SHA256    string `gorm:"type:char(64)"`  // missing：资产 sha256；orphan 空
    ObjectKey string `gorm:"type:varchar(512);not null"` // CAS 相对键（missing=RelPath；orphan=剥渠道前缀后的对象键）
    Size      int64
    LastModified *time.Time // orphan：对象 Last-Modified；missing nil
    Status         string `gorm:"type:varchar(16);not null;default:open"` // open | resolved
    ResolvedAt     *time.Time
    ResolvedAction string `gorm:"type:varchar(32)"` // marked_lost | cleaned | stale（差异已过时，见 §3.5 守卫）
    ResolveError   string `gorm:"type:varchar(512)"` // 清理失败原因（保留 open 供重试）
    CreatedAt      time.Time
}

// ArtifactReconcileSetting 定期对账设置（单行 id=1，firstOrCreate 兜底）。
type ArtifactReconcileSetting struct {
    ID            uint `gorm:"primaryKey"` // 恒 1
    Enabled       bool `gorm:"not null;default:true"`
    IntervalHours int  `gorm:"not null;default:24"` // 钳制 [1,720]，默认每日
    NextRunAt     *time.Time // 下次定期触发时间；禁用时 nil
    UpdatedAt     time.Time
}
```

`model.Asset` 增状态常量 `AssetStorageLost AssetStorageState = "lost"`（失效：索引在、外置对象缺失，下载不可用；由「标记失效」处置写入，重传同内容自愈清除）。不加新列——`StorageState` 字段既有（FR-347 基线预留「标记失效语义可用它承载」）。

### 3.3 BlobStore 分页遍历（接口扩展）

`Store` 接口新增（FR-347 的 `List` 保留不动，连通探测用；对账用分页全量遍历）：

```go
// ListPage 分页枚举 prefix 下的 blob（对账全量遍历用，FR-349）。
// token 传上一页返回的续传令牌（首页传空）；nextToken 非空表示还有后续页。
// limit<=0 取 1000。令牌对调用方不透明（s3=ListObjectsV2 continuation-token；local=游标键）。
ListPage(ctx context.Context, prefix string, limit int, token string) (items []ObjectInfo, nextToken string, err error)
```

- s3 适配器：ListObjectsV2 加 `continuation-token`；响应解析 `IsTruncated`/`NextContinuationToken`（`listBucketResult` 补两字段）。
- local 适配器：WalkDir 全收集 → 键排序 → 从 `token`（上一页末键，start-after 语义）之后取 `limit` 个；还有剩余则 nextToken=本页末键。local 渠道不参与对账，此实现仅为接口完备与测试。

### 3.4 对账服务（`service.ArtifactReconcileService`，新文件 artifact_reconcile.go）

```go
type ArtifactReconcileService struct {
    db       *gorm.DB
    channels *ArtifactStorageChannelService
    audit    *AuditService // 可 nil（沿既有约定，nil 时审计静默跳过）
    mu       sync.Mutex
    inflight map[uint]bool // channelID → 对账在途（进程内去重；CP 单进程）
    // storeFor 渠道→BlobStore 解析（默认 channels.StoreFor；测试注入假 store）。
    storeFor func(*model.ArtifactStorageChannel) (blobstore.Store, error)
    pageSize int          // ListPage 页大小，默认 1000（测试注小值验分页）
    now      func() time.Time
    stopCh   chan struct{}
    running  bool
}
```

- **触发**：`Trigger(channelID, triggeredBy)` → 校验渠道存在且 type=s3；`beginRun`（互斥登记 inflight + 建 `running` 运行行）；goroutine 执行 `executeRun`；即刻返回 run（异步，前端轮询 runs 列表）。在途重复触发 → `ErrReconcileInProgress`（409）。`TriggerAll(triggeredBy)` 遍历全部 s3 渠道逐个 Trigger，在途的**跳过**并回报（`skipped[]`），无 s3 渠道 → `ErrReconcileNoChannel`（422）。测试用同步入口 `ReconcileSync`（同 beginRun+executeRun，串行返回终态 run）。
- **执行**（executeRun，defer 释放 inflight）：
  1. 索引侧：查该渠道全部 s3 client-file 资产 → `keyed map[relPath]asset`；
  2. 对象侧：`store.ListPage(ctx, "var/artifacts/client-file/", pageSize, token)` 循环至 nextToken 空；每对象：键在索引 → matched（若资产非 lost）；不在 → 孤儿明细；
  3. 缺失：索引中**非 lost** 资产的键未在对象集合出现 → 缺失明细；
  4. 终态：一个事务写差异明细 + 更新运行行（counts/succeeded/FinishedAt）。store 报错 → 运行行置 failed + ErrorMessage（不写半截明细）。
- **启动清障**：`Start()` 先把库里遗留 `running` 的运行行置 failed（"CP 重启中断"）——防 CP 崩溃留假在途。
- **定期调度**（形态沿 Scheduler/MetricService 平台级后台 goroutine 先例，每分钟 tick）：`checkScheduled(now)` —— 设置行 `Enabled=false` 直接返回；`NextRunAt` 为空 → 置 `now+interval`（**首个周期从启用/启动时刻起算，不在启动瞬间扫存储**）；`now >= NextRunAt` → `TriggerAll("scheduled")` 并推进 `NextRunAt=now+interval`。
- **设置**：`Settings()` firstOrCreate 单行；`UpdateSettings(enabled, intervalHours)` interval 钳 [1,720]、越界 `ErrReconcileInvalidInterval`（422）；变更即重算 NextRunAt（enabled→now+interval；disabled→nil）。
- **查询**：`ListRuns(channelID, limit)`（id desc，limit 默认 20 上限 100）；`GetRun(id)`；`ListDiffs(runID, kind, page, pageSize)`（分页，pageSize 默认 50 上限 200）。
- **处置**（仅对 `succeeded` 的 run；running → `ErrReconcileRunRunning` 409）：
  - `ResolveMissing(runID)`：run 内全部 open+missing 明细 → 逐条：资产仍存在且仍 `s3+同渠道+同键` → `StorageState=lost`，明细 resolved(`marked_lost`)；资产已删/已迁走 → 明细 resolved(`stale`)（差异已过时，不动资产）。返回 `{marked, stale}`。
  - `CleanupOrphans(runID)`：run 内全部 open+orphan 明细 → 逐条**过时守卫**：当前索引已有该渠道同键资产（run 之后新上传同内容——对象已被合法引用）→ resolved(`stale`) **不删**；否则 `store.Delete(key)` → 成功 resolved(`cleaned`)、失败保持 open 并记 ResolveError（可重试）。返回 `{cleaned, stale, failed}`。

### 3.5 处置安全性（为什么这样做）

- 孤儿清理的**过时守卫**是硬要求：报告是快照，run 到点击按钮之间可能有新上传（CAS 同键）——不加守卫会把刚上传的合法对象删掉。守卫按「当前索引同渠道同键」现查现判。
- 缺失标记的对称守卫：资产在 run 后被删除/被 348 迁移改自述 → 不再标 lost（stale）。
- lost 资产的键**始终参与孤儿排除**：若某资产被误标 lost 而对象其实在（或运维在存储侧手工恢复了对象），其对象绝不能被当孤儿清掉。
- 对账本身零写入（只读索引 + 只读 List），全部变更集中在两个处置端点 + 审计留痕。

### 3.6 失效制品的下载语义与自愈恢复

- **玩家消费端点 `GET /client-artifacts/:sha256`**：鉴权后、后端分流前判 `StorageState=lost` → **410 Gone** `{error:"ARTIFACT_LOST", message:"制品外置对象已缺失，请联系运营重传"}`（替代 302 → 预签名 404 的哑失败；4xx 对 updater 是明确终态错误非重试）。追踪事件照记（errCode=ARTIFACT_LOST）。
- **管理面下载 `GET /client-channels/:id/files/download` 与文本预览 `GET /client-channels/artifact-content`**：同判 → 410 `ARTIFACT_LOST`（前端可给「重传自愈」提示文案）。
- **自愈（重传同内容）**：`AssetService.Ingest` 去重命中分支扩展——命中记录 `StorageState=lost && StorageBackend=s3` 时，经 `StoreForAsset` 把临时文件 `PutFile` 补回**记录自述的渠道**（不是当前活跃渠道——位置由记录自述，ADR-073），成功后 `StorageState` 复位 `external`；补传失败 → Ingest 报错（快失败，沿 ADR-073 决策 5）。恢复闭环：报缺失 → 标失效 → 运营重传同文件 → 去重命中 + 对象补传 + 失效清除 → 列表复绿、下载复通。发布/分块/聚合上传全汇于 Ingest，自愈全入口覆盖。
- lost 不参与 hot/archived/external 三态计数语义：overview 分组/汇总加性新增 `lostCount`。

### 3.7 API（gate-api 完整定义）

全部挂 admin 组（JWT + `RequireRole(RolePlatformAdmin)`），前缀 `/api/v1`。错误码：400 `INVALID_REQUEST` / 404 `NOT_FOUND` / 409 `RECONCILE_IN_PROGRESS` / 422 `BUSINESS_ERROR` / 500 `INTERNAL_ERROR`。

| Endpoint | 方法 | 请求体/参数 | 成功响应 | 错误 | FR |
|---|---|---|---|---|---|
| `/artifact-reconcile/settings` | GET | — | 200 `{enabled, intervalHours, nextRunAt?}` | 500 | FR-349 |
| `/artifact-reconcile/settings` | PUT | `{enabled*, intervalHours*}` | 200 同上（审计 artifact_reconcile.settings_update） | 400；422（interval 越界 [1,720]） | FR-349 |
| `/artifact-reconcile/runs` | POST | `{channelId?}`（>0 单渠道；缺省/0 全部 s3 渠道） | 202 `{started: Run[], skipped: [{channelId, channelName, reason}]}`（审计 artifact_reconcile.trigger） | 404（渠道不存在）；409 RECONCILE_IN_PROGRESS（单渠道在途）；422（local 渠道 / 无 s3 渠道） | FR-349 |
| `/artifact-reconcile/runs` | GET | `?channelId=&limit=`（limit 默认 20 上限 100） | 200 `Run[]`（id desc） | 500 | FR-349 |
| `/artifact-reconcile/runs/:id` | GET | — | 200 `Run` | 404 | FR-349 |
| `/artifact-reconcile/runs/:id/diffs` | GET | `?kind=missing\|orphan&page=&pageSize=`（pageSize 默认 50 上限 200） | 200 `{items: Diff[], total, page, pageSize}` | 400（kind 非法）；404 | FR-349 |
| `/artifact-reconcile/runs/:id/resolve-missing` | POST | — | 200 `{marked, stale}`（审计 artifact_reconcile.mark_lost） | 404；409（run 运行中） | FR-349 |
| `/artifact-reconcile/runs/:id/cleanup-orphans` | POST | — | 200 `{cleaned, stale, failed}`（审计 artifact_reconcile.cleanup_orphans） | 404；409（run 运行中） | FR-349 |
| `GET /runtime-assets/overview`（既有） | GET | — | 200 加性新增：`artifactChannels: [{id,name,type}]`；组/汇总加 `lostCount`；asset 行沿 model 序列化含 `storageChannelId`/`storageState:"lost"` | 不变 | FR-349 |
| `GET /client-artifacts/:sha256`（既有，玩家 key） | GET | — | 不变 | **410 `ARTIFACT_LOST`**（StorageState=lost，先于后端分流） | FR-349 |
| `GET /client-channels/:id/files/download`、`GET /client-channels/artifact-content`（既有，JWT） | GET | — | 不变 | **410 `ARTIFACT_LOST`** | FR-349 |

TypeScript 类型（`web/src/api/artifactReconcile.ts` 与 devmock contracts 同源）：

```ts
interface ArtifactReconcileRun {
  id: number; channelId: number; channelName: string
  status: 'running' | 'succeeded' | 'failed'
  triggeredBy: 'manual' | 'scheduled'
  startedAt: string; finishedAt?: string
  indexCount: number; objectCount: number; matchedCount: number
  missingCount: number; orphanCount: number
  errorMessage: string
}
interface ArtifactReconcileDiff {
  id: number; runId: number; channelId: number
  kind: 'missing' | 'orphan'
  assetId: number; sha256: string; objectKey: string; size: number
  lastModified?: string
  status: 'open' | 'resolved'
  resolvedAt?: string; resolvedAction: string; resolveError: string
}
interface ArtifactReconcileSettings { enabled: boolean; intervalHours: number; nextRunAt?: string }
interface TriggerReconcileResult {
  started: ArtifactReconcileRun[]
  skipped: Array<{ channelId: number; channelName: string; reason: string }>
}
```

### 3.8 前端（IA：入口放制品页，避开 348 的渠道页）

- **RuntimeAssetsPage client-file 段**：仅该组的表格追加两列——「存储位置」（`storageBackend==='s3'` → overview `artifactChannels` 映射渠道名，缺失回退 `渠道#id`；否则「本机」）与「对账状态」（`storageState==='lost'` → 红色「失效」徽章 + title 提示重传自愈；否则「正常」）。既有「存储」列的状态文案补 `lost → 失效`（danger 样式）。
- **对账区**（新组件文件 `src/pages/ArtifactReconcileSection.tsx`，RuntimeAssetsPage 制品区之后渲染，不动渠道页）：
  - 头部：标题 + 「对账设置」按钮（Dialog 模态：启用开关 + 周期小时数，ui-modals 纪律）+ 「立即对账」按钮（全局触发，toast 回报 started/skipped）。
  - 运行记录表：渠道 / 状态徽章 / 触发方式 / 开始时间 / 索引数 / 对象数 / 缺失 / 孤儿 / 「查看报告」。有 running 行时 5s 轮询刷新。
  - 报告 Dialog（`scrollableDialogContentClass` + `ScrollableDialogBody`）：运行摘要 + 缺失明细（名称链 sha / 键 / 大小 / 处置态）+ 孤儿明细（键 / 大小 / 最后修改 / 处置态），各自分页（上一页/下一页）；操作按钮「全部标记失效」「清理全部孤儿」均 DangerConfirm 二次确认，仅有 open 明细时可用。
  - 无 s3 渠道时显示灰态提示（不隐藏区块——可发现性）。
- **api client** `src/api/artifactReconcile.ts`：useReconcileSettings / useUpdateReconcileSettings / useTriggerReconcile / useReconcileRuns / useReconcileDiffs / useResolveMissing / useCleanupOrphans。`runtimeAssets.ts` 的 `AssetInfo` 补 `storageChannelId` 与 `'lost'` 状态、overview 补 `artifactChannels`、组/汇总补 `lostCount`。
- **devmock**：contracts.ts 追加上述类型；新独立域文件 `handlers/domains/artifact-reconcile.ts`（runs/diffs/settings 集合 + 全部端点，seed 一次含缺失+孤儿的完成 run）；node.ts overview 加性补 `artifactChannels` + `lostCount` + 一条 lost 的 s3 资产 seed（列渲染测试用）。
- **i18n**：zh/en 成对新增 `runtimeAssets.storageLocation/storageLocal/storageLost/reconcileState/reconcileOk/reconcileLostHint` 与 `artifactReconcile.*`。

### 3.9 装配

- `database.AutoMigrate` 注册三新表。
- `main.go`：`NewArtifactReconcileService(db, artifactStorageSvc)` + `SetAudit(auditSvc)` → `Start()`/`defer Stop()`；注入 router Deps。
- `router.go`：Deps 加 `ArtifactReconcile`，非 nil 时挂 admin 组。

## 4. 任务拆分

- [x] spec（本文档，过 gate-api 自审）
- [x] Go：blobstore ListPage（local/s3 + fake 服务器续传测试）
- [x] Go：model 三表 + AutoMigrate + AssetStorageLost
- [x] Go：ArtifactReconcileService（执行/调度/处置/守卫）+ 测试
- [x] Go：router + 装配 + Ingest 自愈 + 下载 410 + overview 加性 + 测试
- [x] Web：api client + 列表两列 + 对账区组件 + i18n + devmock + DOM 测试
- [x] 文档同步：API.md、ARCHITECTURE.md（PRD/CHANGELOG 由主会话统一处理）

## 5. 验收标准

- [ ] `go build ./... && go vet ./...` 过；`go test ./internal/controlplane/...` 全绿（预存失败 TestBotStressSession_StartCreatesAssociatedBots 除外）。
- [ ] 对账三态：一致仅计数；缺失/孤儿各产明细且计数正确；lost 资产不重复报缺失但仍排除孤儿。
- [ ] 分页遍历：pageSize 小于对象数时跨页结果完整（fake S3 continuation-token 续传 + 服务层循环）。
- [ ] 前缀隔离：渠道 prefix 下 CAS 命名空间外对象（probe/ 等）不算孤儿。
- [ ] 标记失效：missing 明细 resolved、资产 lost；玩家端点 410 ARTIFACT_LOST（不 302）；管理面下载/预览 410。
- [ ] 孤儿清理：store.Delete 逐键调用、明细 resolved(cleaned)；过时守卫命中 stale 不删。
- [ ] 自愈：lost 资产重传同内容 → 对象补回记录自述渠道、StorageState 复位 external。
- [ ] 定期：设置行默认 enabled/24h；到点触发 scheduled run 并推进 NextRunAt；禁用不触发；同渠道在途二次触发 409。
- [ ] 前端 vitest：client-file 两列渲染（渠道名/本机、失效红标）；对账区运行记录 + 报告视图；处置按钮 DangerConfirm 二次确认后发请求。`tsc -b --noEmit` + lint 过（预存失败 ui-package-boundary.test.ts 除外）。
- [ ] **真机（需用户确认）**：rustfs 删对象 → 对账报缺失 → 标记失效（列表红标、下载 410）→ 重传同文件自愈复绿；bucket 塞孤儿对象 → 对账列出 → 二次确认清理（bucket 内对象消失）；定期任务到点自跑留痕（runs 表 scheduled 记录）。

## 6. 风险 / 待定

- **报告快照过时**：处置端点均带现查守卫（§3.5），过时明细翻 stale 不误伤。
- **超大 bucket 对账时长**：分页遍历 + 异步执行 + 在途去重，不阻塞请求线程；索引侧一次性载入渠道全部 s3 资产键（内存 ~每条几十字节，十万级制品可承受；更大规模再引入分批比对，YAGNI）。
- **多 CP 实例**：在途去重为进程内互斥——当前部署形态恒单 CP（单二进制），多活 CP 需分布式锁（超范围，届时另立 FR）。
- **lost 资产被引用发布版本**：manifest 仍列该文件，updater 拉取得 410 明确失败；是否阻断含 lost 制品的频道发布（预检）留待运营反馈（不在本 FR 自动化）。
