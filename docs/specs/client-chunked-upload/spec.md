# 功能规格：客户端分发大文件分块上传 + 进度

> 状态：待审　·　关联 PRD：FR-251（增强 FR-088）　·　关联 ADR：无（加性上传协议，不推翻既有决策）　·　分支：feature/fr-251-chunked-upload

## 1. 背景与目标

现有客户端制品上传 `POST /client-channels/:id/files`（`ClientVersionHandler.PublishFile` → `ClientVersionService.PublishFile(io.Reader)` → `AssetService.Ingest`）是**单次 multipart 上传**：一个请求传完整文件。真机短板：① 4G+ 整合包单请求易超时/被反代拦；② 无断点、失败整传重来；③ 无实时进度（只能 spinner）。FR-191 spec §6 已预埋「超大整合包…改服务端分块端点（后续 FR）」——本 FR 即承接。

**目标**：后端新增**分块上传协议**（init→chunk→complete），落盘复用现有 CAS（`AssetService.Ingest`），返回体与单次上传**完全一致**（`{sha256,md5,size,codec}`）；前端出**可复用分片上传客户端** + **实时进度条**，支持 4G+ 文件。P1。**本 FR 只加「上传机制」，不改发布编排**（编排重做归 FR-250，消费本 FR 的分片客户端）。

## 2. 需求（要什么）

### 范围内
- **后端分块协议**（JWT 平台管理员，与 `PublishFile` 同鉴权组）：
  - `POST /client-channels/:id/uploads`（init）：声明 `{filename,totalSize,chunkSize?}` → 建上传会话，返回 `{uploadId,chunkSize,chunkCount}`。
  - `PUT /client-channels/:id/uploads/:uploadId/chunks/:index`（chunk）：body 为该分片**原始字节**（`application/octet-stream`），`index` 0 基；**幂等**（重传同 index 覆盖，支持失败重试）。返回 `{received,total}`。
  - `POST /client-channels/:id/uploads/:uploadId/complete`（complete）：body `{codec?,expectedSha256?}` → 校验分片齐全 + 总字节匹配 → 顺序拼装喂入 CAS → 返回 `ClientFileResult{sha256,md5,size,codec}`（与单次上传同结构，前端可无缝替换）→ 清理临时分片。
  - `DELETE /client-channels/:id/uploads/:uploadId`（abort，可选但实现）：弃单、清临时。
- **临时分片落盘**：`<dataRoot>/cache/client-uploads/<uploadId>/<index>.part`（用 `dataroot.Root.CacheDir()`）。complete 用 `io.MultiReader` 顺序拼各 part → `ClientVersionService.PublishFile(multiReader, params)`（流式，**不额外落一份整文件**），Ingest 自算 sha256/md5/size。
- **会话治理**：内存会话表（mutex 守护）：uploadId、channelID、filename、totalSize、chunkSize、chunkCount、已收 index 集、createdAt/lastActivity、tempDir。后台 TTL 清理（空闲 > 1h 的弃单：删临时 + 移除会话），CP 启动清残留 `client-uploads/`。
- **前端分片客户端**（可复用）：`web/src/lib/chunkedUpload.ts` — `uploadFileChunked(channelId, file, {onProgress, signal}) => Promise<ClientFileResult>`：init → 按 chunkSize 切片顺序 PUT（每片完成回调进度）→ complete。`onProgress(uploadedBytes,totalBytes)`；`signal` 支持取消（AbortController → DELETE 弃单）。
- **接入现有发布页**：`ClientPublishPage.uploadOne` 由单次 `usePublishClientFile` 改走 `uploadFileChunked`，上传态展示**百分比进度条**（替换现「done/total 计数」）。行为不变：上传后仍得 `{sha256,md5,size}` 追加草稿。
- 纯函数抽取 + 测试先行：切片数学（chunkCount/边界/末片）、进度归并。
- i18n zh/en（只追加自己键块）；暗亮主题 token。

### 不做（范围外）
- 发布编排重做（拖拽本地预览树、延迟到「发布」才批量上传）——归 FR-250，消费本 FR。
- 断点续传跨 CP 重启（会话内存态；重启即弃单，前端重传。§6 记待定）。
- 压缩（codec 仍由调用方声明，本期发布走 `codec=none` 不变）。
- 玩家消费端点分块（玩家侧下载已由 `http.ServeContent` 的 Range 支持，无需改）。

## 3. 设计（怎么做）

