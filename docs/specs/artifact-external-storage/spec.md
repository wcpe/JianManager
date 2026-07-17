# 功能规格：制品库外置对象存储底座（BlobStore + 存储渠道 + 302 预签名分发）

> 状态：开发中　·　关联 PRD：FR-347（增强 FR-088，修订 ADR-011 存储节）　·　分支：feature/fr-347-artifact-external-storage　·　架构决策：ADR-073

## 1. 背景与目标

客户端分发制品（client-file）体量大（整包数 GB），当前全部落 CP 数据根 `var/artifacts` 且下载由 CP `http.ServeContent` 直出——CP 磁盘与出口带宽成为分发瓶颈。用户诉求：制品上传自部署 rustfs（S3 兼容）做数据分发，CP 不存大对象、不当带宽中继。

本 FR 建「外置对象存储底座」：BlobStore 存储抽象 + 本地/S3 双适配器 + 制品存储渠道配置页 + S3 制品 302 预签名短时效 URL 下载。迁移（FR-348）与对账（FR-349）在本底座之上后续实现。

**已拍板决策（brainstorming 用户确认）**：S3 下载走 302 预签名短时效 URL（本地制品仍 CP ServeContent 直出）；制品库独立一套存储渠道（不动备份域 BackupStorage）；凭证面板直填 + 可逆加密存储（沿 FR-192 拉取密钥可逆加密先例）；不做 SFTP/WebDAV 适配器（接口留扩展位）、不做迁移与对账。

## 2. 需求（要什么）

- BlobStore 存储抽象：统一 Put/Open/Stat/Delete/List/Presign，后续新后端只加适配器。
- 本地适配器：封装现 CAS 落盘行为，**local 路径行为零变化**（临时文件 `os.Rename` 原子进 CAS、`ServeContent` 直出、`os.Remove` 删除）。
- S3 适配器：SigV4 纯标准库（无 SDK）+ presign query 签名，path-style 寻址，http/https 均支持（rustfs/MinIO 常走 http 内网）。
- 新表「制品存储渠道」：名称唯一、type local|s3、endpoint/bucket/region/prefix、AK/SK 可逆加密列、useSSL、presign TTL、连通测试结果、活跃标记；内置一条不可删的「本机存储」渠道。
- 渠道 API：CRUD + 连通测试（真连探测）+ 设活跃渠道；admin JWT。
- 写路径：`AssetService.Ingest` 落盘改经活跃渠道 BlobStore——活跃=local 行为完全如旧；活跃=s3 上传对象（键沿 CAS 相对路径），Asset 记 `StorageBackend='s3'` + 渠道引用。**仅 client-file 类型走渠道路由**，其余 Asset 类型（core/plugin/jdk/updater-core/client-updater-core 等）恒 local。
- 读路径：按 `Asset.StorageBackend` 路由——local→`http.ServeContent` 如旧；s3→302 `Location=` 预签名 URL（TTL 渠道可配，默认 10 分钟）。
- 前端「文件存储配置」页：渠道列表 + 新建/编辑模态（ui-modals.md 纪律）+ 连通测试 + 设活跃 + 删除守卫；SK 脱敏（编辑不回显明文）。
- devmock 契约同步 + 中英 i18n + docs/API.md + docs/ARCHITECTURE.md 存储节。

**不做（范围外）**：SFTP/WebDAV 适配器；存量制品迁移（FR-348）；索引↔S3 对账（FR-349）；multipart 分段上传到 S3（单 PUT 流式足够，rustfs/MinIO 单对象上限远超制品体量；后续需要再扩展）；备份域 BackupStorage 任何改动；updater 侧代码改动（跨协议限制以部署建议规避，见 §3.8）。

## 3. 设计（怎么做）

### 3.1 BlobStore 接口（新包 `internal/controlplane/blobstore`）

