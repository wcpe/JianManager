# 实施计划 — FR-070 文件管理资源管理器化 + 交互全集 + 编辑器基础 + Ctrl+S 历史

> 关联 FR: FR-070 | 优先级: P1 | 状态: 🔨 in-progress | 共享组件地基（FR-071/073/074/075/082/083/084 复用）

## 背景

现状（FR-008）：`web/src/components/FileBrowser.tsx` 单文件 335 行，左侧**平铺列表**（面包屑导航，非树）、右侧 `CodeEditor`（CodeMirror 6，仅 yaml/json 高亮）+ `FileVersionPanel`。无树、无多选、无拖拽、无剪切粘贴、无 Ctrl+S、无批量下载；删除/回滚用手搓内联弹窗（非 `DangerConfirm`）。

后端：文件 gRPC 全链路已存在，**`RenameFile` 已实现端到端**（FR-020 备注「缺 rename」已过时）。FR-051 版本/回滚后端 done（`file_versions` 表 + `GET/POST /files/versions|diff|rollback`）。仅缺**批量 zip 下载**。

本 FR 目标：重做为**可复用资源管理器组件**（左树懒加载 + 右内容/编辑器 + 交互全集），后端补一个 zip 流式下载端点。

## 设计要点

### 后端（最小）
- **gRPC `DownloadArchive`（server-stream）**：`proto/worker.proto` 加 RPC + 两条 message，重生成 `workerpb`。Worker `internal/worker/grpc/file_ops.go`（或新 `archive_ops.go`）实现：解析 `paths` → 各自 `validatePath`（复用既有，防 zip-slip/越界）→ `archive/zip` 流式打包（目录 `filepath.Walk` 递归、仅常规文件）→ ~32KiB 分片 `stream.Send`。参考 `backup_ops.go` 的 tar.gz 遍历与越界防御，改 zip。
- **CP 流代理**：`FileService.DownloadArchive` 返回 gRPC 流；`FileHandler.DownloadArchive`（`router/file.go`）逐 `Recv` 写 `c.Writer`+`Flush`，设 `application/zip` + `Content-Disposition`。新路由 `POST /instances/:id/files/archive`（加性追加，不重排）。
- **纯函数可测**：把「zip 内条目名计算」「paths 校验聚合」抽成可单测函数；Worker `DownloadArchive` 用真实 tmp 目录端到端测（建文件树→调用→读回 zip 校验条目）。

### 前端（主体）— 可复用 explorer 组件
目录 `web/src/components/explorer/`：
- **`ResourceExplorer.tsx`**（对外主组件，props: `instanceId`，未来可泛化数据源）：双栏布局（左树 + 右内容/编辑器），状态机统管选中/多选/剪贴板/编辑态。这是 FR-071 等复用的入口。
- **`FileTree.tsx`**：懒加载树。点目录展开→拉 `GET /files?path=`；HTML5 拖拽：把列表项/树节点拖到目录节点 → `rename`(=移动)。
- **`FileList.tsx`**：右侧目录内容表（多选行）。
- **`Toolbar.tsx`**：新建文件/夹、上传、下载、批量下载、删除、剪切/复制/粘贴、全选/清空。
- **`editor/`**：复用并增强 `CodeEditor.tsx`（加 Ctrl+S keymap、扩展语言高亮）。Ctrl+S 封装为 `useCtrlSave` 或编辑器 `Mod-s` 键位 `preventDefault`→ onSave。
- **纯逻辑抽到 `.ts` 兄弟文件**（vitest `environment: node`，无 jsdom）：
  - `selection.ts`：多选模型（shift 范围 / ctrl 切换 / 全选 / 清空），输入有序文件名 + 锚点 + 修饰键 → 新选中集。
  - `clipboard.ts`：剪切/复制/粘贴状态机（mode cut|copy + 源 paths + 目标目录 → 待执行操作列表；粘贴=rename(剪切)/暂仅 rename 移动 ；复制粘贴跨目录需后端 copy → **本 FR 范围内复制粘贴仅同名冲突检测 + 经 rename 实现移动；纯复制(copy)若无后端 copy 端点则置灰或走「下载后手动」**。见下「范围裁剪」）。
  - `paths.ts`：join/parent/basename/ext、相对路径规范化。
  - `language.ts`：扩展名 → CodeMirror 语言（yaml/json 现成；properties/toml/log/txt/md/sh/ini 用 StreamLanguage 或纯文本兜底）。

