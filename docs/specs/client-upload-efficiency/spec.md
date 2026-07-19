# 功能规格：客户端分发上传协议增效（秒传预查 + 小文件聚合 + 有限并发）

> 状态：已交付@v0.18.0　·　关联 PRD：FR-346（增强 FR-250/251，间接增强 FR-088/191）　·　关联 ADR：无（加性上传协议，同 FR-251 先例，不推翻既有决策）　·　分支：feature/fr-346-upload-efficiency

## 1. 背景与目标

FR-250/251 后发布链路为「本地暂存 → 点发布才批量上传」，每个待传文件走 FR-251 三段分块协议
（init → PUT chunk → complete），且文件间**严格串行**。真机反馈：数千文件整合包（3000 文件）
发布需 ≥9000 个串行请求，小文件的协议开销远超字节传输本身；且后端 CAS（`AssetService.Ingest`
按 `(type='client-file', sha256)` 唯一去重）本就内容寻址，二次发布同内容文件仍被完整重传——
缺的只是「先查后传」协议。

**目标**（P1）：
1. **秒传预查**：前端上传前算好各文件原始内容 SHA-256，一个请求批量查 N 个；命中制品库者
   跳过字节上传，直接得到与真上传同构的 `ClientFileResult` 引用既有制品。
2. **小文件聚合上传**：一个 multipart 请求携带多个小文件（≤ 8 MiB/个），替代逐文件三段协议。
3. **前端文件级有限并发**（并发 4）+ 进度整合（总进度单调不倒退、不 NaN）。
4. 大文件（> 8 MiB）保留 FR-251 分块协议不动（已算出 hash 者 complete 顺带 `expectedSha256` 强校验）。

### 不做（YAGNI）
- 断点续传跨 CP 重启、上传队列持久化、压缩传输（沿 FR-251 spec §6 留白）。
- 玩家消费端点改动（下载侧不变）。
- 单次 multipart 上传端点 `POST /:id/files` 与 FR-251 四端点的行为改动（零改动、共存）。
- 0 字节文件走分块 init 被拒的既有 BUG（另走 sdd-fix-bug；本 FR 的小文件路径天然支持 size=0，不依赖该修复）。

## 2. Hash 对齐语义（查重键结论，实现必须遵守）

- **前端查重键 = 文件原始内容 SHA-256**（WebCrypto `crypto.subtle.digest`，整读后散列）。
- **后端映射**：发布链路（单次/分块/本 FR 聚合）恒 `codec=none`，`AssetService.Ingest` 对上传
  字节**原样**散列落库（服务端无压缩环节）⇒ `assets(type='client-file').sha256` ==
  原始内容 hash == manifest `files[].sha256` == `files[].artifact.sha256`。
  故预查直接以 `(type='client-file', sha256=<前端 hash 小写>)` 命中，无需二级映射表。
- **历史 codec≠none 制品**（`zstd` / `zstd-patch` patch 制品）：其 `asset.sha256` 是压缩态字节
  hash，用原始内容 hash 查询**天然不会命中** → 恒 miss → 走真上传，无错误引用风险。
  即便出现字节巧合相同的极端情形，内容寻址保证存储 blob 字节 == 前端散列过的字节，
  返回 `codec=none` 依旧正确（客户端按原样落盘）。
- **秒传命中返回体 `codec` 恒 `"none"`**（与真上传 `codec=none` 去重命中时返回一致），
  不回读既有资产 metadata 里的 codec。
- 命中额外要求 `size` 精确相等（防前端 hash 算错文件；不等按 miss 处理，真上传由 Ingest 兜底校验）。
- 命中与真上传去重路径同样 bump `last_used_at`。

## 3. API 定义（均为 FR-346；权限 = JWT 平台管理员 `requirePlatformAdmin`，挂 admin 组，与 FR-251 同组）

### 3.1 批量秒传预查 `POST /api/v1/client-channels/:id/files/precheck`

请求（`application/json`）：

```json
{
  "files": [
    { "sha256": "ab34…64位hex", "size": 12345 }
  ]
}
```

- `files` 1..**500** 项；`sha256` 必填 64 位 hex（大小写不敏感，服务端归一小写）；`size` 必填 ≥ 0。

响应 `200 OK`（`results` 与请求 `files` **顺序对齐**、等长）：

```json
{
  "results": [
    { "sha256": "ab34…", "hit": true, "result": { "sha256": "ab34…", "md5": "…", "size": 12345, "codec": "none" } },
    { "sha256": "cd56…", "hit": false }
  ]
}
```

