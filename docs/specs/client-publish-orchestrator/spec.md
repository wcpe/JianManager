# 功能规格：客户端分发发布编排器重做（延迟批量上传）

> 状态：待审　·　关联 PRD：FR-250（增强 FR-191，依赖 FR-251）　·　关联 ADR：无（纯前端编排重做，复用 FR-251 分块上传 + 既有发布端点）　·　分支：feature/ui-docker-scale-2026-06-30（波2 单 FR，直接在基线开发）

## 1. 背景与目标

FR-191 把发布做成独立页面（`ClientPublishPage`），但**选文件即刻逐个上传**（`onPickFiles`→`uploadOne`→上传得 sha256 才入草稿）。真机短板：① 还没决定发不发就已上传，删文件 = 白传（费带宽）；② 只能选散文件/zip，**不能拖拽文件夹**保结构；③ 上传与编排耦合，改路径/删文件时文件早已在服务端。FR-251 已交付可复用分片上传客户端 `uploadFileChunked`。

**目标**：发布编排器改为**拖拽多文件/文件夹到浏览器 → 前端本地预览文件树（不上传）→ 调整层级/sync/platform → 点「发布」才批量上传（消费 FR-251 分块上传，带进度）→ 发布版本**。省带宽（未发布不上传、删除不上传）、支持文件夹拖拽保结构。P1。**纯前端，复用 FR-251 `uploadFileChunked` + 既有 `POST .../versions`，不改后端。**

## 2. 需求（要什么）

### 范围内
- **本地暂存、延迟上传**：选文件/拖拽后，文件以**浏览器内 `File` 对象**本地持有（草稿含 File 引用 + 本地元数据 name/size/相对 path，**无 sha256**——尚未上传）。文件树、路径/sync/platform 编排、删除全部在**本地草稿**上操作，零网络。
- **拖拽文件/文件夹**：支持把散文件、**文件夹**、zip 拖入落区（`DataTransfer` + `webkitGetAsEntry` 递归遍历目录树保相对路径）；保留「添加文件」「上传 ZIP」「选择文件夹」（`webkitdirectory`）入口。zip 仍前端 `fflate` 解包为本地草稿（不立即上传）。混合累加。
- **点发布才批量上传**：点「发布」→ 逐个 `uploadFileChunked(channelId, file, {onProgress, signal})` 批量上传（4G+ 走分块），聚合**总体进度**（已完成文件数/总数 + 当前文件字节 %）→ 得各 sha256/md5/size → 调 `usePublishClientVersion` 提交 `files/managedDirs/note`。上传中可**取消**（AbortController → 各 `uploadFileChunked` 弃单）。
- **省带宽**：发布前删除的文件**从不上传**；同一批内内容相同的文件（同 size+name 或本地 hash）只传一次（CAS 本就去重，前端亦跳过重复传）。
- **失败可重试不丢草稿**：某文件上传失败 → 提示 + 保留全部本地草稿 + 停在文件步/发布态，可重试（不回退已成功的、断点续批）。
- **预览步**：结构预览走本地草稿树（`ClientFileTree` 只读态）；内容预览从**本地 `File`**读文本（`FileReader`，二进制/超大降级「不可预览」）——不再依赖「已上传制品」（未传无 sha256）。
- **离开守卫**：有本地草稿（未发布）即 dirty，沿用 FR-191 的返回/刷新二次确认。
- 纯函数抽取 + 测试先行（`lib/client-publish-wizard.ts` 扩展：目录 entry 递归→草稿、本地去重、批量上传进度归并、dirty 判定）。i18n zh/en；暗亮主题；遵循 `.claude/rules/ui-modals.md`（页面级非模态）。

### 不做（范围外）
- 改后端上传/发布端点或 manifest 协议（复用 FR-251 分块端点 + `POST .../versions`）。
- 断点续传跨刷新（本地 `File` 引用刷新即失，dirty 守卫拦截；跨刷新续传留后续）。
- 压缩上传（codec 恒 none，与现状一致）。
- 文件内容编辑（仅编排 path/sync/platform）。

## 3. 设计（怎么做）

