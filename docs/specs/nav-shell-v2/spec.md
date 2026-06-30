# 导航外壳 v2：命令面板 + 常驻实例列（FR-240，含 FR-241/FR-245）

> 状态：开发中 · 增强 FR-131（五域 IA）/ FR-162（页眉）· 面向 1000+ 实例
> 已选方向（2026-06-30）：**方案 B 为主骨架**（图标域轨 + 常驻实例列 master-detail）+ **内置方案 C 的 Ctrl+K 命令面板**（= FR-241）。

## 1. 需求（用户走查 → 目标）

- 「导航里的实例选择需要随导航栏一起重构」「搜索功能需要支持」「面向 1000+ 实例」。
- 现状：页眉搜索框是占位（`ConsoleHeader.SearchBox` 仅 Ctrl+K 聚焦，无检索）；实例选择是 cluster 域展开后内嵌的 `InstanceTree`（藏在二级、非常驻）。

## 2. 设计

### 2.1 Part A — Ctrl+K 命令面板（FR-241，本期先交付）
- 全局命令面板（裸 div 模态，复用 `MODAL_OVERLAY`），Ctrl/⌘+K 或点页眉搜索框打开，Esc 关。
- 单输入框 + 分组结果，**四类可检索目标**：
  1. **实例**（名称/UUID 子串）→ 选中即 `openInstance(id)` 进工作区。
  2. **节点**（名称/host 子串）→ 跳 `/nodes` 并选中该节点。
  3. **页面**（五域导航项 label 子串）→ `navigate(to)`。
  4. **操作**（刷新当前页 / 折叠侧栏 / 切主题 / 退出）→ 执行。
- 键盘：↑↓ 移动、Enter 执行、Esc 关；鼠标悬停即选中行。空查询显默认（最近实例 + 常用页面）。
- 数据走**现有** `useInstances()` / `useNodes()` + 静态导航表；1000+ 实例的服务端检索由 FR-247 后续接入（本期客户端子串足够，结果截断 + 提示「精确搜索」）。
- 页眉 `SearchBox` 由占位 Input 改为**按钮**（点击/聚焦即开面板）；Ctrl+K 处理移入面板全局监听（单一处理器）。

### 2.2 Part B — 图标域轨 + 常驻实例列（FR-240 主骨架，本期出设计、待确认再改壳）
- 侧栏重构为 master-detail：**窄图标域轨**（五域图标，hover tooltip）+ **常驻实例列**（搜索 + 按组/节点分组 + 虚拟滚动 + 最近/收藏），实例列不再藏在 cluster 域二级。
- 实例数据走 FR-247 服务端搜索/分页（1000+）。
- ⚠ 此改动重塑 FR-131 既定五域 IA 的呈现形态 → **需先出具体设计/Mockup 经用户确认**，若改 IA 语义补 ADR。

### 2.3 Part C — 面包屑文案纠错（FR-245，排最后）
- `PageBreadcrumb` 路由→labelKey 映射与侧栏导航文案对齐（用户报「面包屑文字与导航内位置不符」）。

## 3. 验收

- [ ] Ctrl+K / 点搜索框打开命令面板；输入可检索实例/节点/页面/操作并跳转/执行。
- [ ] 键盘 ↑↓/Enter/Esc 可用；空查询有合理默认；结果过多截断且有提示。
- [ ] 页眉搜索框改为开面板入口，不再是死占位。
- [ ] 前端 `tsc`/`eslint`/`vitest` 绿；纯检索/匹配逻辑有单测。
- [ ] **真机走查**：实例多时搜得到、跳得对（用户确认）。
- [ ] Part B/C 单列后续，各自验收。

## 4. 关联

`components/console/ConsoleHeader.tsx`（SearchBox）、新 `components/console/CommandPalette.tsx`、`stores/console.ts`（面板开关 + 检索状态）、`components/console/ConsoleSidebar.tsx`（NAV 表复用）、`InstanceTree`/`NodeSwitcher`（Part B）、`PageBreadcrumb.tsx`（Part C）。

## 5. 备注

本期先交付 **Part A（命令面板）**——可独立验证、不动既有 IA、即时缓解「搜索是死占位」。Part B（常驻实例列）重塑侧栏，先出设计经用户确认（必要时 ADR）再实现；Part C 面包屑纠错排最后。
