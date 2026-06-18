# 实施计划 — FR-045 制品库（内容寻址 + 完整性校验）

> 关联 FR: FR-045 | 优先级: P1 | 状态: 🔨 in-progress | 关联 ADR: ADR-011（依赖 FR-044 数据根）

## 背景

平台要下载并复用 MC 核心 jar，后续还有插件、图片、视频、媒体 blob 等。纯文件夹缓存无去重无校验；纯扁平哈希池不利于归档/分层。采用**类型分区 + 内容寻址（CAS）**：物理存于数据根 `var/artifacts`，DB 索引 `assets`，sha256 去重 + md5/sha256 校验。

## 设计要点

- **CAS 布局**：`var/artifacts/<type>/<sha256[:2]>/<sha256><ext>`，登记 `rel_path` 相对数据根（便携）。
- **去重**：唯一键 `(type, sha256)`；命中复用记录并刷新 `last_used_at`，不重复落盘。
- **完整性**：边写临时文件边算 sha256/md5/size；期望校验和不符则删临时文件并拒收。
- **原子落位**：临时文件写入 `cache/`，校验/去重通过后 `os.Rename` 到 CAS 目标；DB 写失败回滚物理文件。
- **引用保护**：`ref_count>0` 删除前拒绝。
- **归档就绪**：`storage_state` + `storage_backend` 留位（归档策略/外部后端为后续 FR）。

## 任务拆解

### 数据模型与迁移
- [x] `internal/controlplane/model/asset.go`：`Asset` + `AssetType`/`AssetStorageState` 枚举 + `ValidAssetType`，唯一索引 `idx_assets_type_sha256`。
- [x] `database.AutoMigrate` 注册 `&model.Asset{}`。

### CAS 存储服务
- [x] `internal/controlplane/service/asset.go`：`AssetService`（持 `*dataroot.Root`）。
  - [x] `Ingest(io.Reader, IngestParams)`：算 sha256+md5 → 校验 → 去重 → 原子落 CAS → 建记录。
  - [x] `IngestFromPath`、`IngestFromURL`（下载入库，供 FR-034 复用）。
  - [x] `List(type, page, pageSize)`、`GetByID`、`Delete`（引用保护）、`AbsPath`。
  - [x] 错误：`ErrAssetNotFound` / `ErrAssetInUse` / `ErrInvalidAssetType` / `ErrChecksumMismatch`。

### 路由 `/assets`（平台管理员）
- [x] `internal/controlplane/router/asset.go`：`GET /assets`、`GET /assets/:id`、`POST /assets`(multipart 或 register-from-path)、`DELETE /assets/:id`。
- [x] `router.go` 注册（Handler 内部 `requirePlatformAdmin` 收敛）；`Services.Asset` 接线；CP `main.go` 用数据根构造 `AssetService`。

### 测试
- [x] 服务层（`asset_test.go`）：hashing、CAS 布局、去重（同类型复用/跨类型不去重）、校验和（匹配/不符/大小写/md5）、非法类型、from-path、list 过滤分页、引用保护、not-found。
- [x] 路由层（`asset_test.go`）：multipart 上传、list 过滤、get、去重、校验和拒收(422)、引用保护(409)、平台管理员鉴权(403)。

## 验证

- `go build ./...`、`go vet ./...` 通过。
- `go test ./internal/controlplane/service/... ./internal/controlplane/router/... ./internal/platform/...` 通过。

## 未覆盖（后续 FR，按范围纪律不在本次实现）

- 归档策略与外部对象存储后端切换（模型已留位 `storage_state`/`storage_backend`）。
- FR-034 建服取核心（调用 `IngestFromURL` + 绑定 `ref_count`）。
- FR-014 模板引用制品库核心。
- `ref_count` 的自动增减由引用方（模板/实例）落地时实现；本次仅提供保护语义与字段。