```go
// ObjectInfo 一个已存储 blob 的元数据。
type ObjectInfo struct {
    Key     string    // 存储键（= CAS 相对路径）
    Size    int64
    ModTime time.Time
}

// Store 制品 blob 存储后端抽象（ADR-073）。键 = CAS 相对路径
// var/artifacts/<type>/<sha2>/<sha256><ext>（与 Asset.RelPath 同值）；
// S3 侧真实对象键 = <prefix>/<key>。
type Store interface {
    Kind() string                                                        // "local" | "s3"（写入 Asset.StorageBackend）
    PutFile(ctx context.Context, key, srcPath string, size int64) error  // 把已写完的本地临时文件放入存储
    Open(ctx context.Context, key string) (io.ReadCloser, error)         // 读取 blob 内容
    Stat(ctx context.Context, key string) (*ObjectInfo, error)           // 元数据；缺失→ErrBlobNotFound
    Delete(ctx context.Context, key string) error                        // 幂等删除（缺失不报错）
    List(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error) // 前缀枚举（连通探测与 FR-349 对账用）
    Presign(key string, ttl time.Duration) (string, error)               // 短时效公开 GET URL；local→ErrPresignUnsupported
}
```

- 入口用 `PutFile`（而非泛化 `Put(io.Reader)`）：Ingest 恒先落临时文件边写边算 hash，local 适配器可用与现状**逐字节等价**的 `os.MkdirAll + os.Rename`（零行为变化）；S3 适配器 `os.Open` 后流式 PUT（`UNSIGNED-PAYLOAD`，与 worker 备份域同策略，不缓冲大文件）。
- `Presign` 纯本地签名计算无网络 IO，不带 ctx。
- 本地适配器：`NewLocal(root *dataroot.Root)`；Open/Stat/Delete 即 `os.Open/Stat/Remove`（Remove 幂等吞 not-exist）；List 走 `filepath.WalkDir` 限量；Presign 返回 `ErrPresignUnsupported`。
- S3 适配器：`NewS3(cfg S3Config)`（Endpoint/Bucket/Region/Prefix/AccessKey/SecretKey/UseSSL/HTTPClient）；path-style `<scheme>://<endpoint>/<bucket>/<prefix>/<key>`；SigV4 header 签名（PUT/GET/HEAD/DELETE/List）+ presign query 签名（`X-Amz-Algorithm/Credential/Date/Expires/SignedHeaders=host/Signature`）；`now func() time.Time` 可注入以做确定性签名测试；region 缺省 `us-east-1`；endpoint 容带 scheme 自动剥离（与 worker 版一致）。
- 依赖方向：CP 不 import `internal/worker/storage`（进程边界语义脏）；CP 侧独立实现并对拍 AWS 官方签名向量，重复原因与取舍见 ADR-073 §决策 3。

### 3.2 制品存储渠道模型（新表 `artifact_storage_channels`）

```go
type ArtifactStorageChannel struct {
    ID        uint   `gorm:"primaryKey"`
    Name      string `gorm:"type:varchar(128);not null;uniqueIndex"`
    Type      string `gorm:"type:varchar(16);not null"`      // local | s3（创建后不可改）
    Endpoint  string `gorm:"type:varchar(512)"`              // host[:port]，容带 scheme
    Bucket    string `gorm:"type:varchar(255)"`
    Region    string `gorm:"type:varchar(64)"`               // 缺省 us-east-1
    Prefix    string `gorm:"type:varchar(255)"`              // 对象键前缀
    // AccessKeyEnc/SecretKeyEnc AES-256-GCM 可逆加密密文（复用 FR-192 KeyEncryptor 基建）。
    // json:"-"：任何 API 响应不回明文也不回密文；列表以 HasAccessKey/HasSecretKey 布尔示意。
    AccessKeyEnc string `gorm:"type:text" json:"-"`
    SecretKeyEnc string `gorm:"type:text" json:"-"`
    UseSSL       bool
    // PresignTTLSeconds 预签名 URL 有效期秒数，默认 600（10 分钟），钳制 [60, 3600]。
    PresignTTLSeconds int  `gorm:"default:600;not null"`
    Active            bool `gorm:"default:false;not null"`   // 全表恰一条活跃（service 事务保证）
    Builtin           bool `gorm:"default:false;not null"`   // 内置「本机存储」，不可删不可编辑
    LastTestAt        *time.Time
    LastTestOk        bool
    LastTestMessage   string `gorm:"type:varchar(255)"`
    HasAccessKey      bool   `gorm:"-"`                      // 列表标示是否已配凭证（不回明文）
    HasSecretKey      bool   `gorm:"-"`
    CreatedAt         time.Time
    UpdatedAt         time.Time
}
```

