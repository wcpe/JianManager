# 实施计划 — FR-071 配置管理资源管理器化 + 自动发现全部配置 + Ctrl+S 历史 + 收藏

> 关联 FR: FR-071 | 优先级: P1 | 状态: 🔨 in-progress | 依赖: FR-070、FR-031

## 背景

现状：「配置」段用独立组件 `web/src/components/ConfigEditor/index.tsx`（三栏：文件列表 / 文本·表单编辑 / 版本 diff），文件列表只列**顶层**内置可识别配置（Worker `ListConfigFiles` 非递归），无树、无多选、无拖拽/重命名/批量、无收藏。FR-070 已落共享 `components/explorer/ResourceExplorer.tsx`（左树右内容/编辑器 + 交互全集 + Ctrl+S 历史 + CodeMirror 多格式高亮）。

本 FR 目标：**配置段复用 `ResourceExplorer`**，叠加配置专属能力（递归发现全部配置、schema 双模式、Ctrl+S 写配置版本、跨文件校验、收藏），后端仅加一个递归发现端点（不改 proto）。

## 设计要点

### 复用策略（核心）—— 给 ResourceExplorer 加可选「配置增强」

不重写资源管理器。给 `ResourceExplorer` 增**可选 props**（默认关闭，FR-070 文件段行为不变）：

```ts
interface ResourceExplorerProps {
  instanceId: number
  /** 配置增强（FR-071）：开启后叠加 schema 双模式编辑、配置版本、收藏、已发现配置面板。 */
  config?: ConfigCapabilities
}
interface ConfigCapabilities {
  /** 打开文件时渲染配置编辑器（schema→表单/文本双模式 + 跨文件校验），返回 null 走默认 CodeEditor。 */
  renderEditor: (args: { instanceId; path; name; onClose; onAfterSave }) => ReactNode | null
  /** 收藏栏 + 已发现配置面板（嵌入左栏顶部）。 */
  sidebarExtra?: ReactNode
  /** 编辑器是否走配置编辑器（决定打开文件时用配置版本抽屉而非文件版本抽屉）。 */
  useConfigEditor?: boolean
}
```

- **文件段（FR-070）**：不传 `config`，行为完全不变。
- **配置段（FR-071）**：`ConfigExplorer` 包装组件传入 `config`，把打开的文件交给**配置编辑器** `ConfigFileEditor`（从既有 `ConfigEditor` 抽出单文件编辑逻辑：schema→表单/文本双模式 + 校验 + 跨文件校验 + 提交说明 + Ctrl+S 经 `useConfigSave`），并在左栏顶部插入**收藏栏 + 已发现配置面板**。
- 树/列表/工具栏/选择/剪贴板/拖拽/批量/重命名/删除/上传/下载 **全部复用** `ResourceExplorer` 既有逻辑。

为最小化对 `ResourceExplorer` 的侵入，仅抽一个「编辑器插槽」与「左栏额外内容插槽」+「打开文件后用哪套版本抽屉」开关。其余不动。

### 前端组件（新增，目录 `web/src/components/config-explorer/`）
- `ConfigExplorer.tsx`：对外入口（props: `instanceId`）。组合 `ResourceExplorer` + 配置增强。替换 `WorkspacePane`/`InstanceDetailPage` 中 `config` 段对 `ConfigEditor` 的引用。
- `ConfigFileEditor.tsx`：单文件配置编辑器（schema 表单 ↔ 文本双模式 + 校验徽标 + 跨文件校验 + 提交说明 + 保存/撤销 + Ctrl+S）。逻辑搬自 `ConfigEditor`，去掉其自带的文件列表/版本栏（版本走 `ResourceExplorer` 的抽屉）。
- `FavoritesBar.tsx`：收藏列表（点跳转打开 + 取消收藏）+ 已发现配置面板（递归发现结果，点收藏/打开）。
- `ConfigVersionDrawer.tsx`：配置版本抽屉（复用 FR-031 `useConfigVersions/useConfigDiff/useRollbackConfig`），供 `ResourceExplorer` 配置模式使用（与 FR-070 的文件 `VersionDrawer` 区分：配置版本表 `instance_config_versions`）。

### 前端纯逻辑（新增，可 vitest，`environment: node`）
- `favorites.ts` + `favorites.test.ts`：收藏读写（localStorage 封装：`load/add/remove/toggle/has`，去重 + 容错坏 JSON + SSR/无 storage 兜底）。纯函数（storage 经参数注入便于测）。
- `discover.ts` + `discover.test.ts`：发现结果排序/分组纯函数（按目录分组、schema 优先、稳定排序）。

### 前端 API（`web/src/api/configs.ts` 加性追加）
- `useConfigDiscover(instanceId)`：`GET /instances/:id/configs/discover`，返回 `{ files, truncated }`。
- 复用既有 `useConfigRead/useWriteConfig/useWriteConfigFields/useCrossCheck/useConfigVersions/useConfigDiff/useRollbackConfig`。