- `result` 与真上传返回的 `ClientFileResult{sha256,md5,size,codec}` **同构**（前端可无差别回填草稿）。
- 只读预查不产生审计记录；命中 bump `last_used_at`。

错误码：

| HTTP | error | 场景 |
|---|---|---|
| 400 | `INVALID_REQUEST` | JSON 非法 / `files` 空 / sha256 非 64 hex / size < 0 |
| 400 | `BATCH_LIMIT_EXCEEDED` | `files` 超 500 项 |
| 401/403 | 中间件 / `FORBIDDEN` | 未登录 / 非平台管理员 |
| 404 | `CHANNEL_NOT_FOUND` | 频道不存在 |
| 500 | `INTERNAL_ERROR` | 查询失败 |

### 3.2 小文件聚合上传 `POST /api/v1/client-channels/:id/files/batch`

请求（`multipart/form-data`，**part 顺序强约束**，服务端 `MultipartReader` 流式消费）：

1. 首个 part：字段名 `meta`，JSON 数组：

```json
[
  { "filename": "a.jar", "size": 2048, "sha256": "ab34…64位hex" }
]
```

2. 随后**恰好 `len(meta)` 个** part：字段名 `files`，与 `meta` **同序**，body 为对应文件原始字节。

上限（服务端常量，越限拒收）：

| 约束 | 值 | 依据 |
|---|---|---|
| 单批文件数 | ≤ **200** | 单请求 DB/audit 开销可控 |
| 单文件字节 | ≤ **8 MiB**（= FR-251 defaultChunkSize） | 聚合阈值与分块协议界线对齐 |
| 单批总字节（Σ meta.size） | ≤ **32 MiB** | 单请求体量护栏（低于常见反代上限，前端显式 120s 超时） |
| `sha256` | 必填 64 hex | 聚合路径前端必已算 hash，服务端经 `Ingest ExpectedSHA256` 强校验 |

响应 `201 Created`（`results` 与 `meta` 顺序对齐、等长；元素与 `ClientFileResult` 同构）：

```json
{
  "results": [
    { "sha256": "ab34…", "md5": "…", "size": 2048, "codec": "none" }
  ]
}
```

- 每项与该文件走单次 `POST /:id/files` 或 FR-251 分块上传的返回**逐字段一致**（同一 CAS 入库）。
- 审计：**每批 1 条** `client_file.publish`，detail `{channelId, via:"batch", count, totalBytes}`（不逐文件刷审计）。
- **失败语义 fail-fast**：任一文件校验失败即中止并返回错误；**已入库的前序文件保留**
  （CAS 无引用制品无害，重试整批时它们即秒传/去重命中，不重复占储）。

错误码：

| HTTP | error | 场景 |
|---|---|---|
| 400 | `INVALID_REQUEST` | multipart 非法 / 首 part 非 `meta` / meta JSON 非法 / `files` part 数与 meta 不符 / sha256 非法 / size < 0 |
| 400 | `BATCH_LIMIT_EXCEEDED` | 文件数 > 200 / 单文件 > 8 MiB / 总字节 > 32 MiB |
| 401/403 | 中间件 / `FORBIDDEN` | 未登录 / 非平台管理员 |
| 404 | `CHANNEL_NOT_FOUND` | 频道不存在 |
| 422 | `CHECKSUM_MISMATCH` | 某文件实际字节与声明 `sha256` 不符（含实际字节数≠声明 size 的一切情形——字节数不同必然 hash 不同） |
| 500 | `INTERNAL_ERROR` | 落 CAS / DB 失败 |

### 3.3 既有端点（不改，仅列关系）

- `POST /:id/uploads`、`PUT /:id/uploads/:uploadId/chunks/:index`、`POST /:id/uploads/:uploadId/complete`、
  `DELETE /:id/uploads/:uploadId`（FR-251）：> 8 MiB 文件继续走此协议；前端在已算出 hash 时
  complete 请求体顺带 `expectedSha256`（服务端既有能力，纯前端增强）。
- `POST /:id/files`（FR-088 单次 multipart）：保留，未被前端发布页使用（发布页走分块/聚合）。

## 4. 前端设计（路由、并发、进度）

常量（`apps/control-plane-web/src/lib/clientUploadPlan.ts`，与服务端上限一致）：