- **硬删除**（无 DeletedAt）：渠道被 Asset 引用时禁止删除（见删除守卫），无需软删；名称释放即可复用。
- **内置「本机存储」**：服务装配时 `EnsureBuiltin()` 幂等 seed 一条 `{Name:"本机存储", Type:local, Builtin:true}`；若全表无活跃行则将其置活跃。内置行不可删、不可编辑（无可编辑字段），仅可被「设活跃」与连通测试。
- **活跃语义**：写路径唯一路由开关。`SetActive(id)` 事务内先清后设，全表恰一条活跃；活跃渠道禁删（先切走再删）。切活跃**只影响新上传**，存量 Asset 按各自 StorageBackend/渠道引用读取，不受影响。
- **凭证**：创建/编辑面板直填明文 → service 层 `KeyEncryptor.Encrypt`（AES-256-GCM）落 `*Enc` 列；读取路径需要时 `Decrypt` 还原。加密器未配置（生产 autogen 失败降级态）时**创建/编辑 s3 渠道直接 422 拒绝**（不落明文、不静默降级）；加密密钥丢失时存量渠道解密失败 → 上传/预签名报错（快失败，运维恢复密钥文件即愈）。
- **Asset 渠道引用**：`model.Asset` 增列 `StorageChannelID uint`（默认 0=本地/无渠道）；新增常量 `model.AssetBackendS3 = "s3"`。local 入库不打渠道标（历史行为原样）；s3 入库记渠道 ID 且 `StorageState=external`。

### 3.3 渠道服务（`service.ArtifactStorageChannelService`）

- `List()`：全量渠道（含 HasAccessKey/HasSecretKey 填充），Builtin 恒排最前。
- `Create(p)`：仅允许 `type=s3`（local 由内置行独占，杜绝多条 local 语义歧义）；校验名称唯一（预检 + DB 唯一索引兜底）、endpoint/bucket 非空、TTL 钳制；凭证加密落库。
- `Update(id, p)`：Builtin → 拒；type 不可改；`accessKey`/`secretKey` 传空 = **保留原值**（脱敏编辑语义），传非空 = 重加密覆盖；成功后清 LastTest*（配置已变旧结论失效）。
- `Delete(id)`：Builtin → 拒；活跃 → 拒；`assets.storage_channel_id = id` 计数 > 0 → 拒（附引用数）。
- `SetActive(id)`：事务清旧设新。
- `Active()`：当前活跃渠道；无活跃行时回退内置 local（防御）。
- `StoreFor(ch)` / `StoreForAsset(a)`：渠道 → BlobStore 实例（local→NewLocal(root)；s3→解密 AK/SK→NewS3）。读路径按 `Asset.StorageChannelID` 找渠道（渠道已删但仍有引用不可能发生——删除守卫兜底）。
- `TestCandidate(p)` / `TestSaved(id)`：真连探测——s3 = 写探测对象（`PutFile` 8 字节 → `Stat` → `Delete`，探测键 `probe/jm-probe-<unixnano>` 挂渠道 prefix 下），验证的正是写路径所需权限；local = 数据根可写探测。返回 `{ok,message,errorCode?,latencyMs}`；TestSaved 持久化 LastTest*（形态沿备份域先例）。TestCandidate 支持带 `id`：编辑态凭证留空时用存库密文解密后探测。

### 3.4 写路径（Ingest 渠道路由）

`AssetService` 注入渠道服务（`SetStorageChannels`，不注入=纯 local，既有测试零改动）。`Ingest` 在去重未命中、临时文件就绪后：