### 后端（最小，不改 proto）
- CP `service/config.go`：`Discover(instanceID) ([]DiscoveredConfig, bool, error)` —— 经既有 `Worker.ListFiles` 逐目录广度遍历工作目录，`isConfigFile` 过滤，命中内置 schema（`schema.MatchPath`）置 `supported`。深度上限 8、目录上限 2000，超限 `truncated=true`。把遍历核心抽为可测纯函数 `walkConfigPaths(listDir func(dir)([]entry,error), limits)`。
- CP `router/config.go`：`Discover` handler + 路由 `GET /instances/:id/configs/discover`（加性，置于通配 `*file` 路由**之前**，避免被吞）。
- 单测：`config_discover_test.go` 用 fake `listDir` 构造目录树 → 校验扁平结果 / 深度截断 / 目录上限截断 / 非配置过滤。

### 范围裁剪与决策（YAGNI / 不越界）
- **不改 proto**：递归发现走 CP 服务层经既有 `ListFiles` gRPC，不加 `recursive` 字段。
- **收藏前端 localStorage**：书签为 UI 便利项，不增 DB 表（见 api.md 理由）。
- **不重写资源管理器**：仅给 `ResourceExplorer` 加可选编辑器/左栏插槽 + 版本抽屉开关。
- **配置写入走配置端点**：schema 文件文本模式 → `POST /configs/write`；表单 → `/configs/write-fields`；二者均生成**配置版本**（满足「Ctrl+S 存+配置版本」）。非 schema 配置文件文本模式也走 `/configs/write`（统一进配置版本表）。
- **跨文件校验保留**：配置编辑器内「校验」按钮经 `useCrossCheck`。

### 接入点
- `web/src/components/console/WorkspacePane.tsx`：`segment==='config'` → `<ConfigExplorer instanceId>` 替换 `<ConfigEditor>`。
- `web/src/pages/InstanceDetailPage.tsx`：config tab 同步替换。
- 旧 `components/ConfigEditor/index.tsx`：逻辑迁入 `ConfigFileEditor` 后**删除**（git 留史），更新引用。

## 任务拆解

### 后端：递归发现
- [x] `service/config.go`：`DiscoveredConfig` 类型 + `walkConfigPaths`（纯函数，BFS）+ `Discover` 方法（经既有 `Worker.ListFiles`）。
- [x] `router/config.go`：`Discover` handler + 路由 `GET /configs/discover`（加性，置于 `*file` 通配前）。
- [x] `config_discover_test.go`：纯函数 `walkConfigPaths` 表驱动测（扁平/supported 标记/深度截断/目录上限截断/非配置过滤/不可读目录跳过）。

### 前端：配置资源管理器
- [x] `ResourceExplorer.tsx`：加可选 `config: ConfigCapabilities` props（编辑器插槽 `renderEditor` + 左栏额外插槽 `sidebarExtra` + 配置版本抽屉 `renderVersionDrawer` + 按路径打开 `openPathRef`）；不传时行为不变。
- [x] `config-explorer/ConfigFileEditor.tsx`：单文件 schema 双模式编辑（文本模式复用共享 `CodeEditor` 含 Ctrl+S/多格式高亮；表单字段级补丁；跨文件校验；提交说明/保存/撤销）。
- [x] `config-explorer/FavoritesBar.tsx`：收藏 + 已发现配置面板（分组/筛选/收藏切换/打开）。
- [x] `config-explorer/ConfigVersionDrawer.tsx`：配置版本抽屉（FR-031 版本/diff/回滚）。
- [x] `config-explorer/ConfigExplorer.tsx`：组合入口（owns 收藏 localStorage 状态）。
- [x] `config-explorer/favorites.ts` + `.test.ts`。
- [x] `config-explorer/discover.ts` + `.test.ts`。
- [x] `api/configs.ts`：`useConfigDiscover`。
- [x] 接入 `WorkspacePane.tsx` + `InstanceDetailPage.tsx`，删除旧 `ConfigEditor`。
- [x] i18n `configExplorer.*`（zh/en 对称 27 键）。

### 文档
- [x] `docs/API.md`：新增「配置管理」章节（`GET /configs/discover` + 复用的 FR-031 配置端点 + 配置段复用资源管理器说明）。
- [x] `docs/ARCHITECTURE.md`：前端架构「配置」段注明复用 `ResourceExplorer` + 配置增强能力。
- [x] `CHANGELOG.md` `[Unreleased]` `新增` 末尾追加一条（只加不改）。
- [x] PRD FR-071 状态 `📋 todo`→`🔨 in-progress`（仅改该行；done 由用户验收）。

## 验证（完成判据）
- 后端 `go build ./...`、`go vet ./...` 全绿；`go test ./internal/controlplane/...`、`./internal/worker/grpc/...`、`./proto/...` 全绿（新增 `walkConfigPaths` 6 个用例通过）。`go test ./...` 仅 `TestManager_DaemonRecover` 偶发失败（Windows 临时目录清理竞态，进程管理包既有 flaky，与本 FR 无关，重跑通过）。
- 前端 `cd web && npx tsc --noEmit && npm run lint && npm run test && npm run build` 全绿（167 测试通过，含新增 favorites 12 + discover 6）。
- 真机（发现全部配置 / 编辑非 schema / Ctrl+S 存 + 配置版本回滚 / 收藏 + 重命名 + 多选）：本环境难起 CP+Worker → **待真机验**。
