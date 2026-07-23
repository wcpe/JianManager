# 功能规格：资源管理器多窗壳（多标签 + 浮动 + 浏览器新标签）

> 状态：开发中（自动化测绿）　·　关联 PRD：FR-376　·　分支：feature/fr-376-file-explorer-multi-window  
> 依赖：FR-375（单窗地址栏/历史/滚动/权限列契约）  
> 下游：FR-377（跨窗 DnD/剪贴板）、FR-378（统一壳全场景接入）  
> 计划：`.tmp/brainstorm-file-explorer-perm-ux-2026-07-23.md`  
> **本 FR 不新增 ADR**（窗口宿主与路由深链属前端壳；跨标签通信协议留给 FR-377 + ADR）

## 1. 背景与目标

### 问题
- 实例控制台「文件/配置」页签内只有**单一** `ResourceExplorer` / `ConfigExplorer` 实例：同时对照两个目录、边改配置边看日志文件时只能来回切，或开多个浏览器窗口却无法深链到具体 path。
- 无应用内**浮动**资源窗：对照查看时必须占满主区或离开控制台。
- 现有深链仅到 `/instances/:id?tab=resource`（`InstanceConsolePage` 的 `?tab=`），**不能**直接打开 `plugins/Essentials/config.yml` 一类相对路径。

### 目标（多窗壳，仍限实例文件/配置主路径）
1. **主区内多标签**：同一实例可开多个资源管理器标签（目录或文件上下文），切换保留各自导航栈与未保存草稿守卫。  
2. **应用内浮动窗**：从标签或工具栏「弹出」为可拖拽/缩放的浮动资源窗，可「收回」回主区标签；弹出/收回不丢 dirty。  
3. **浏览器新标签**：独立路由深链打开同一实例 + 相对 path（及可选打开文件），可单独分享/并排。

属阶段：P1。跨窗 DnD/统一剪贴板（FR-377）、平台存储/分发统一壳（FR-378）不在本 FR。

## 2. 需求（要什么）

### 范围内

1. **主区多标签（资源域）**  
   - 宿主：实例控制台 `tab=resource`（及「管理」=`ConfigExplorer` 同源壳，见 §3.1）。  
   - 标签项语义：至少绑定 `{ id, title, currentDir, openFilePath? }`；标题默认取路径末段或「文件」。  
   - 操作：新建标签（默认根 `/`）、关闭标签（有 dirty 走既有 discard 确认）、切换标签。  
   - 每个标签**独立** `ResourceExplorer` 状态（导航历史、选中、打开文件、dirty）；关闭销毁该实例。  
   - 标签数软上限：**8**（超限 toast，不静默丢）；YAGNI 不做标签拖拽排序（可后续）。

2. **应用内浮动窗**  
   - 从当前标签「弹出」→ 主区该标签变为「已浮动」占位或自动切到邻签；内容迁入浮动层。  
   - 浮动层：可拖标题栏移动、右下角缩放；最小约 480×360；z-index 高于控制台内容、低于全局 Dialog/DangerConfirm。  
   - 「收回」：内容回主区标签条，导航/编辑态保留。  
   - 关闭浮动 = 关该资源会话（同关标签，dirty 守卫）。  
   - 同时浮动数软上限：**3**。

3. **浏览器新标签路由**  
   - 新路由（建议）：`/instances/:id/files` 查询参数：  
     - `path`：相对工作目录目录路径（空=根）  
     - `file`：可选，相对路径文件，打开编辑/预览  
     - `mode`：可选 `files` | `manage`（默认 `files`；`manage` 走 ConfigExplorer 能力）  
   - 入口：工具栏/标签菜单「在浏览器新标签打开」→ `window.open` 同源 URL（带鉴权 cookie 即可，不拼 token）。  
   - 该页渲染**完整**资源管理器壳（含 FR-375 工具栏），无控制台其它页签噪声；顶栏可显示实例名 + 返回控制台链接。  
   - 非法 path：toast + 落到根，不白屏。

4. **脏态与 keep-alive 纪律**  
   - 沿用 `reportInstanceDraft` / `needsDiscardConfirm`：多标签时按「标签 id」登记草稿 key（如 `resource-tab:${tabId}`），实例级「有草稿」仍为任一脏。  
   - 与 ADR-067 兼容：资源页签整体仍可被 `<Activity>` 隐藏；**不**为每个资源标签再开终端式全局单例。  
   - 跨服 LRU 淘汰带草稿时既有 toast 警示不变。

5. **i18n**  
   - `files.tabs.*` / `files.float.*` / `files.openInBrowserTab` 等 zh/en 对称。

6. **测试**  
   - 单元：标签 store 纯函数（增/关/切换/上限）。  
   - DOM：多标签切换保留目录；弹出/收回；深链路由 `path`/`file` 打开。  
   - 不要求真机测浮动拖拽像素级，真机项：新标签深链 + 双标签对照。

### 不做
- 跨浏览器标签 / 主区↔浮动 **DnD 与统一剪贴板**（FR-377）。  
- 平台存储、分发编排树统一壳（FR-378）。  
- 真实桌面 OS 窗口管理、标签持久化到 localStorage 跨会话。  
- 改后端 API / gRPC。

## 3. 设计（怎么做）

### 3.1 宿主分层

