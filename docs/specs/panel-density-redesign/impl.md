# 实施计划 — FR-061 面板信息密度与视觉改造

> 关联 FR: FR-061 | 优先级: P1 | 状态: 🔨 in-progress（5 阶段实现完成 + 真机浏览器验收通过；待用户验收标 done）| 关联 ADR: ADR-009

## 背景

当前前端信息密度低、纯灰阶、稀疏卡片布局（`OverviewPage` 2 卡 + 2 饼图、各列表页留白偏大）。参考 baota 重做为高密度运维面板：多级侧栏、环形仪表盘、分区面板、密集表格、状态色系、MC 绿主色。纯前端，不动后端。设计系统契约见 `api.md`。

## 设计取舍

- **不换框架**：扩展 OKLCH token + 新增组件变体，沿用 shadcn/ui + Tailwind（守架构不变量、控制改动面）。
- **侧栏在 ADR-009 内演进**：仍是运维控制台 Shell，把三段式（`FeatureNav`/`InstanceTree`/`PlatformNav`）整合为常驻多级侧栏；实例树降为「实例」组可展开子区、节点切换器保留——能力不丢、只重组 IA。沿用 FR-037 的 `min-h-0` 高度分配经验，保证短屏不重叠。
- **设计语言先行**：先落 token + 通用组件（仪表盘/图表/密集表格/面板），各页面再套用；避免新页面（含 FR-060 仪表盘、运维扩展批次新页）以旧样式落地后返工。
- **阈值驱动变色**：资源/TPS 按阈值自动着色，异常自浮现。

## 组件 / 文件拆解

```
web/src/index.css                         # 扩展 token：密度档位 + 状态色系 + MC 绿主色
web/src/components/ui/
  gauge.tsx          # ResourceGauge（环形仪表盘）
  panel.tsx          # Panel（分区面板 + 标题栏）
  mini-bar.tsx       # MiniBar（迷你资源条）
  status-badge.tsx   # StatusBadge（状态色徽章）
  data-table.tsx     # DataTable dense 变体（或扩展现有 table.tsx）
web/src/components/charts/
  TimeSeriesChart.tsx# recharts 封装（多序列 + null 断点 + tooltip）
  RangePicker.tsx    # 时间区间选择器（FR-060 复用）
web/src/components/console/
  ConsoleSidebar.tsx # 三段式 → 常驻多级侧栏
  FeatureNav.tsx / PlatformNav.tsx / InstanceTree.tsx / NodeSwitcher.tsx  # 并入多级侧栏
web/src/stores/console.ts                 # 侧栏分组展开状态（UI 状态）
web/src/pages/*.tsx                        # 各页套用高密度档位与新组件
web/src/i18n/{zh,en}.json                  # 新增 nav/IA/组件文案键
```

## 任务拆解

### Phase 1: 设计 token 底座
- [x] `index.css` 扩展密度档位变量 + 状态色系（亮/暗双值）+ MC 绿主色
- [x] 阈值 → 颜色的工具函数（资源/TPS）

### Phase 2: 通用高密度组件
- [x] `ResourceGauge` / `Panel` / `MiniBar` / `StatusBadge`
- [x] `DataTable` dense 变体（行内操作链接 + 徽章 + 迷你条）
- [x] `TimeSeriesChart` / `RangePicker`（FR-060 依赖此二者）

### Phase 3: 侧栏 IA 改造
- [x] `ConsoleSidebar` 改为常驻多级侧栏，整合 FeatureNav/PlatformNav
- [x] 实例树作为「实例」组可展开子区 + 节点切换器保留（能力不丢）
- [x] 短屏高度分配实测（沿用 `min-h-0` 经验）

### Phase 4: 各页面套用
- [x] `OverviewPage`（仪表盘排 + 聚合曲线 + 密集实例表，与 FR-060 对接）
- [x] `NodesPage` / 节点详情（环形仪表盘 + 曲线）
- [x] `InstancesPage` / 实例详情、其余列表页套高密度档位
- [x] i18n（zh/en）补键

### Phase 5: 验证
- [x] `cd web && npx tsc --noEmit` 通过
- [x] `cd web && npm run lint` 通过
- [x] `cd web && npm run build` 通过
- [x] `cd web && npx vitest run` 通过（组件/工具函数单测）
- [x] 暗色/亮色 + zh/en 无样式错乱、对比度可读（人工核对）
- [x] 既有路由与能力（实例树/节点切换/各页）均不丢失

## 产出文件范围
| 文件 | 操作 |
|---|---|
| `web/src/index.css` | 扩展 token |
| `web/src/components/ui/{gauge,panel,mini-bar,status-badge,data-table}.tsx` | 新增/扩展 |
| `web/src/components/charts/{TimeSeriesChart,RangePicker}.tsx` | 新增 |
| `web/src/components/console/*.tsx` | 改造为多级侧栏 |
| `web/src/stores/console.ts` | 侧栏展开状态 |
| `web/src/pages/*.tsx` | 套用高密度档位 |
| `web/src/i18n/{zh,en}.json` | 新增文案键 |
| `docs/ARCHITECTURE.md` | 前端架构章节更新为高密度面板 + 多级侧栏 |

## 不做（范围外，见 scope-discipline）
- 不改任何后端 Go 代码与 API。
- 不在本 FR 实现 FR-060 的数据采集/存储（仅消费 `/metrics`）。
- 运维扩展批次（FR-048~059）各业务页的功能逻辑——本 FR 只提供设计系统并改造既有页，新业务页由各自 FR 用新组件实现。
- 分屏 / 导播台 / 拖拽（仍为禁用占位）。

## 开放问题
- 实例树在多级侧栏的最终位置（组内可展开 vs 常驻面板）按短屏实测定。
- `DataTable` 是新建组件还是扩展现有 `ui/table.tsx`，实现时按改动面权衡。
