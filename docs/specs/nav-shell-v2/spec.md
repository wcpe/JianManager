# 导航外壳 v2：命令面板 + 服务器选择器（FR-240，含 FR-241/FR-245）

> 状态：✅ 已交付@v0.13.0（待主控真机验收） · 增强 FR-131（五域 IA）/ FR-162（页眉）· 面向 1000+ 实例
> 已选方向（2026-06-30）：**方案 B 为主骨架**（图标域轨 + 常驻实例列 master-detail）+ **内置方案 C 的 Ctrl+K 命令面板**（= FR-241）。

## 1. 需求（用户走查 → 目标）

- 「导航里的实例选择需要随导航栏一起重构」「搜索功能需要支持」「面向 1000+ 实例」。
- 原现状：页眉搜索框是占位（`ConsoleHeader.SearchBox` 仅 Ctrl+K 聚焦，无检索）；实例选择是 cluster 域展开后内嵌的 `InstanceTree`（藏在二级、非常驻）。
- 2026-07-05 落地：保留 ADR-055/056/057 的高密度五域 IA，不改语义、不新增 ADR；在展开侧栏顶部接入服务器选择器弹层，并让命令面板实例结果消费 FR-247 服务端搜索。

## 2. 设计

### 2.1 Part A — Ctrl+K 命令面板（FR-241，本期先交付）
- 全局命令面板（裸 div 模态，复用 `MODAL_OVERLAY`），Ctrl/⌘+K 或点页眉搜索框打开，Esc 关。
- 单输入框 + 分组结果，**四类可检索目标**：
  1. **实例**（名称子串，走 `GET /instances/search`）→ 选中即跳 `/instances/:id`。
  2. **节点**（名称/host 子串）→ 跳 `/nodes` 并选中该节点。
  3. **页面**（五域导航项 label 子串）→ `navigate(to)`。
  4. **操作**（刷新当前页 / 折叠侧栏 / 切主题 / 退出）→ 执行。
- 键盘：↑↓ 移动、Enter 执行、Esc 关；鼠标悬停即选中行。空查询显默认（最近实例 + 常用页面）。
- 实例数据走 FR-247 `useSearchInstances()` 服务端分页搜索；节点走 `useNodes()`，页面/操作走静态导航表。页眉节点作用域只透传到实例搜索，不限制节点结果。
- 页眉 `SearchBox` 由占位 Input 改为**按钮**（点击/聚焦即开面板）；Ctrl+K 处理移入面板全局监听（单一处理器）。

### 2.2 Part B — 服务器选择器弹层（FR-240 主骨架，已落地）
- 保持现有五域 IA 与移动底部导航不变；展开侧栏顶部新增「选择服务器」入口，打开服务器选择器弹层。
- 选择器能力：搜索、FR-247 服务端分页（`/instances/search`）、聚合计数（`/instances/aggregate`）、虚拟列表、按节点/状态分组、最近访问、收藏、加载态与空态。
- 1000+ 场景：mock 维持 1200 台服务器种子，选择器仅渲染可视窗口；页眉崩溃计数改消费聚合，命令面板实例搜索不再以 `useInstances()` 全量列表为主路径。
- 未改 ADR-055/056/057 的 IA 决策，未新增 ADR。

### 2.3 Part C — 面包屑文案纠错（FR-245）✅ 已交付
- `lib/breadcrumb` 路由→域/页面 labelKey 映射与侧栏导航对齐：补齐原漏的 `/super`、`/director`、`/client-dist-monitor`（原回退「控制台」、与导航不符）。

## 3. 验收

- [x] Ctrl+K / 点搜索框打开命令面板；输入可检索实例/节点/页面/操作并跳转/执行。
- [x] 命令面板实例结果走 `/instances/search`，命中后跳 `/instances/:id`；节点作用域只约束实例结果。
- [x] 页眉搜索框改为开面板入口，不再是死占位。
- [x] 服务器选择器支持搜索 / 虚拟列表 / 按节点与状态分组 / 最近 / 收藏 / 空态 / 加载态。
- [x] mock handler 补 `/instances/search` 与 `/instances/aggregate`，覆盖 q、nodeId、分页、聚合。
- [x] 前端 `eslint`/相关 `vitest` 绿；API hook、命令面板与服务器选择器有 DOM 覆盖。
- [ ] **真机走查**：实例多时搜得到、跳得对（主控确认）。

## 4. 关联

`components/console/ConsoleHeader.tsx`（SearchBox + 聚合计数）、`components/console/CommandPalette.tsx`、`components/console/ServerSelector.tsx`、`components/console/ConsoleSidebar.tsx`（选择器入口 + NAV 表复用）、`api/instances.ts`（FR-247 hook）、`mocks/handlers/domains/instance.ts`（search/aggregate mock）、`PageBreadcrumb.tsx`（Part C）。

## 5. 备注

本次以“不推翻既有高密度五域 IA”为约束交付 FR-240：命令面板与导航服务器选择器均消费 FR-247 地基；服务器选择器以弹层入口替代重写 master-detail 外壳，降低 IA 破坏风险。最终真机多实例走查仍待主控验收。