### 3.1 后端
- **新服务** `internal/controlplane/service/client_chunk_upload.go`：`ChunkedUploadService`，持 `*dataroot.Root` + `*ClientVersionService`（复用其 `PublishFile` 落 CAS）+ `sync.Mutex` + `map[string]*uploadSession`。方法：`Init`、`WriteChunk`、`Complete`、`Abort`、后台 `Start/Stop`（TTL 清理）。`uploadId` 用 `crypto/rand` 生成 hex。分片校验：index 边界、body 大小（非末片须 == chunkSize）、complete 时齐全性 + 总字节。全程 `int64`，无 32 位截断。
- **新 handler** `internal/controlplane/router/client_chunk_upload.go`：`ChunkedUploadHandler`（**不碰** `client_version.go`），4 端点复用 `requirePlatformAdmin` + 频道存在校验 + 审计（complete 记 `client_file.publish` 同款，或新增 `client_file.publish_chunked`）。`RegisterRoutes(rg)` 挂在发布组下。
- **装配** `cmd/control-plane/main.go`：在 `clientVersionSvc` 就绪后建 `ChunkedUploadService`、`Start()`，建 handler、注册路由。
- 复用不改：`ClientVersionService.PublishFile`、`AssetService.Ingest` 签名与 CAS 去重逻辑原样。

### 3.2 前端
- `lib/chunkedUpload.ts`：切片 + 顺序 PUT（`fetch`，进度按「已完成片 * chunkSize + 当前片字节」归并；片粒度进度即可，字节级为增强）+ 取消。定义 `ClientFileResult` 类型复用 `@/api/clientVersions`。
- `api/clientVersions.ts`：加分块端点薄封装（init/complete）供 lib 调用；或全部内聚在 lib（择一，spec 内拍）。
- `ClientPublishPage.tsx`：`uploadOne` 改调 `uploadFileChunked`，`progress` 状态改百分比 + 进度条组件。zip 解包出的每个 entry 同样走分片上传。
- `mocks/handlers/domains/client.ts`：加 MSW 内存假后端的分块端点（供 dom 测试与 mock 模式）。

## 4. 任务拆分
- [ ] 后端切片/会话纯逻辑 + 单测（红→绿）：`client_chunk_upload_test.go`（init/chunk/complete/abort、乱序片、缺片报错、字节不符、TTL 清理、complete 结果与单次上传一致）
- [ ] `ChunkedUploadService`（服务） + `ChunkedUploadHandler`（4 端点） + 路由/装配
- [ ] 前端 `lib/chunkedUpload.ts` + 切片数学单测 + 进度/取消
- [ ] 接入 `ClientPublishPage`（进度条）+ MSW 分块端点 + dom 测试
- [ ] i18n zh/en；暗亮主题
- [ ] doc-sync：`docs/API.md`（4 新端点）、`docs/ARCHITECTURE.md`（分块上传临时区 `cache/client-uploads/`）、PRD FR-251「计划」→「开发中」（只改本行）、CHANGELOG `[Unreleased]` 末尾追加
- [ ] 中文 commit（feat(control-plane) 后端、feat(web) 前端拆 commit）

## 5. 验收标准
- 后端 `go build ./...` + `go test ./internal/controlplane/...` 绿；前端 tsc/eslint/build + vitest 绿。
- 分块 complete 返回的 `{sha256,md5,size}` 与同一文件走单次 `POST /files` **逐字段相同**（同一 CAS、内容寻址一致）——单测断言。
- 缺片 / 字节不符 / 越界 index → complete/chunk 明确报错，不产坏制品；abort 或 TTL 后临时目录清空。
- 前端上传显示**实时百分比**；可取消（取消后服务端弃单）。
- **【需真机，用户确认】** 浏览器实传一个 **> 2GB** 文件走通 init→chunk→complete，进度条推进、最终发布可用；中途取消能中止；zh/en + 暗亮正常。（真机为硬闸，单元/e2e 不替代。）

## 6. 风险 / 待定
- **临时磁盘占用**：上传中占 1x 文件大小于 `cache/`；TTL + abort 兜底清理；`cache/` 本就是数据根临时区。大批并发上传的总量守卫（max 会话/总字节）本期记待定，先靠管理员单点 + TTL。
- **跨重启续传**：会话内存态，CP 重启丢会话 → 前端整传重来（幂等 init 新会话）。跨重启续传留后续 FR。
- **反代 body 上限**：分片默认 8MiB 远低于常见反代上限；`chunkSize` 由 init 下发、前端遵从，避免单请求过大。
- **与 FR-250 的接口契约**：本 FR 的 `uploadFileChunked(channelId,file,{onProgress,signal})` 即 FR-250 延迟批量上传要消费的客户端；签名需稳定，FR-250 spec 将据此写。