```
仅 p.Type == client-file 且渠道服务已注入时取活跃渠道：
  活跃 = local → 走现有代码原文（MkdirAll + os.Rename 进 CAS；StorageBackend=local）
  活跃 = s3   → store.PutFile(ctx, relPath, tmpPath, size)（S3 PUT，键=CAS 相对路径挂渠道 prefix）
                Asset{StorageBackend:"s3", StorageChannelID:ch.ID, StorageState:external, RelPath:relPath 不变}
其余类型恒走 local 分支。
```

- 去重语义不变：`(type, sha256)` 命中即复用既有记录（**不迁移不重传**，哪怕现活跃渠道与既有记录后端不同——内容寻址同内容等价，位置由记录自述）。
- S3 上传失败 → Ingest 报错（快失败，不静默回落 local——回落会让 blob 落点不可预期，违背「CP 不存大对象」的用户意图）；DB 落记录失败 → 尽力删已传对象（对称现 local 的 `os.Remove` 回滚）。
- 所有上传入口（单传 PublishFile / 分块合成 FR-251 / 聚合小文件 FR-346）均汇于 `Ingest`，一处路由全覆盖。

### 3.5 读路径（按记录路由）

- **玩家消费端点 `GET /client-artifacts/:sha256`**（拉取密钥鉴权）：鉴权、防护、限流、带宽检查**全部照旧先行**；之后按 `Asset.StorageBackend` 分流——local → `os.Open + http.ServeContent`（原文）；s3 → 现算预签名 URL（TTL=渠道配置），`Cache-Control: no-store`（短时效 URL 禁缓存）+ **302** `Location`。updater 已 `setInstanceFollowRedirects(true)` 自动跟随（跨协议限制见 §3.8）。预签名失败（渠道缺失/解密失败）→ 503 `ARTIFACT_STORAGE_UNAVAILABLE`（对 updater 可重试语义）。追踪事件照记（302 计一次 artifact 事件；Bytes 为 CP 出口字节≈0，S3 出口流量不经 CP，语义为「CP 已授权跳转」）。
- **管理面下载 `GET /client-channels/:id/files/download`**（JWT）：s3 → CP 经 BlobStore.Open **代理直流**（Content-Length=asset.Size + attachment）。不走 302：前端 axios blob fetch 跨域跟随预签名 URL 会撞 rustfs CORS（见 §3.8），管理面下载是低频运维动作，CP 中继代价可接受、前端零改动。local 原文不动。
- **管理面文本预览 `ReadArtifactText`（≤1MiB）**：s3 → BlobStore.Open 限量读，降级口径（too-large/binary）不变。
- **发布期 zstd 补丁生成 `manifestFileContentPath`**：源制品在 s3 时经 BlobStore.Open 物化到临时文件再参与 diff（复用既有 materialize 管道，本地 codec=none 直用 CAS 路径的快路径原样保留）。
- **删除 `AssetService.Delete`**：DB 记录删除后按后端清物理——local `os.Remove`（原文）；s3 `store.Delete`（均尽力而为，与现状一致）。

### 3.6 API（gate-api 完整定义）

全部挂 admin 组（JWT + `RequireRole(RolePlatformAdmin)`），前缀 `/api/v1`。响应**永不含**凭证明文或密文。