| 常量 | 值 | 含义 |
|---|---|---|
| `AGGREGATE_MAX_FILE_BYTES` | 8 MiB | ≤ 此值走聚合；> 此值走 FR-251 分块 |
| `HASH_MAX_FILE_BYTES` | 256 MiB | ≤ 此值才算 hash 并预查（WebCrypto 无流式，整读内存护栏）；> 此值直接分块不预查 |
| `PRECHECK_MAX_ENTRIES` | 500 | 预查请求分批大小 |
| `BATCH_MAX_FILES` / `BATCH_MAX_TOTAL_BYTES` | 200 / 32 MiB | 聚合装箱上限（镜像服务端） |
| `UPLOAD_CONCURRENCY` | 4 | 上传任务池并发（3~5 区间取 4） |

流水线（`uploadFilesEfficient(channelId, entries, {signal, onProgress})` → `Map<key, ClientFileResult>`）：

1. 输入 = FR-250 本地去重后的 unique 草稿（`name+size` 第一层近似去重保留）。
2. **hash 阶段**：安全上下文（HTTPS/localhost）下逐个**串行**读取 ≤ 256 MiB 的文件算 SHA-256（内存峰值 ≤ 单文件；> 256 MiB 跳过）；普通 HTTP 非安全上下文无 WebCrypto 时，≤ 8 MiB 聚合小文件用无依赖纯 JS SHA-256 兜底，> 8 MiB 文件跳过 hash 直接分块，兼顾请求聚合与主线程开销。
3. **预查阶段**：hash 按 ≤ 500/批请求 precheck；**预查请求失败不阻断发布**（降级视全部 miss，
   继续正常上传——预查是纯优化，出错由后续真上传兜底暴露）。
4. **装箱**：miss 且 ≤ 8 MiB → 贪心装箱进聚合批（≤200 个且 ≤32 MiB/批）；miss 且 > 8 MiB
   与未 hash 的超大文件 → 分块任务（有 hash 者 complete 带 `expectedSha256`）。
5. **执行**：聚合批 + 分块任务混入并发 4 的任务池；任一任务失败即中止其余（fail-fast）并抛错
   （页面保留草稿可重试，与现行为一致）；`signal` 取消贯穿各阶段（分块已含弃单 DELETE）。
6. 命中者不产生任务，结果直接取预查 `result`。

进度模型（单调、不 NaN）：

- `phase: 'hashing' | 'uploading'`；hashing 按文件数（`hashedFiles/totalFilesToHash`），
  uploading 按字节（`uploadedBytes/totalBytes`，`totalBytes` = Σ unique 字节）。
- `uploadedBytes` = 已完成任务字节 + 在途任务已传字节（聚合批用 axios `onUploadProgress`
  按批总字节封顶折算；分块沿用片粒度回调）+ 秒传命中文件在预查落定即计满。
- 汇报经**单调 clamp**（`report(next) = max(prev, next)`），总进度不倒退；
  `totalBytes === 0` 时百分比恒 0（复用现 UI 的 `total>0` 守卫），无 NaN。
- 进度回调结构（页面据此渲染，orchestrator 不做 i18n）：
  `{phase, hashedFiles, totalFilesToHash, uploadedBytes, totalBytes, completedFiles, totalFiles, reusedFiles, current: {kind:'file',name}|{kind:'batch',count}|null}`。

页面接入（`ClientPublishPage.doPublish`）：串行 for 循环替换为 orchestrator 单调用；
进度条增 hashing 阶段文案与秒传命中数展示；批量任务名以 i18n 文案渲染（`聚合小文件 ×N`）。
聚合批请求显式 `timeout: 120000`（axios 默认 10s 对 32 MiB 批不够）。

## 5. 后端设计

- **新服务** `internal/controlplane/service/client_upload_efficiency.go`：
  `ClientUploadEfficiencyService{db, versions *ClientVersionService}`。
  - `Precheck(entries []PrecheckEntry) ([]PrecheckResult, error)`：校验（数量/格式）→ 单条
    `WHERE type='client-file' AND sha256 IN (...)` 批查 → 按序回填 → 命中 ids 一条 UPDATE bump `last_used_at`。
  - `ValidateBatchMetas(metas []BatchFileMeta) error`：数量/单文件/总字节/sha 格式护栏。
  - `IngestBatchFile(meta BatchFileMeta, r io.Reader) (*ClientFileResult, error)`：
    `LimitReader(size+1)` 防超量 → 复用 `versions.PublishFile{Codec:"none", ExpectedSHA256}`（Ingest 强校验+去重）。
