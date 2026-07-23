# 功能规格：统一文件管理器壳 + Capability 全场景接入

> 状态：开发中（自动化测绿）　·　关联 PRD：FR-378　·　分支：feature/fr-378-unified-shell  
> 依赖：FR-375 / FR-376（单窗/多窗体验契约）；可与 FR-377 并行  
> 计划：`.tmp/brainstorm-file-explorer-perm-ux-2026-07-23.md`  
> **不新增 ADR**（在 FR-213 `FileBrowserSource` 之上加 Capability 与壳路由；不改三进程边界）

## 1. 背景与目标

文件浏览 UI 分叉：`ResourceExplorer` / `ConfigExplorer` / `FileBrowser` / `FileExplorer`（分发编排）/ `ClientFileTree` / `StoragePage` 自绘表 / `DirectoryPicker`。目标：

1. 用 **Capability** 描述各场景能写/能 chmod/能上传/业务扩展槽，**不硬套、不删**分发标记等能力。  
2. **统一壳**按 Capability 选择：全功能多标签宿主 / 只读 FileBrowser / 业务自定义 children。  
3. **至少三条路径**接入：实例资源、平台存储浏览、客户端分发预览（后两者已有 FileBrowser 则接线+Capability 标注）。

## 2. 需求

### 范围内
- `ExplorerCapability` 类型 + 预设（instanceFull / instanceBrowse / storageBrowse / clientDistBrowse）  
- `UnifiedExplorerShell`：按 cap 渲染 `ExplorerTabHost` 或 `FileBrowser`（+ 可选 header/extra）  
- `storageFileSource`：平台存储 list 适配 FileBrowser（无读内容 API → 文件预览显式不可预览）  
- `StoragePage` 浏览区改 FileBrowser；实例「浏览」走 Shell；分发侧确认仍用 FileBrowser + 文档登记  
- **不重写** FileExplorer 编排树、不删 CleanScope/标记  

### 不做
- 把分发编排 FileExplorer 整棵迁到 ResourceExplorer  
- 新后端端点（存储读内容等可后续）  
- 桌面 OS 窗口管理  

## 3. 设计

```
UnifiedExplorerShell({ capability, source?, instanceId?, ... })
  if capability.mode === 'instance-manage' → ExplorerTabHost / ConfigExplorer（调用方组合）
  if capability.mode === 'browser'        → FileBrowser(source, actions from cap)
  if capability.mode === 'custom'         → children
```

Capability 字段（最小集）：
- `id` / `mode`: `browser` | `instance-files` | `custom`
- `canWrite` / `canUpload` / `canChmod` / `canDownload`
- `labelKey?`（i18n）
- `extraActions?`（FileBrowserAction[] 或业务 ReactNode）

## 4. 任务

- [x] Capability 类型 + 预设 + 单测（5）  
- [x] UnifiedExplorerShell + DOM（3）  
- [x] storageFileSource + StoragePage 接入（单测 3 + 页 DOM 5）  
- [x] InstanceResourceCard 浏览/文件走壳（DOM 回归）  
- [x] 分发预览：既有 FileBrowser + clientDistSource（FR-214，不重写编排树）  
- [x] 文档 PRD/CHANGELOG  
- [ ] 真机：实例 / 平台存储 / 分发各一条（用户确认）

## 5. 验收

1. 实例文件/浏览/管理能力不减。  
2. 平台存储浏览经 FileBrowser 可列目录。  
3. 分发预览仍可用 FileBrowser（回归既有测）。  
4. Capability 预设单测绿；壳 DOM 测绿。  
5. 无「为复用砍业务」回归。
