# 功能规格：清理目录树形右键菜单可视化

> 状态：待审　·　关联 PRD：FR-262（增强 FR-255）　·　依赖 FR-261（FileExplorer 组件）

## 1. 背景与目标

FR-255 做了 ManagedDirsEditor（目录树勾选 + clean-all 开关 + 自定义排除），但用户体验仍不够直观——勾选状态不够醒目，批量操作不便。用户要求：树形展示目录，右键菜单标记清理/排除，Ctrl+点选/Shift+连选批量操作，颜色区分（红=清理、绿=排除、无色=不管理），父子联动。

**目标**：用 FR-261 的 FileExplorer 同款文件树，替换 ManagedDirsEditor，改为右键菜单 + 多选 + 颜色标记的交互模式。

## 2. 需求

### 范围内
- **复用 FileExplorer 的文件树**（目录结构展示、展开/折叠）
- **三态标记**：每个目录/文件可标记为「清理」「排除」「不管理」
- **右键菜单**：右键目录 → 弹出「标记为清理」「标记为排除」「取消标记」
- **多选批量操作**：Ctrl+点击追加选、Shift+点击连选，然后右键批量标记
- **颜色区分**：红色=清理、绿色=排除、无色=不管理（背景色或左侧色条）
- **父子联动**：父目录标记清理→子目录继承、子目录单独改标记→父目录半选/混合色
- **产出**：标记为「清理」的目录 → managedDirs（含 `"*"` 时全目录清理）；标记为「排除」的目录 → cleanExclude
- **clean-all 开关保留**：开启后全目录标记为清理（红色）
- **DangerConfirm 二次确认保留**：发布 clean-all 时强制确认
- i18n zh/en

### 不做
- 文件级标记（只标记目录，文件跟随所在目录）
- 拖拽上传/新建文件夹（那是 FR-261 发布页的事，清理页只读展示目录结构）
- 改 manifest 协议（managedDirs/cleanExclude 字段不变）

## 3. 设计

### 3.1 组件结构
- 新建 `CleanScopeEditor.tsx`，替换 `ManagedDirsEditor.tsx`
- 内部复用 FileExplorer 的文件树渲染（目录节点 + 展开折叠），但不启用编辑功能（readonly 模式 + 自定义右键菜单）
- 目录节点左侧加色条标记三态

### 3.2 状态管理
- `cleanMap: Map<string, 'clean' | 'exclude'>` —— 目录路径 → 标记状态
- 从 manifest 的 managedDirs + cleanExclude 反向初始化 cleanMap
- 标记变化时同步输出 managedDirs + cleanExclude

### 3.3 交互流程
```
右键目录 → 弹出菜单：
  - 标记为清理（红）
  - 标记为排除（绿）
  - 取消标记（恢复无色）
  ————
  - 全选 / Ctrl+A（可选）

Ctrl+点击 → 追加选中
Shift+点击 → 连选范围内所有目录
右键选中集 → 批量标记

父子联动：
  标记父目录为清理 → 所有子目录继承清理标记
  子目录单独标记为排除 → 父目录变为混合色（橙）
  取消父目录标记 → 子目录也取消（可改为独立保留，用户选择）
```

### 3.4 颜色方案
- 清理：红色背景 `bg-red-500/10` + 左侧 2px 红色条
- 排除：绿色背景 `bg-green-500/10` + 左侧 2px 绿色条
- 混合（子目录有不同标记）：橙色背景 `bg-orange-500/10` + 左侧 2px 橙色条
- 不管理：无特殊颜色

## 4. 任务拆分
- [ ] 新建 CleanScopeEditor.tsx（复用文件树 + 三态标记 + 右键菜单）
- [ ] 颜色区分渲染（红/绿/橙/无色）
- [ ] Ctrl+点击/Shift+点击多选
- [ ] 右键菜单批量标记
- [ ] 父子联动逻辑
- [ ] 替换 ManagedDirsEditor
- [ ] clean-all 开关 + DangerConfirm 保留
- [ ] vitest/dom 测试 + i18n zh/en
- [ ] tsc/lint/build 绿

## 5. 验收标准
- 树形展示目录结构，三态颜色区分（红=清理/绿=排除/橙=混合/无色=不管理）
- 右键菜单标记清理/排除/取消
- Ctrl+点击多选、Shift+点击连选、批量右键标记
- 父子联动：父标记→子继承、子单独改→父混合色
- 产出 managedDirs + cleanExclude 正确
- clean-all 开关 + DangerConfirm 保留
- 前端 tsc/lint/vitest/build 绿
- **【需真机】** 配清理+排除 → 发布 → manifest 字段正确
