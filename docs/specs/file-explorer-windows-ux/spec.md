# 功能规格：资源管理器单窗 Windows 对齐体验

> 状态：开发中（自动化测绿）　·　关联 PRD：FR-375　·　分支：feature/fr-375-file-explorer-windows-ux  
> 依赖：FR-373（权限列 / 写预检 / 尝试修复）  
> 增强：FR-070 / FR-071 / FR-141 / FR-008 / FR-031  
> 计划：`.tmp/brainstorm-file-explorer-perm-ux-2026-07-23.md`  
> **本 FR 含原 brainstorm「写预检/编辑器权限态」（未单开 379）**

## 1. 背景与目标

### 问题
- 文件管理进入后滚轮带动**整页**滚动，列表/树未隔离。
- 无资源管理器式**地址栏、前进/后退历史、鼠标侧键**；浏览器后退会离开页面。
- 排序弱；无权限列/锁标；编辑器不展示可写态，写失败才暴露。

### 目标（单窗，对齐 Windows 资源管理器「详细信息」主路径）
1. 可编辑地址栏 + 前进/后退历史栈 + 侧键 + **有栈时拦截浏览器后退**  
2. 详情列头排序（名/时间/类型/大小/**权限**）+ 详情/列表/大图标视图切换与偏好记忆  
3. 列表/树**滚动隔离**  
4. 权限列 + 不可写锁标 + 编辑器可写态 + 保存/写路径预检与「尝试修复」（调 FR-373）

属阶段：P0。多窗/跨标签（FR-376/377）与统一壳全场景（FR-378）不在本 FR。

## 2. 需求（要什么）

### 范围内
1. **导航**  
   - 地址栏：相对实例工作目录路径，可键入/粘贴，Enter 跳转（非法路径 toast，不崩）。  
   - 历史栈：进入目录/打开文件推栈；工具栏后退/前进；`mouseup` button 3/4（侧键）在资源管理器聚焦时后退/前进。  
   - `popstate`：栈非空时 `preventDefault`/立即 `history.pushState` 吃掉浏览器后退；栈空放行路由。

2. **列表与视图**  
   - 详情视图列：名称、修改时间、类型、大小、权限（modeString 或锁标）；点列头升/降序；目录优先。  
   - 视图：详情 / 列表 / 大图标（三档）。  
   - 偏好：`localStorage` 记排序键/方向/视图（按实例或全局键，spec 实现时定 `jm.files.sort` / `jm.files.view`）。

3. **滚动**  
   - 资源管理器根容器 `h-full min-h-0 overflow-hidden`；树与列表各自 `overflow-auto`；滚轮不冒泡到页面主滚动。

4. **权限 UX（依赖 FR-373）**  
   - 列表展示 `readable`/`writable`/`modeString`；不可写文件锁标。  
   - 打开编辑：顶栏显示可写/只读；只读时禁用保存或保存前再 `check-access`。  
   - 写失败/预检失败：提供「尝试修复」→ 实例 `chmod` API + 确认。

5. **接入**  
   - 主改 `ResourceExplorer` / `FileList` / `Toolbar` / 路径条；`ConfigExplorer` 经 ResourceExplorer 间接受益。  
   - i18n `files.*` 扩充。

### 不做
- 应用内浮动窗、浏览器新标签、跨窗 DnD（376/377）。  
- 平台存储/分发树统一壳（378）。  
- chown/递归 chmod/Windows ACL。

## 3. 设计（怎么做）

### 3.1 历史栈
纯函数模块 `explorer/nav-history.ts`：`push` / `back` / `forward` / `canBack` / `canForward`；条目 `{ kind: 'dir'|'file', path: string }`。Vitest 覆盖。

### 3.2 浏览器后退拦截
`useExplorerBrowserBack(stack, onBack)`：挂载时 `pushState` 哨兵；`popstate` 时若 canBack 则 back 并再 pushState；卸载清理。

### 3.3 布局
```
[ Toolbar: 后退 前进 | 地址栏 | 视图切换 | … ]
[ 左树 | 右：FileList 或 Editor ]
```
均在固定高度 flex 列内，禁止外层页面跟着滚。

### 3.4 排序
`sortFiles(files, { key, dir, dirsFirst })` 纯函数；列头按钮切换。

## 4. 任务拆分

- [x] `nav-history.ts` + 单测  
- [x] `file-sort.ts` + 单测 + localStorage 偏好  
- [x] Toolbar：后退/前进 + 地址栏 + 视图切换  
- [x] 侧键 + 浏览器后退拦截（ResourceExplorer）  
- [x] FileList 列头排序 + 权限列/锁标 + 详情/列表/图标  
- [x] 滚动隔离布局（h-full min-h-0 overscroll-contain）  
- [x] 编辑器可写态 + check-access/chmod 入口  
- [x] i18n zh/en 键  
- [x] DOM 测（`ResourceExplorer.dom.test.tsx` 9 条含 FR-375 地址栏/后退/三视图/权限列；`nav-history`/`file-sort` 单测 6 条）  
- [x] CHANGELOG 一行（Unreleased）  
- [ ] 真机：滚动不整页、侧键、权限列与写预检

## 5. 验收标准

1. 侧键与工具栏前进后退一致；栈空才把后退还给路由。  
2. 排序/视图切换刷新后偏好仍在（localStorage）。  
3. 文件页滚轮不带动整页（真机）。  
4. 权限列与 FR-373 列表字段一致；不可写有锁标。  
5. 写前预检失败可尝试修复或明确阻断保存。  
6. 自动化：nav-history 单测 + 关键 DOM 测绿。

## 6. 风险 / 待定

- 浏览器后退拦截与 SPA 路由冲突：必须仅在 explorer 聚焦/挂载时生效。  
- 大图标视图性能：目录超大时虚拟化可后续，本 FR 先简单 grid。  
- ConfigExplorer 编辑器槽：权限态经 props 下沉，避免双实现。
