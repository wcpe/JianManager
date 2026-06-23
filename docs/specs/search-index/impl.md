# FR-074 — 跨文件全文搜索与持久倒排索引 · 实现规格

> 状态：🔨 in-progress ｜ 关联 ADR-017 ｜ 依赖 FR-070（资源管理器）

## 目标

Worker 侧对实例工作目录建并增量维护**持久倒排全文索引**（落数据根 `var/index/`），提供：
- 跨文件关键字搜索（命中文件 + 行号 + 行片段）；
- 文件名快速打开（quick-open，按 basename/路径子串匹配）；
- 可配忽略规则（glob，默认排除 logs/、cache/、二进制/归档扩展等）。

经 gRPC `SearchFiles` 被 CP 委托查询，CP 加 `POST /instances/:id/files/search` 转发；前端资源管理器加搜索面板，命中点击跳编辑器定位到行。

## 架构红线（必须遵守）

- 索引是 **Worker 本地派生资产**，落 `<dataRoot>/var/index/<instance-uuid>/`，**绝不进 CP 数据库**。
- CP↔Worker 仅 gRPC（ADR-002）；CP 仅转发查询，不持有/不缓存索引。
- 文件归 Worker：索引构建/增量/查询全在 Worker 进程内。

## 引擎决策（见 ADR-017）

在树内最小倒排索引（纯 Go、零新增依赖）。bleve（依赖树过重）与 SQLite FTS5（纯 Go 驱动不带 FTS5、需 cgo）均否决。

## 后端实现

### `internal/worker/search`（新增包）
- `Index`：每实例一份倒排索引（token → 文件集合）+ 文件指纹表（path → size+mtime）。
- 持久化：gob 编码到 `<indexDir>/<instance-uuid>/index.gob`。
- `BuildOrUpdate(workDir)`：扫描工作目录，按指纹比对增量（新增/变化重索引、删除移除、未变跳过），落盘。
- `SearchContent(query, max)`：倒排取候选文件 → 候选内精确行扫描 → 命中（path/line/snippet）。
- `SearchFilename(query, max)`：文件名子串匹配（行号 0）。
- 忽略规则：默认 glob 集 + 二进制探测（NUL/非 UTF-8）+ 单文件体积上限 + 文件数上限；可配 glob 经 `search.ignore` 追加。

### 配置 `internal/worker/config.go`
- 新增 `Search.Ignore []string`（`worker.yaml` `search.ignore` / `JIANMANAGER_SEARCH_IGNORE`），追加到默认忽略集。

### proto `proto/worker.proto`
- 新增 `rpc SearchFiles(SearchFilesRequest) returns (SearchFilesResponse)`。
- `SearchFilesRequest{ instance_uuid, query, mode(content|filename), max_results }`。
- `SearchFilesResponse{ repeated SearchHit hits; bool truncated }`，`SearchHit{ path, line, snippet }`。
- protoc 重新生成（禁 sed）。

### Worker gRPC `internal/worker/grpc/search_ops.go`（新增）
- 实现 `SearchFiles`：取实例 workDir → 拿/建该实例 `search.Index` → 增量更新 → 查询 → 返回命中。
- Server 持有按实例 UUID 的索引表（`map[string]*search.Index` + 锁）与 `indexDir`（来自 root.IndexDir()）。

### CP `internal/controlplane/service/file.go` + `router/file.go`
- `FileService.SearchFiles(instanceID, query, mode, max)`：经 ClientPool 转发 gRPC。
- `POST /instances/:id/files/search`：鉴权 `canAccessInstance`，body `{ query, mode, maxResults }`，返回命中列表。

## 前端实现

### `web/src/api/files.ts`
- `searchFiles(instanceId, query, mode, maxResults)` → `POST /instances/:id/files/search`，返回 `SearchHit[]`。

### `web/src/components/explorer/`
- 搜索面板（工具栏入口或左栏标签）：输入关键字 + 模式切换（内容/文件名）→ 结果列表（文件/行/片段）→ 点击经既有 `openByPath` 打开文件并定位到行。
- `CodeEditor` 增加可选 `gotoLine` prop：打开后滚动并高亮到指定行。

### i18n
- `web/src/i18n/{zh,en}.json` 新增 `search.*` 键（仅加自己的键）。

## 测试（先行）

- `internal/worker/search/*_test.go`：建索引、增量更新（改/删/增文件）、内容搜索命中行+片段、文件名搜索、忽略规则（glob + 二进制跳过 + 体积上限）、持久化往返。
- `internal/worker/grpc/search_ops_test.go`：SearchFiles happy path（建临时 workDir + 实例 → 搜命中）。
- 前端：`searchFiles` 与面板纯逻辑（若有可单测的纯函数）vitest 覆盖。

## 任务清单

- [x] ADR-017
- [x] PRD FR-074 → in-progress
- [x] dataroot 加 `var/index` + IndexDir()
- [ ] proto SearchFiles + protoc 重生成
- [ ] worker/search 包 + 单测
- [ ] worker gRPC SearchFiles 接线 + 单测
- [ ] CP service + router 转发
- [ ] 前端 api + 面板 + 编辑器 gotoLine + i18n
- [ ] doc-sync（API.md / ARCHITECTURE.md）+ CHANGELOG
- [ ] 完成判据：go build/vet/test 绿；前端 tsc/lint/build 绿（真机难起标「待真机验」）

## 完成判据

- `go build ./...` 不 panic + `go vet` + `go test` 绿。
- 前端 `cd web && npm ci && npx tsc --noEmit && npm run lint && npm run build` 绿。
- 真机：建索引、搜命中、改文件增量、忽略规则生效（难起标「待真机验」）。
