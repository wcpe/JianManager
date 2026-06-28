# 功能规格：共享「文件浏览器」前端组件 + 实例资源卡片迁移

> 状态：开发中　·　关联 PRD：FR-213　·　分支：feat/fr-213-file-browser
> 关联：FR-070（资源管理器地基，复用其 `explorer/*` 原语）、FR-075（归档只读预览范式）、FR-191（`ClientFileTree` 扁平 manifest 树范式）、FR-214（下游消费方：客户端分发文件预览）

## 1. 背景与目标

当前「内容预览 + 目录树 + 下载」这套**只读浏览**能力被复制在多处、且都与各自后端**硬耦合**：

- 实例文件管理 `explorer/ResourceExplorer`（FR-070）：右栏预览/编辑、左树、下载全部直连 `@/api/files`（实例级端点 `/instances/:id/files*`）。
- 归档浏览 `explorer/ArchiveViewer`（FR-075）：条目树 + 只读 `CodeEditor` 预览 + 二进制降级，直连 `@/api/archive`。
- 客户端分发 `ClientFileTree`（FR-191）：扁平 `ManifestFile[]` → 目录树**只读展示**（无内容预览），直连 manifest 数据。

FR-214（客户端分发文件预览）需要在**发布页已上传文件 / 版本详情历史文件树**上预览内容，其后端是 **CAS 内容寻址 + manifest**（与实例工作目录完全不同的数据源，且**只读**）。若继续各写一套，预览/降级/高亮逻辑三处发散。

**目标**：抽出一个**展示型、数据源经 props 注入、不耦合具体后端**的共享「文件浏览器」组件 `web/src/components/file-browser/FileBrowser.tsx`，统一「目录树/列表 + 内容预览（文本/配置/json 高亮，二进制/大文件降级）+ 下载」这层只读浏览能力；并把**实例「资源卡片」**接到该共享组件作为其只读浏览/预览/下载视图——**行为不变、能力不减**。为 FR-214 提供可直接喂不同后端的底座。

属阶段：P1。

## 2. 需求（要什么）

### 范围内
1. 新增展示型组件 `web/src/components/file-browser/`：
   - 目录树 / 列表渲染（支持两种数据源形态：**懒加载分层**目录数据源 + **扁平全量**文件清单）。
   - 内容预览：文本 / 配置（yaml/properties/ini/toml）/ json 语法高亮（复用既有 `explorer/editor/CodeEditor` 只读模式 + `explorer/language.ts` 高亮）。
   - 降级：**二进制**（非 UTF-8 文本）与**超大文件**（超过阈值）降级为「不可预览 + 仅下载」占位，不尝试渲染。
   - 下载：单文件下载经 props 回调（组件不假设端点）。
   - `readOnly`（默认 true，纯浏览）与「可操作」（注入操作回调时显示操作入口，如下载/可选的重命名/删除——本组件只暴露**回调挂载点**，不内置实例写端点）。
2. 数据源 / 操作经 **props 注入**：列出目录 / 读内容 / 下载 / （可选）操作，均由调用方提供；组件本身**不 import** `@/api/files`、`@/api/archive` 或任何具体后端 api。
3. 实例「资源卡片」迁移：实例卡片的**只读浏览 + 内容预览 + 下载**这层改由共享 `FileBrowser` 承载，喂以实例文件数据源适配器（基于既有 `@/api/files`）。**实例既有文件管理能力（增删改查 / 上传 / 下载 / 文本与配置编辑 / 配置版本 / 跨文件校验 / 收藏 / 已发现配置 / 搜索 / 归档浏览 / 反编译）一个不少**（这些写/编辑/版本能力仍由 `explorer/ResourceExplorer` + `config-explorer/*` 提供，FR-213 不删除、不降级它们）。
4. i18n：新增 `fileBrowser.*` 键块（zh/en 对称）；预览降级、空态、加载、下载等文案。
5. 暗/亮 + 双主题：仅用设计 token（`text-muted-foreground`/`bg-card` 等），不写死色值。
6. 遵循 `ui-modals`：本组件为内嵌浏览视图，非「创建/编辑/配置」表单弹出，不引入内联展开表单；如未来需要弹出预览则套 `Dialog`（本 FR 内嵌呈现，无需模态）。

