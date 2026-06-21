# API Spec — FR-070 文件管理资源管理器化（含批量下载 zip 端点）

> 关联 FR: FR-070 | 优先级: P1 | 状态: 🔨 in-progress | 关联 FR: FR-008, FR-020(rename 已存在), FR-051(版本/回滚), FR-059(危险确认)

## 概述

把实例文件管理重做为「资源管理器」：左树（懒加载子目录）+ 右内容列表/编辑器，多选/拖拽/批量/剪切粘贴/重命名/Ctrl+S 历史。**前端为主**，后端只新增一个能力——**批量打包下载（zip 流式）**。其余文件操作（list/read/write/upload/download/delete/rename/versions/diff/rollback）后端已全部存在，本 FR 仅在新 UI 中复用既有端点。

### 既有端点复用（不改）
| 端点 | 用途 | 关联 FR |
|---|---|---|
| `GET /instances/:id/files?path=` | 目录列表（树懒加载、内容列表） | FR-008 |
| `GET /instances/:id/files/read?path=` | 读文件内容（编辑器载入） | FR-008 |
| `POST /instances/:id/files/write` | 写文件（Ctrl+S 保存，改前自动快照 FR-051） | FR-008/051 |
| `POST /instances/:id/files/upload` | 上传（multipart，覆盖前自动快照） | FR-008/051 |
| `GET /instances/:id/files/download?path=` | 单文件流式下载 | FR-008 |
| `DELETE /instances/:id/files` | 删除文件/目录（递归） | FR-008 |
| `POST /instances/:id/files/rename` | 重命名/移动（`{oldPath,newPath}`，跨目录即移动） | FR-008/020 |
| `GET /instances/:id/files/versions?path=` | 版本列表 | FR-051 |
| `GET /instances/:id/files/diff?path=&from=&to=` | 版本 diff | FR-051 |
| `POST /instances/:id/files/rollback` | 回滚到指定版本 | FR-051 |

> **重命名即移动**：`RenameFile`(Worker `os.Rename`) 支持跨目录，故树内拖拽「移动」直接复用 `POST /files/rename`，无需新增 move 端点。

## 新增端点

### POST /api/v1/instances/:id/files/archive
- **描述**: 批量打包下载。把选中的若干文件/目录（目录递归）即时打包为 **zip** 流式返回。Worker 侧边遍历边打包边流式输出（不在 CP/Worker 全量缓冲整包），CP 把 gRPC 流转成 HTTP `application/zip` 响应体。
- **关联 FR**: FR-070
- **权限**: `instance.file`（可访问实例，与单文件 download 一致）
- **请求**: `{ "paths": ["plugins", "server.properties", "world/level.dat"] }`
  - `paths`：相对工作目录的条目集合，非空；每个条目经路径校验（禁 `..` / 禁前导 `/`）；目录递归纳入，文件直纳入。
- **响应**: `200`，`Content-Type: application/zip`，`Content-Disposition: attachment; filename="<instanceName>-files.zip"`；响应体为 zip 字节流（分块）。
  - zip 内条目名：以各 `paths` 条目为根的相对路径（如选 `plugins` 则内含 `plugins/xxx.jar`）。
  - 选单个文件时 zip 内为该文件名。
- **错误**:
  - `400 INVALID_REQUEST`：`paths` 为空或含非法路径（`..`/前导 `/`）。
  - `404 NOT_FOUND`：实例不存在 / 无访问权限。
  - `422 BUSINESS_ERROR`：节点离线 / 工作目录未设置 / 打包失败（流已开始后失败则截断连接，由前端按下载失败处理）。
- **说明**: 与单文件 `GET /files/download` 并存；前端多选或选目录时走本端点，单文件下载仍走 `download`。采用 POST 是因路径集合可能较长/含特殊字符，置于请求体更稳。

## gRPC 契约（Worker 新增）

`proto/worker.proto` `WorkerService` 追加：

```proto
// DownloadArchive 把选中的文件/目录（目录递归）即时打包为 zip 并分块流式返回（FR-070 批量下载）。
rpc DownloadArchive(DownloadArchiveRequest) returns (stream DownloadArchiveChunk);

message DownloadArchiveRequest {
  string instance_uuid = 1;
  repeated string paths = 2; // 相对工作目录的文件/目录集合
}

message DownloadArchiveChunk {
  bytes content = 1; // zip 字节分片
}
```

- Worker 实现：解析每个 `paths` 条目为绝对路径并 `validatePath` 校验（防越界），`archive/zip` 流式写入；遇目录 `filepath.Walk` 递归，仅打包常规文件（跳过符号链接/设备/锁文件无关）；边写边按 ~32KiB 分片 `stream.Send`。
- CP 实现：`FileService.DownloadArchive(instanceID, paths) (workerpb.WorkerService_DownloadArchiveClient, error)` 返回流；`FileHandler.DownloadArchive` 逐 `Recv()` 写 `c.Writer` 并 `Flush`。

## 数据模型

无新增表。批量下载不落库；版本表 `file_versions`（FR-051）已存在，不改。

## 前端契约（TanStack Query / api 客户端）

新增 `web/src/api/files.ts`（统一文件 ops hooks，替代 `FileBrowser` 内联调用）：
- `useFileList(instanceId, path)` → `GET /files`
- `readFile / writeFile / deleteFile / renameFile / uploadFile`（mutation 或裸函数）
- `downloadFile(instanceId, path)`（单文件，blob 下载）
- `downloadArchive(instanceId, paths[])`（POST `/files/archive`，`responseType:'blob'` → `application/zip` → `<a download>`）
- 版本相关复用既有 `web/src/api/fileVersions.ts`（不动）。

i18n：新增键并入 `files.*`（zh/en 对称）：`tree/newFile/newFolder/cut/copy/paste/move/selectAll/clear/download/downloadZip/selected/dropToUpload/saveHint(Ctrl+S)/moveSuccess/moveFailed/createSuccess/createFailed/nameExists/...`。