| Endpoint | 方法 | 请求体 | 成功响应 | 错误 | FR |
|---|---|---|---|---|---|
| `/artifact-storages` | GET | — | 200 `ArtifactStorageChannel[]`（含 hasAccessKey/hasSecretKey/active/builtin/lastTest*） | 500 INTERNAL_ERROR | FR-347 |
| `/artifact-storages` | POST | `{name*, type*("s3"), endpoint*, bucket*, region?, prefix?, accessKey?, secretKey?, useSsl?, presignTtlSeconds?}` | 201 渠道对象 | 400 INVALID_REQUEST；422 BUSINESS_ERROR（名称冲突/类型非法/endpoint 或 bucket 缺失/TTL 越界/加密器未配置） | FR-347 |
| `/artifact-storages/:id` | PUT | 同 POST（type 不可改；accessKey/secretKey 空=保留） | 200 渠道对象 | 400；404 NOT_FOUND；422 BUSINESS_ERROR（内置不可编辑/类型不可改/名称冲突/校验失败） | FR-347 |
| `/artifact-storages/:id` | DELETE | — | 200 `{message}` | 404；422 BUSINESS_ERROR（内置不可删/活跃不可删/被 N 个制品引用） | FR-347 |
| `/artifact-storages/test` | POST | 同 POST + 可选 `id`（编辑态复用存库凭证） | 200 `{ok,message,errorCode?,latencyMs}` | 400 | FR-347 |
| `/artifact-storages/:id/test` | POST | — | 200 同上（并持久化 LastTest*） | 404 | FR-347 |
| `/artifact-storages/:id/activate` | POST | — | 200 渠道对象 | 404 | FR-347 |
| `GET /client-artifacts/:sha256`（既有，玩家 key 鉴权） | GET | — | local：200/206 如旧；s3：**302** `Location=预签名 URL` + `Cache-Control: no-store` | 503 ARTIFACT_STORAGE_UNAVAILABLE（渠道失效/解密失败）；其余错误码不变 | FR-347 |
| `GET /client-channels/:id/files/download`（既有，JWT） | GET | — | local 如旧；s3：200 CP 代理直流 | 404/500 不变 | FR-347 |

TypeScript 类型（前端 `src/api/artifactStorages.ts` 与 devmock contracts 同源）：

```ts
interface ArtifactStorageChannel {
  id: number; name: string; type: 'local' | 's3'
  endpoint: string; bucket: string; region: string; prefix: string
  useSsl: boolean; presignTtlSeconds: number
  active: boolean; builtin: boolean
  hasAccessKey: boolean; hasSecretKey: boolean
  lastTestAt?: string; lastTestOk: boolean; lastTestMessage: string
  createdAt: string; updatedAt: string
}
interface SaveArtifactStorageBody {
  name: string; type: string; endpoint?: string; bucket?: string; region?: string
  prefix?: string; accessKey?: string; secretKey?: string; useSsl?: boolean
  presignTtlSeconds?: number
}
interface ArtifactStorageTestResult { ok: boolean; message: string; errorCode?: string; latencyMs: number }
```

### 3.7 前端「文件存储配置」页

- 路由 `/artifact-storages`，导航挂「平台管理 → 存储与运行时」小节（备份存储旁），i18n `nav.artifactStorages`。
- 列表：渠道卡片/行（名称+内置/活跃徽章、类型、endpoint/bucket、最近测试结果、TTL）；操作=测试/设活跃/编辑/删除（内置行隐藏编辑删除；活跃行禁删）。
- 新建/编辑：`Dialog` + `scrollableDialogContentClass` + `ScrollableDialogBody`（ui-modals.md）；字段 name/endpoint/bucket/region/prefix/useSsl/presignTtl/accessKey/secretKey；**编辑不回显 SK**（placeholder「已配置，留空保留」，据 hasSecretKey 提示）；表单内「测试连接」按钮即时探测（编辑态密钥留空带 id 复用存库凭证）。
- 删除守卫命中 422 用后端 message 呈现；设活跃有确认语义（影响后续上传落点）。
- devmock 新域 `artifact-storage.ts`（contracts + handlers + seed 两条：内置 local 活跃 + s3 示例）；DOM 测试覆盖列表/创建/测试/设活跃/删除守卫。

### 3.8 消费方兼容性与部署注意（真机必验）