```
InstanceConsolePage (tab=resource)
  └─ InstanceResourceCard          # 管理 | 文件 | 浏览 三视图保持
       ├─ manage → ConfigExplorer  # 本 FR：Config 侧可复用同一 TabHost（可选同 PR，优先 files 视图）
       ├─ files  → ExplorerTabHost # 新增：多标签 + 浮动宿主
       └─ browse → FileBrowser     # 只读浏览本 FR 不改

ExplorerTabHost
  ├─ TabBar（新建/关闭/弹出/新浏览器标签）
  ├─ active pane → ResourceExplorer（key=tabId）
  └─ FloatLayer[] → 各浮动 ResourceExplorer

路由 /instances/:id/files
  └─ InstanceFilesPage → 单例 ResourceExplorer（+ 可选 Config）
```

**MVP 范围裁定（推荐）**：  
- **必须**：`files` 视图 + 深链页 + 浮动。  
- **同 PR 可选**：`manage`（ConfigExplorer）挂同一 TabHost；若工期紧可二期，spec 验收以 `files` 为准。

### 3.2 标签状态（建议纯模块，便于单测）

`explorer/explorer-tabs.ts`（名称可调）：

```ts
type ExplorerTab = {
  id: string
  title: string
  currentDir: string
  openFilePath?: string
  floated: boolean
}

// openTab / closeTab / activateTab / setFloated / renameTitle
// MAX_TABS=8, MAX_FLOATS=3
```

- UI 状态可用 `useState` 于 `ExplorerTabHost`；**不必**上 Zustand，除非实现中发现跨路由共享必要（深链页独立，不共享主区标签）。

### 3.3 ResourceExplorer 适配

加性 props（破坏性最小）：

| prop | 用途 |
|---|---|
| `initialDir?: string` | 挂载时初始目录（深链/新标签） |
| `initialFile?: string` | 挂载后打开文件 |
| `onContextChange?: (ctx: { dir: string; file?: string; dirty: boolean }) => void` | 回传标题与草稿 |
| 既有 `openPathRef` | 保留 |

- 每个标签 `key={tabId}` 挂载独立实例，避免串状态。  
- 历史栈仍是组件内 FR-375 实现；**标签级不共享**历史。

### 3.4 浮动层

- 实现：`fixed` 容器 + 标题栏 pointer 拖拽；尺寸 state；不引入新 npm 依赖。  
- 遮罩：无全屏 modal 遮罩（可对照主区操作）；危险确认仍走全局 Dialog portal。  
- 点击浮动窗聚焦：提升 z-index（简单自增即可）。

### 3.5 路由

- 在 `Workspace.tsx` 增加：  
  `Route path="instances/:id/files" element={<InstanceFilesPage />}`  
- `InstanceFilesPage`：读 `id` + `useSearchParams` 的 `path`/`file`/`mode`，渲染壳。  
- 与 ADR-067：`/instances/:id/files` **不**并入 `instances-console` routeKey 保活集（独立页；关闭标签即卸）。控制台内多标签才保活在资源页签内。

### 3.6 与 FR-375 / 377 边界

| 能力 | FR |
|---|---|
| 地址栏/历史/侧键/排序/权限列 | 375（已落地，本 FR 复用） |
| 多标签/浮动/深链 | **376** |
| 跨窗拖放与剪贴板总线 | 377 |
| 全场景 Capability 壳 | 378 |

## 4. 任务拆分

- [x] `explorer-tabs.ts` + 单测（增删切换/上限/浮动标记）  
- [x] `ResourceExplorer`：`initialDir` / `initialFile` / `onContextChange`  
- [x] `ExplorerTabHost` + TabBar UI + i18n  
- [x] 浮动层拖拽/缩放/收回/关闭  
- [x] `InstanceFilesPage` + Workspace 路由  
- [x] 工具栏/菜单：「新标签」「弹出」「浏览器新标签」  
- [x] DOM：多标签切换、弹出收回、深链 path/file  
- [x] 文档：PRD 开发中、CHANGELOG、本 spec 勾选  
- [ ] 真机：双标签对照 + 深链并排（用户确认）

## 5. 验收标准

1. 同实例资源「文件」视图可开 ≥2 标签，各自目录独立，切换不丢未保存确认纪律。  
2. 弹出 → 浮动可拖/缩放 → 收回后目录与打开文件一致；dirty 不丢。  
3. 「浏览器新标签」打开 `/instances/:id/files?path=…&file=…` 可直达目录/文件（自动化或真机）。  
4. 标签/浮动达软上限有明确提示，不崩溃。  
5. 关闭脏标签出现既有 discard 确认。  
6. 单元 + 关键 DOM 测绿；i18n 中英对称。  
7. 真机（用户确认）：双标签对照 + 深链并排一页。

## 6. 风险 / 待定

| 项 | 说明 | 建议默认 |
|---|---|---|
| Config「管理」是否同批多标签 | 工期 | **MVP 仅 files**；manage 可同结构跟进 |
| 浮动是否 modal 锁背景 | 对照效率 | **不锁**背景 |
| 深链是否要鉴权 query | 安全 | **否**，走会话 cookie |
| 标签跨刷新恢复 | 复杂度 | **不做** |
| 与控制台 `?tab=` 命名冲突 | `tab` 已占用 | 资源内标签用内存 id，URL 仅深链页带 path |

### 审核闸（SDD）

**请审核本 spec。** 通过后才进入测试先行与实现；若设计细节要改请指出；若与 PRD FR-376 定义冲突请拍板后再动 PRD。