### 范围裁剪与决策（YAGNI / 不越界）
- **移动 = rename 跨目录**：树内拖拽移动直接 `POST /files/rename`，无需新后端。✅ 在范围内。
- **剪切粘贴**：剪切→粘贴=移动（rename）。✅ 可做。
- **复制粘贴（真复制）**：后端**无 copy 文件端点**（仅有 `CloneWorkDir` 整目录克隆，粒度不符）。在不新增后端 copy 的前提下，复制粘贴对**单文件**可「read→write 到目标」实现（CP 既有 read+write），对目录暂不支持（避免引入递归复制后端）。→ 决策：**复制粘贴支持单文件（read+write 组合，前端编排），目录复制本 FR 不做**（如需要登记后续 FR）。剪切（移动）文件与目录均支持（rename）。
- **多格式高亮**：yaml/json 用现成 lang 包；properties/ini/toml/log/sh 等用轻量 `StreamLanguage` 或纯文本+通用高亮兜底，不为每格式引重型依赖。
- **批量下载**：多选或选目录 → POST `/files/archive`（新端点）。

### 接入点
- `web/src/components/console/WorkspacePane.tsx`（segment==='files'）渲染 `<ResourceExplorer instanceId>` 替换 `<FileBrowser>`。
- `web/src/pages/InstanceDetailPage.tsx` files tab 同步替换。
- 保留旧 `FileBrowser.tsx`？→ 直接替换引用；旧文件删除（git 留史）。`FileVersionPanel.tsx` 复用（迁入历史抽屉），其内联回滚确认改用 `DangerConfirm`。

## 任务拆解

### 后端：批量 zip 下载
- [ ] `proto/worker.proto`：加 `DownloadArchive` RPC + `DownloadArchiveRequest{instance_uuid, repeated paths}` + `DownloadArchiveChunk{bytes content}`；`scripts/proto-gen.sh` 重生成 `workerpb`。
- [ ] Worker `internal/worker/grpc/archive_ops.go`：`DownloadArchive` 流式 zip 打包 + `validatePath` 每条目 + 目录递归；纯函数 `zipEntryName` 可测。
- [ ] CP `service/file.go`：`DownloadArchive(instanceID, paths)` 返回流（+ paths 校验聚合）。
- [ ] CP `router/file.go`：`DownloadArchive` handler（流代理）+ 路由 `POST /files/archive`（加性）。
- [ ] 单测：Worker `archive_ops_test.go`（建 tmp 树→打包→读回 zip 校验条目/内容/越界拒绝）；纯函数测 `zipEntryName`。

### 前端：可复用资源管理器
- [ ] shadcn 薄封装（radix-ui barrel 已装）：`ui/dropdown-menu.tsx`、`ui/context-menu.tsx`、`ui/scroll-area.tsx`（按需，不滥加）。
- [ ] `explorer/selection.ts` + `selection.test.ts`（shift/ctrl/全选/清空）。
- [ ] `explorer/clipboard.ts` + `clipboard.test.ts`（剪切/复制/粘贴编排、同名冲突）。
- [ ] `explorer/paths.ts` + `paths.test.ts`（join/parent/basename/ext/规范化）。
- [ ] `explorer/language.ts` + `language.test.ts`（扩展名→语言映射）。
- [ ] `api/files.ts`：文件 ops hooks + `downloadFile` + `downloadArchive`（blob）。
- [ ] `editor/CodeEditor` 增强：Ctrl+S keymap（`Mod-s` preventDefault→onSave）+ 扩展语言；`useCtrlSave` 测试钩（纯函数：键事件→是否触发保存）。
- [ ] `explorer/FileTree.tsx`（懒加载 + 拖拽移动）、`FileList.tsx`（多选）、`Toolbar.tsx`、`ResourceExplorer.tsx`。
- [ ] 拖拽上传（HTML5 dragover/drop + DataTransfer，批量多文件，沿用 upload 分块语义=逐文件 upload）。
- [ ] 历史版本：复用 `FileVersionPanel`（或抽屉化），回滚确认改 `DangerConfirm`；删除/批量删/覆盖确认接 `DangerConfirm`。
- [ ] 接入 `WorkspacePane.tsx` + `InstanceDetailPage.tsx`，移除旧 `FileBrowser.tsx` 引用。
- [ ] i18n `files.*` 扩充（zh/en 对称）。

### 文档
- [ ] `docs/API.md` 文件管理章节加 `POST /files/archive`；说明 rename 兼作移动。
- [ ] `docs/ARCHITECTURE.md`：通信协议章节加 `DownloadArchive` RPC；前端架构加 explorer 组件。
- [ ] `CHANGELOG.md` `[Unreleased]` 追加一行。
- [ ] PRD FR-070 状态 `📋 todo`→`🔨 in-progress`（交付由用户验收后置 done）。

## 验证（完成判据）
- 后端 `go build ./... && go vet ./... && go test ./...` 全绿（新增 archive 测试通过）。
- 前端 `cd web && npx tsc --noEmit && npm run lint && npm run test && npm run build` 全绿（新增 selection/clipboard/paths/language/ctrlsave 测试通过）。
- 真机（若本环境可起 CP+Worker）：拖拽上传 / 批量下载 zip / 多选删 / 重命名 / 剪切粘贴(移动) / Ctrl+S 存 + 版本回滚 端到端；不能起则如实报「待真机验」。