### 不做（范围外）
- **不接客户端分发**（FR-214）：本 FR 只交付组件 + 实例卡片迁移；客户端分发发布页/版本详情接入由 FR-214 复用本组件完成。
- **不动后端 / 不加端点 / 不动 proto**：纯前端组件抽取与接线，复用既有端点。
- **不改实例文件操作的语义/端点**：迁移只换「只读浏览/预览/下载」的承载组件，写路径（write/upload/delete/rename/版本/配置）保持现状不动。
- **不重写 `ResourceExplorer`/`ConfigExplorer`/`ArchiveViewer`/`ClientFileTree` 的现有行为**：可让它们在只读预览处**复用** `FileBrowser` 的展示原语，但不得回归其既有能力（最小风险优先）。
- 不引入新高亮依赖（复用 `explorer/language.ts` 既有 yaml/json/properties/toml StreamLanguage）。

## 3. 设计（怎么做）

### 3.1 组件契约（props / 接口）

目录 `web/src/components/file-browser/`：

- **`FileBrowser.tsx`**（对外主组件，展示型）：双栏「左树/列表 + 右预览」。统管「当前选中文件 / 当前目录 / 预览态」纯 UI 状态；**所有数据经 `source` 注入，所有动作经回调注入**。

```ts
/** 文件浏览器内一个节点（目录或文件），与具体后端无关。 */
export interface FileEntry {
  /** 相对根、以 "/" 分隔的路径（唯一键）。 */
  path: string
  /** 展示名（通常为 path 末段）。 */
  name: string
  /** 是否目录。 */
  isDir: boolean
  /** 文件字节大小（目录可省略/为 0）。 */
  size?: number
  /** 修改时间（unix 秒，可选，仅展示）。 */
  modTime?: number
}

/** 预览内容结果（由 source.readContent 解析后返回；降级由本类型显式表达，组件不再猜）。 */
export type PreviewContent =
  | { kind: 'text'; content: string; truncated?: boolean }  // 可高亮文本（含配置/json）
  | { kind: 'binary' }                                       // 二进制：不可预览，仅下载
  | { kind: 'too-large'; size: number }                     // 超大：不可预览，仅下载
  | { kind: 'error'; message: string }                      // 读取失败

/** 文件浏览器数据源（与后端解耦；实例 / 客户端分发各自提供一个实现）。 */
export interface FileBrowserSource {
  /**
   * 列目录。两种形态：
   * - 懒加载分层：传入目录 path，返回该层直接子项（实例工作目录用）。
   * - 扁平全量：忽略 path，一次性返回全部条目（manifest 用，由组件内部建树）。
   * 形态由 source.flat 标记。
   */
  list: (dirPath: string) => Promise<FileEntry[]>
  /** 数据源是否为「扁平全量」（true 时 list 一次返回全部，组件内部 buildTree）。 */
  flat?: boolean
  /** 读取文件内容用于预览；由 source 负责二进制/超大判定并返回对应 PreviewContent。 */
  readContent: (entry: FileEntry) => Promise<PreviewContent>
  /** 下载单文件（source 触发浏览器下载；省略则不显示下载入口）。 */
  download?: (entry: FileEntry) => void | Promise<void>
}

export interface FileBrowserProps {
  /** 数据源（注入）。 */
  source: FileBrowserSource
  /** 只读浏览（默认 true）。false 时显示注入的额外操作入口。 */
  readOnly?: boolean
  /**
   * 额外行操作（可操作态）。每项渲染为文件行的操作按钮/右键项；
   * 组件不内置任何写端点，全部经此注入（如重命名/删除/编辑）。
   */
  actions?: FileBrowserAction[]
  /** 选中文件变化回调（可选，供外部联动）。 */
  onSelect?: (entry: FileEntry | null) => void
  /** 数据刷新信号：值变化时重新拉取（增删改后由外部递增）。 */
  refreshKey?: number
  /** 容器高度类（默认自适应父容器 h-full）。 */
  className?: string
}

export interface FileBrowserAction {
  key: string
  label: string
  icon?: ReactNode
  /** 仅对满足条件的条目显示（如仅文件）。 */
  visible?: (entry: FileEntry) => boolean
  onAction: (entry: FileEntry) => void
}
```