- **updater 跨协议 302 限制**：`HttpURLConnection.setInstanceFollowRedirects(true)` **不跨协议跟随**（http↔https 互跳返回 302 原文不追）。部署建议（文档级约束，updater 不改码）：CP 与 S3 endpoint **同协议**——CP https 则 rustfs 走 https（或同域反代）；CP http 内网则 rustfs http。真机验证点。
- **跟随重定向的请求头外泄**：Java 跟随时会向 S3 重放 `X-Client-Key` 等自定义头。拉取密钥本为半公开（ADR-022），且预签名 URL 指向运营商自有 rustfs，非第三方，无新增泄露面；S3 侧签名仅覆盖 host 头，多余头不破签。
- **浏览器面板**：管理面下载现实现为 axios blob fetch（`downloadClientArtifact` → JWT 端点），若 302 到跨域预签名 URL，浏览器跟随后 CORS 校验 rustfs 响应头必失败（rustfs 默认无 CORS）。故管理面 s3 走 **CP 代理直流**（§3.5），前端零改动、rustfs 无需配 CORS。玩家端点 302 的消费方是 updater（非浏览器），无 CORS 问题。
- **时钟偏移**：预签名有效性依赖 CP 与 S3 服务器时钟一致；偏移超 TTL 会 403。运维要求 NTP 同步（rustfs 同机/同网常态无虞）。

## 4. 任务拆分

- [x] spec + ADR-073（本文档）
- [x] Go：blobstore 包（接口 + local + s3 + presign）+ 签名向量/适配器测试
- [x] Go：渠道 model + AutoMigrate + ArtifactStorageChannelService + 测试
- [x] Go：渠道 router + 装配（main.go/router.go）+ 测试
- [x] Go：Ingest 渠道路由 + 读路径（302/代理/预览/补丁物化/删除）+ 测试 + local 全回归
- [x] Web：api client + 文件存储配置页 + 模态 + nav/route + i18n + DOM 测试
- [x] devmock 契约同步
- [x] 文档同步：API.md、ARCHITECTURE.md 存储节、adr/README 索引（PRD/CHANGELOG 由主会话统一处理）

## 5. 验收标准

- [ ] `go build ./... && go vet ./...` 过；`go test ./internal/controlplane/...` 全绿（预存失败 TestBotStressSession_StartCreatesAssociatedBots 除外）；既有 asset/client_version/backup 测试零破坏（local 行为零变化的回归证明）。
- [ ] S3 presign 签名对拍 AWS SigV4 官方文档向量逐字节一致；header 签名经 fake S3 服务器断言请求形态（path-style、UNSIGNED-PAYLOAD、Authorization 结构）。
- [ ] 渠道 CRUD：重名 422、内置不可删不可编辑、活跃不可删、被制品引用不可删（附引用数）、SK 编辑留空保留、响应永不含凭证。
- [ ] Ingest：活跃=local 与主线行为逐字段一致（CAS 路径/StorageBackend=local/物理文件存在）；活跃=s3 对象上到 fake S3（键=prefix+CAS 相对路径）、Asset 记 s3+渠道 ID+external、本地 CAS 无文件。
- [ ] GetArtifact：local 制品 200/206 如旧；s3 制品 302 且 Location 含 `X-Amz-Signature`/`X-Amz-Expires=<渠道 TTL>`、`Cache-Control: no-store`；渠道解密失败 503。
- [ ] 管理面下载：s3 制品经 CP 代理 200 字节一致；文本预览 s3 制品正常/降级口径不变。
- [ ] 前端 vitest：渠道页列表/创建校验/连通测试/设活跃/删除守卫全绿；`tsc -b --noEmit` + `pnpm lint` 过（预存失败 ui-package-boundary.test.ts 除外）。
- [ ] **真机（需用户确认）**：rustfs 建渠道→测试连通→设活跃→上传制品落 rustfs（bucket 内可见对象）→updater 经 302 拉取成功（同协议部署）→浏览器面板下载/预览正常→切回本机存储后新上传回落本地（local 回归）。

## 6. 风险 / 待定

- **加密密钥丢失**（`etc/client-key-enc.key` 损毁且未备份）：存量渠道 SK 不可解密，s3 上传/预签名快失败报错；恢复=从备份还原密钥文件或重填渠道凭证。与 FR-192/263 同一风险面，无新增。
- **切活跃后的双落点**：制品分布于本地与多个 s3 渠道由 Asset 逐条自述，读路径永远正确；集中归拢靠 FR-348 迁移工具。
- **presign 时钟偏移**：见 §3.8，运维要求。
- **updater 跨协议不跟随**：文档级部署建议规避；若未来必须跨协议，需 updater 增手动跟随（另立 FR，动客户端发版链路）。