- **新 handler** `internal/controlplane/router/client_upload_efficiency.go`（不碰 `client_version.go`）：
  两端点 + `RegisterRoutes(admin 组)`；batch 用 `c.Request.MultipartReader()` 流式逐 part 消费
  （meta 首 part → 逐 files part 调 `IngestBatchFile`），multipart 顺序装配逻辑抽为可测函数。
- **装配**：`router.Services` 加 `ClientUploadEfficiency` 字段（nil 则端点关闭，测试零改动）；
  `apps/control-plane/main.go` 在 `clientVersionSvc` 后构造注入。
- 复用不改：`AssetService.Ingest`、`ClientVersionService.PublishFile` 签名与去重逻辑原样。

## 6. devmock 契约镜像（`packages/devmock/src/handlers/domains/client.ts`）

- 模块级 `knownArtifacts: Map<sha256, {md5,size}>`：batch 成功登记 meta 各项；分块 complete
  提供 `expectedSha256` 时同样登记（并回显该 sha，提升镜像保真）。
- `POST /:id/files/precheck`：按登记表命中（sha+size 全等），响应结构与真后端一致。
- `POST /:id/files/batch`：`formData()` 解析 meta+files、校验数量/上限，结果回显 meta 声明值，201。

## 7. 测试计划（先行）

Go（`client_upload_efficiency_test.go`，service 级，沿 FR-251 范式）：
- 预查：命中 / 未命中 / 混合顺序对齐；命中结果与真上传 `PublishFile` 返回逐字段一致；size 不符按 miss；
  非法 sha / 超 500 项报错。
- 聚合：多文件成功且各结果与单独 `PublishFile` 一致；声明 sha 不符拒收（`ErrChecksumMismatch`）
  且前序文件已入库；超文件数 / 超单文件 / 超总字节拒收；size=0 文件可入库。
- router 侧：multipart 顺序装配函数（meta 首位、数量匹配、乱序/缺 part 报错）。

前端 vitest：
- `clientUploadPlan.test.ts`：hash 正确性（已知向量）、按大小分路（聚合/分块/免预查）、贪心装箱
  （数量与字节上限）、并发池上限、进度聚合单调不倒退不 NaN。
- `efficientUpload.test.ts`（mock `@/api/client`）：预查命中跳过上传、miss 小文件进聚合、
  大文件走分块、预查失败降级全量上传、HTTP 非安全上下文无 WebCrypto 时小文件用 JS hash 兜底聚合、fail-fast。
- `ClientPublishPage.dom.test.tsx`：请求计数器与失败注入随新协议演进（行为契约不变：
  选文件零请求、点发布才上传、失败保草稿）。

## 8. 验收标准

- `go build ./...`、`go vet ./...`、`go test ./internal/controlplane/...` 绿；
  前端 `tsc -b --noEmit`、`pnpm lint`、相关 vitest 全绿；FR-251 既有分块测试不破。
- 预查命中返回体与真上传逐字段一致（单测断言）；聚合上传结果与单文件上传逐字段一致（单测断言）。
- 3000 小文件量级请求数：改前 ~9000 串行 → 首次发布 ~`ceil(3000/500)=6` 预查 + `~ceil(3000/200)=15` 聚合批
  （字节上限触顶时按 32 MiB/批折算）；内容未变的二次发布仅 ~6 个预查请求、零字节上传。
- 进度条全程单调不倒退、无 NaN；取消可中止并保草稿。
- **【需真机，用户确认】** 浏览器实发一个数千文件整合包：首发走聚合+并发明显快于旧串行；
  重复发布秒传直达；混合场景（部分命中+小文件+>8MiB 大文件）发布产物 manifest 正确、
  玩家端可正常拉取；zh/en 与暗亮主题正常。

## 9. 风险 / 待定

- **WebCrypto 可用性与无流式**：仅安全上下文（HTTPS/localhost）提供 `crypto.subtle`；普通 HTTP 面板对 ≤ 8 MiB 小文件使用内置纯 JS SHA-256，保留预查/聚合，较大文件直接分块。WebCrypto 可用时 hash 阶段整读文件进内存，以 `HASH_MAX_FILE_BYTES=256MiB` + 串行护栏；超限文件放弃预查（首发多传一次，不影响正确性）。不引第三方流式 hash 库。
- **聚合批部分成功**：fail-fast 后前序文件留在 CAS（无引用、内容寻址无害）；重试整批秒传通过。
  不做逐文件 partial-result 协议（YAGNI）。
- **预查降级**：预查请求失败静默降级为全量上传，不阻断发布（优化失效 ≠ 功能失效）。
- **axios 超时**：聚合批显式 120s；分块单片沿用现状（10s 默认）不动。