子组件（`file-browser/` 内）：
- **`FileBrowserTree.tsx`**：目录树/列表渲染。懒加载形态点目录展开拉 `source.list`；扁平形态用内部 `buildTree(entries)` 一次成树（移植/复用 `lib/client-publish-wizard` 的 `buildFileTree` 思路，但泛化到 `FileEntry`）。
- **`FilePreview.tsx`**：右栏预览。`PreviewContent.kind==='text'` → `CodeEditor readOnly`（高亮经 `languageExtensionFor(name)`）；`binary`/`too-large` → 居中占位「不可预览 + 下载按钮」；`error` → 错误文案；未选中 → 引导占位。
- **纯逻辑 `tree.ts` + `tree.test.ts`**：`buildTree(entries: FileEntry[]): TreeDir`（扁平→层级，含子树规模统计），node 环境可测，不依赖 jsdom。

### 3.2 预览类型与降级判定（口径）

- **谁判定**：由 `source.readContent` 适配器判定并返回 `PreviewContent`，组件按 kind 渲染。这样不同后端各自决定「什么算二进制 / 多大算超大」，组件只消费判定结果（关注点分离）。
- **实例适配器口径**（`file-browser/sources/instanceSource.ts`）：
  - 文本：经 `GET /files/read` 取文本；高亮语言由文件名后缀决定（`explorer/language.ts`）。
  - 二进制：复用既有 `explorer/paths.ts` / 既有判定——本 FR 采用「按后缀黑名单 + 读取结果含 NUL 字节」简单判定（与 `ArchiveViewer` 二进制范式一致）。无需新端点。
  - 超大：内容字节数（或 `FileInfo.size`）超过阈值 `PREVIEW_MAX_BYTES`（默认 1 MiB，常量）→ `too-large`，不读全量。
- **下载兜底**：任何降级态都提供下载入口（经 `source.download`），即"不可预览必可下载"。

### 3.3 实例资源卡片迁移（行为不变）

现状：实例「资源卡片」= `console/WorkspaceCardBody.tsx`（`case 'resource'`）→ `config-explorer/ConfigExplorer` → `explorer/ResourceExplorer`（FR-070/071，全功能）。

迁移策略（**最小风险、零能力回归**）：
- 保留 `ResourceExplorer` + `ConfigExplorer` 作为实例卡片的**全功能文件 / 配置管理器**（增删改查 / 上传 / 编辑 / 配置版本 / 校验 / 收藏 / 发现 / 搜索 / 归档 / 反编译均不动）。
- **抽取共享原语**：把「只读内容预览 + 二进制/超大降级 + 高亮」这层从 `ArchiveViewer` 内联实现下沉为共享 `FilePreview`，`ArchiveViewer` 改为复用 `FilePreview`（行为等价：原 `archive.binaryNotice` 降级 → `PreviewContent.kind==='binary'`）。`ResourceExplorer` 默认 CodeEditor 预览/编辑路径**保持不动**（它是可编辑视图，非纯预览）。
- **资源卡片接入共享 FileBrowser**：在实例「资源卡片」内提供一个基于 `@/api/files` 的 `instanceSource` 适配器，使共享 `FileBrowser` 能独立用于实例工作目录的**只读浏览/预览/下载**（验证迁移可用、为 FR-214 范式背书）。**资源卡片对用户暴露的能力集合不变**：编辑/写/版本仍走 `ConfigExplorer`，浏览/预览/下载经共享组件——二者并存，用户可用功能一个不少。

> 决策记录：FR-213「实例资源卡片迁移到共享组件」取「**抽取并复用展示原语 + 提供实例数据源适配器**」而非「用纯展示组件替换全功能管理器」——后者会删除编辑/版本/配置/搜索能力，违反 PRD「行为不变 / 一个不少」与 `scope-discipline`。本决策不新增 ADR（无跨进程/架构层决策，属前端组件分层）。

