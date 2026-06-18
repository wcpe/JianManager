# 实施计划 — FR-037 运维控制台布局

> 关联 FR: FR-037 | 优先级: P1 | 状态: 🔨 in-progress | 关联 ADR: ADR-009

## 背景

现有主布局 `DashboardPage` 是「单一导航侧栏 + 路由内容区」，实例终端埋在实例详情页 Tabs 里。运维多实例 MC 群组服时需在「实例列表 → 详情 → 终端 Tab」反复跳转。ADR-009 决定以「运维控制台」三段式 Shell 取代主布局：左栏上=功能导航、中=节点切换+实例树、下=系统平台导航，右=工作区；点实例在工作区开单个终端。

本批为纯前端，不动后端、不动 `web/src/api/bots.ts`。

## 组件拆解

```
web/src/pages/DashboardPage.tsx        # 改写为「运维控制台」Shell：左栏 + 右工作区
web/src/components/console/
  ConsoleSidebar.tsx                   # 左侧三段栏容器（上/中/下布局）
  FeatureNav.tsx                       # 上：功能导航（仪表盘/节点/实例/Bot/告警/模板/计划任务/备份）
  PlatformNav.tsx                      # 下：系统平台导航（用户/用户组/审计）+ 主题/语言/退出/版本
  NodeSwitcher.tsx                     # 中-上：节点下拉（全部节点 + 各节点），用 shadcn Select
  InstanceTree.tsx                     # 中-下：实例树（全部按节点分组 / 单节点平铺）+ 状态点
  InstanceStatusDot.tsx               # 实例状态点（绿/琥珀/红/空心）
  Workspace.tsx                        # 右：路由出口 + 终端面板切换
  TerminalPane.tsx                     # 右：单实例终端（复用 token + xterm + 面包屑 + 占位按钮）
  WorkspaceEmpty.tsx                   # 右：未开终端时的空状态
  instanceTree.ts                      # 纯函数：按节点分组、节点筛选（可单测）
  instanceTree.test.ts                 # vitest 单测（分组 / 筛选逻辑）
web/src/stores/console.ts              # Zustand：当前选中实例 id + 选中节点 id（UI 状态）
```

设计取舍（对齐 ADR-009 + 现有代码）：
- **选中实例状态用 Zustand**（`console.ts`），与项目「客户端 UI 状态用 Zustand」约定一致；不放 URL，避免与既有 `/instances/:id` 详情路由语义冲突。
- **终端打开 = 在工作区渲染 `<TerminalPane instanceId=…>`**，覆盖在路由出口之上；选另一个实例换 `instanceId`，`key` 绑定 `instanceId` 保证 xterm 实例重建，复刻详情页行为。
- **节点筛选**：`全部节点` → `useInstances()` 不带 `nodeId`，前端按 `nodeId` 分组；选某节点 → `useInstances({ nodeId })`，只列该节点（后端过滤）。
- **导航分区**：功能导航 = 与实例运维强相关（仪表盘/节点/实例/Bot/告警/模板/计划任务/备份）；系统平台导航 = 平台管理（用户/用户组/审计）。`设置` 当前无对应页面/路由，暂不加入以免死链（详情见「开放问题」）。**保留全部既有 11 个导航目标**，仅重组分区。

## 任务拆解

### Phase 1: 纯逻辑 + 状态

- [ ] `console.ts`（Zustand：`selectedInstanceId` / `selectedNodeId` + setter）
- [ ] `instanceTree.ts`（`groupInstancesByNode`、节点名映射 helper）
- [ ] `instanceTree.test.ts`（vitest，覆盖分组/空列表/未知节点）

### Phase 2: 侧栏组件

- [ ] `InstanceStatusDot.tsx`（状态 → 颜色/空心）
- [ ] `NodeSwitcher.tsx`（shadcn Select，全部节点 + 各节点）
- [ ] `InstanceTree.tsx`（分组/平铺渲染 + 点选回调 + loading/empty）
- [ ] `FeatureNav.tsx` / `PlatformNav.tsx`（NavLink + 主题/语言/退出/版本）
- [ ] `ConsoleSidebar.tsx`（三段栏容器）

### Phase 3: 工作区

- [ ] `TerminalPane.tsx`（面包屑 + 禁用占位按钮「分屏」「切导播台」+ 复用 Terminal）
- [ ] `WorkspaceEmpty.tsx`（空状态）
- [ ] `Workspace.tsx`（路由 Routes + 终端覆盖层）

### Phase 4: 组装 + i18n

- [ ] 改写 `DashboardPage.tsx` 为 Shell（左栏 + 工作区）
- [ ] zh/en i18n 新增 `console.*` 键（面包屑、占位按钮、空状态、全部节点、版本等）
- [ ] 沿用既有 `nav.*`、`theme.*`、`common.logout`

### Phase 5: 验证

- [ ] `cd web && npx tsc --noEmit` 通过
- [ ] `cd web && npm run lint` 通过
- [ ] `cd web && npm run build` 通过
- [ ] `cd web && npx vitest run` 通过（新增分组/筛选单测）
- [ ] 暗色/亮色 + zh/en 无样式错乱（人工核对）
- [ ] 既有路由（节点/用户/审计/实例详情…）仍可达

## 产出文件范围

| 文件 | 操作 |
|---|---|
| `web/src/pages/DashboardPage.tsx` | 改写为控制台 Shell |
| `web/src/components/console/*.tsx` | 新增（侧栏/工作区/终端面板等） |
| `web/src/components/console/instanceTree.ts` + `.test.ts` | 新增（纯函数 + 单测） |
| `web/src/stores/console.ts` | 新增（选中实例/节点 UI 状态） |
| `web/src/i18n/{zh,en}.json` | 新增 `console.*` 键 |
| `web/package.json` + 配置 | 新增 vitest devDependency + test 脚本（仅当本仓库尚无测试基建时） |
| `docs/ARCHITECTURE.md` | 前端架构章节更新为控制台布局 |
| `docs/specs/ops-console/{api,impl}.md` | 本规格文档 |

## 不做（范围外，见 scope-discipline）

- 工作区 Bot 段、实例树 Bot 聚合徽标（FR-039）
- 全局 Bot 页重构（FR-040）
- Bot 规模化 API（FR-038）
- 分屏 / 导播台 / 拖拽（仅占位按钮，禁用）
- 不修改 `web/src/api/bots.ts` 及任何后端 Go 代码

## 开放问题

- **`设置` 导航**：ADR-009 把「设置」列入系统平台导航，但当前前端无 SettingsPage 与 `/settings` 路由。本批不新建页面（属另一 FR），故 `PlatformNav` 暂不渲染「设置」，避免死链。待设置页落地后补入。