### 3.1 草稿模型改本地态（`ClientPublishPage.tsx` + `lib/client-publish-wizard.ts`）
- `DraftFile` 去掉「上传后才有」的 sha256/md5/codec，改为持 `file: File` + `path/sync/platform/size`；上传结果（sha256/md5）在**发布时**才产生（临时映射，不入草稿态）。
- `onPickFiles`/拖拽 handler **不再上传**——只把 File + 相对 path 入草稿。zip 前端解包为草稿。
- 目录拖拽：`e.dataTransfer.items[].webkitGetAsEntry()` → 递归 `FileSystemDirectoryEntry.createReader()` 收集 `FileSystemFileEntry` → `file()` 得 `File`，相对 path = entry.fullPath（归一 POSIX、去 `..`）。纯函数 `collectEntries` 便于测试。

### 3.2 发布批量上传（点「发布」触发）
- `doPublish`：建 `AbortController`；对每个草稿顺序 `uploadFileChunked(channelId, d.file, {onProgress: 归并到总体进度, signal})`；本地去重（同 key 复用首个上传结果）；全部成功 → 组 `ManifestFile[]`（path/sync/platform + artifact.sha256/size/codec=none）→ `publish.mutateAsync`。
- 进度 UI：总体「上传 x/N 文件 · 当前 42%」进度条；失败停批、标记失败文件、保草稿可重试。

### 3.3 预览（本地内容）
- 结构：`ClientFileTree`（只读）喂本地草稿。
- 内容：新增本地 `FileBrowserSource` 适配器（读 `File` 文本；NUL/超大降级），或复用 `FileBrowser` 喂本地源；较 FR-214 的「读已上传制品」改为「读本地 File」。

### 3.4 复用不改
- `uploadFileChunked`（FR-251）、`usePublishClientVersion`（`POST .../versions`）、`ClientFileTree`（编排/只读）、离开守卫（FR-191）原样复用。

## 4. 任务拆分
- [ ] `lib/client-publish-wizard.ts`：目录 entry 递归收集 / 本地去重 / 批量上传进度归并 / dirty（本地草稿）+ vitest（红→绿）
- [ ] `ClientPublishPage`：草稿改本地 File 态、拖拽落区（文件+文件夹）、延迟上传、发布批量上传 + 总体进度 + 取消 + 失败重试
- [ ] 本地内容预览源（读 File 文本，二进制/超大降级）接入预览步
- [ ] i18n zh/en 追加；暗亮主题；ui-modals 合规；dom 测试（拖拽入草稿不上传、发布才上传、删除不传、失败保草稿）
- [ ] doc-sync：`docs/ARCHITECTURE.md` 前端发布流程描述更新；PRD FR-250「计划」→「开发中」（只改本行）；CHANGELOG `[Unreleased]` 末尾追加
- [ ] 中文 commit（feat/refactor(web) 拆 commit）

## 5. 验收标准
- 前端 tsc/eslint/build + vitest 绿；后端无改动。
- 拖拽**文件夹**入页 → 按目录结构进本地预览树、**不上传**（Network 无上传请求）；散文件/zip/文件夹混合累加。
- 编排（改 path/sync/platform、删除）在本地即时生效、零网络；发布前删除的文件**不产生上传请求**。
- 点「发布」→ 批量分块上传（4G+ 可用）+ 总体进度 + 可取消；全部成功后版本发布、切 latest。
- 某文件上传失败 → 保留草稿可重试，不丢编排。
- **【需真机，用户确认】** 浏览器拖文件夹 → 编排 → 发布：发布前无上传请求、点发布才批量上传（大文件走分块进度）、发布成功玩家侧可拉取；中途取消能中止且弃单；zh/en + 暗亮正常。

## 6. 风险 / 待定
- **大批/大文件浏览器内存**：本地持有 `File` 对象仅持引用（不读全量进内存），实际字节在 `uploadFileChunked` 内按分片 `file.slice` 流式读，内存可控；超大批量的 UI 列表虚拟化本期不做（记待定）。
- **文件夹拖拽兼容**：`webkitGetAsEntry` 各浏览器支持良好（Chromium/Firefox/Safari），但为非标准 API；保留 `webkitdirectory` 选择器作等价入口兜底。
- **去重键**：本地去重用 name+size 近似（精确需读全量算 hash，费时）；CAS 服务端按真实 sha256 兜底去重，前端近似去重仅省重复传，误判不影响正确性（最坏多传一次）。