### 3.4 复用边界

| 既有 | FR-213 处理 |
|---|---|
| `explorer/editor/CodeEditor`（只读高亮预览） | **复用**（`FilePreview` 内 `readOnly`） |
| `explorer/language.ts`（后缀→高亮） | **复用** |
| `explorer/paths.ts`（join/base/ext） | **复用** |
| `lib/client-publish-wizard` `buildFileTree`（扁平→树） | 思路**移植/泛化**到 `file-browser/tree.ts`（泛型 `FileEntry`；不改 FR-191 既有用法） |
| `ArchiveViewer` 二进制降级内联 | **下沉**为 `FilePreview` 共享降级（行为等价） |
| `@/api/files`、`@/api/archive` | 仅在**适配器**中 import，组件主体不依赖 |

## 4. 任务拆分

- [ ] `file-browser/tree.ts` + `tree.test.ts`（扁平→层级建树，node 可测）。
- [ ] `file-browser/FilePreview.tsx`（文本高亮 / binary / too-large / error / 空态）。
- [ ] `file-browser/FileBrowserTree.tsx`（懒加载 + 扁平两形态树/列表）。
- [ ] `file-browser/FileBrowser.tsx`（主组件，props 契约如 §3.1）。
- [ ] `file-browser/sources/instanceSource.ts`（实例数据源适配器，基于 `@/api/files`，含二进制/超大判定）。
- [ ] 实例资源卡片接入：在资源卡片提供只读浏览/预览/下载（共享 FileBrowser），保留 `ConfigExplorer` 全功能；行为不变。
- [ ] （可选、不回归前提下）`ArchiveViewer` 复用 `FilePreview` 降级原语。
- [ ] i18n `fileBrowser.*`（zh/en 对称）。
- [ ] vitest 组件测：树渲染、预览切换（文本↔降级）、readOnly vs 可操作、binary/too-large 降级、下载回调触发。
- [ ] 既有 `explorer/*`、`config-explorer/*` 测试仍绿（迁移不回归）。
- [ ] 文档同步：PRD FR-213「计划」→「开发中」；ARCHITECTURE 前端组件章节（如有）补共享文件浏览器；本 spec 任务勾选。

## 5. 验收标准

- 组件 `FileBrowser` 可仅凭注入 `source` 渲染树 + 预览 + 下载，**主体不依赖任何具体后端 api**（import 审查通过）。
- 预览：文本/配置/json 有语法高亮；二进制与超大文件降级为「不可预览 + 下载」占位且下载可用。
- `readOnly` 默认纯浏览；注入 `actions` 后出现对应操作入口。
- 实例「资源卡片」迁移后，**既有文件能力一个不少**（增删改查/上传/下载/预览/文本与配置编辑/配置版本/校验/收藏/发现/搜索/归档/反编译均可用）——由既有 `explorer/*`、`config-explorer/*` 测试保持全绿守护 + 新增组件测覆盖浏览/预览/下载。
- 硬闸：`cd web && npm ci && npx tsc --noEmit && npm run lint && npm run build && npx vitest run` 全绿。
- **真机维度（需用户确认）**：实例「资源卡片」真机点——浏览目录、预览文本/配置、二进制/超大降级、下载、并确认编辑/版本/上传等既有能力未回归。标「待真机验」，纯绿不替代。

## 6. 风险 / 待定

- 实例资源卡片是高频核心组件，迁移须**行为不变**——靠既有 `explorer`/`config-explorer` 测试守护 + 不删原能力（并存而非替换）。
- 二进制判定口径（后缀黑名单 + NUL 字节）为启发式，可能误判极少数文本；降级仍可下载，无数据风险；后续如需精确可由 source 适配器升级，不影响组件契约。
- `buildTree` 泛化勿破 FR-191 `ClientFileTree` 既有用法（保留 `lib/client-publish-wizard` 原函数，新建 `file-browser/tree.ts` 泛型版，二者独立）。
