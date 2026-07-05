# FR-273 通用组件包与控件博物馆规格

- **状态**: ✅ 已交付@v0.13.0
- **日期**: 2026-07-05
- **关联**: FR-273、FR-267~272、ADR-057、ADR-058

## 背景

A 亮色高密度云运维台与 C 方块拓扑 Logo/Favicon 语言已经作为全站页面级设计方向推进。当前 `web/src/components` 同时承载基础控件、通用图表、控制台壳层、业务弹窗、API 驱动面板和文件/配置浏览器等内容，导致控件风格难以统一，也缺少一个可视化入口集中检查设计 token、明暗主题与常用状态。

FR-273 在继续改 Shell、侧栏、顶栏和页面前，先把真正通用的控件抽成前端组件包，并建立独立 wiki 子项目作为控件博物馆。

## 目标

1. 建立 `web/packages/ui`，对外提供 `@jianmanager/ui` 通用组件包。
2. 主应用 `web/src` 从 `@jianmanager/ui` 引入共享控件，不再直接维护第一版通用控件实现。
3. 新增 `web/wiki` 子项目，集中展示第一版控件、设计 token、常用状态矩阵和明暗主题效果。
4. 迁移保持业务行为不变，只调整共享控件的包边界、导入路径与样式来源。
5. 继续保留 navigation benchmark，用于观察切换页面卡顿问题的后续收口。

## 非目标

- 不抽 `ConsoleSidebar`、`ConsoleHeader`、`Workspace`、`InstanceConsolePage` 等控制台业务壳层。
- 不抽业务弹窗、API 驱动面板、store/router/i18n 强耦合组件。
- 不引入 Storybook 或新的第三方文档工具。
- 不改根目录 monorepo 结构。
- 不新增或修改后端 API。

## 第一版范围

组件包只收纳无业务依赖、可在主应用与 wiki 之间复用的控件与辅助函数。

### Foundation

- 组件级样式 token、主题变量、工具条表面、紧凑页面骨架类。
- Logo/Favicon 已由既有全站视觉方向承接，本 FR 只消费 token，不重新定义品牌方向。

### Actions / Forms

- `Button`
- `Input`
- `Textarea`
- `Select`
- `Checkbox`
- `PasswordInput`
- `FieldLabel`

### Data / Layout

- `Badge`
- `Card`
- `Panel`
- `Table`
- `StatusBadge`
- `StatCard`
- `SummaryChips`
- `MiniBar`
- `ViewToggle`

### Navigation / Overlay

- `Tabs`
- `Dialog`
- `Sheet`
- `DropdownMenu`
- `ContextMenu`
- `ScrollableDialog`

### Monitoring / Charts

- `Gauge`
- `RangePicker`
- `Sparkline`
- `TimeSeriesChart`
- `MonitorChart`
- `MonitorSkeleton`
- `MetricsOverviewStrip`

### Shared helpers

- `utils`
- `threshold`
- `brush`
- `chart-hover`
- `monitor-metrics`

## 工程约束

- `@jianmanager/ui` 第一阶段作为 `web/packages/ui` 下的源码包，由 Vite/TypeScript alias 直接消费。
- `web/src/components/ui` 与通用 chart 的旧入口可保留一个迁移期 re-export，避免一次性改动扩大风险。
- `web/wiki` 必须消费 `@jianmanager/ui`，不得复制组件实现。
- 组件包样式由统一入口导出，主应用与 wiki 共享同一套 token，避免暗色模式出现第二套 CSS 真源。
- 包结构调整涉及 `web/package.json`、`web/vite.config.ts`、`web/tsconfig*.json` 等前端工具链配置，实施时只做完成 FR-273 所需的最小改动。

## Wiki 信息架构

`web/wiki` 第一版是控件博物馆，不做营销页。首屏直接展示控件目录、主题预览与高密度运维台常用组合。

页面分组：

- Foundation：色彩、排版、间距、边框、状态色、工具条表面。
- Actions：按钮尺寸、图标按钮、禁用、加载、危险操作。
- Forms：输入框、选择器、复选框、密码输入、错误状态。
- Data：状态徽章、卡片、面板、表格、统计卡与摘要条。
- Overlay：Dialog、Sheet、DropdownMenu、ContextMenu、ScrollableDialog。
- Monitoring：RangePicker、Sparkline、TimeSeriesChart、MonitorChart、Gauge、指标条。

每个分组至少展示常态、禁用、错误或空态之一；具备暗色风险的控件必须同时展示暗色主题效果。

## 验收标准

1. 主应用中第一版通用控件从 `@jianmanager/ui` 引入；旧入口只作为兼容 re-export 或被删除。
2. `web/wiki` 可独立启动与构建，页面能展示全部第一版控件分组。
3. wiki 不复制组件实现，组件来源可由 import 路径验证。
4. 主应用页面行为保持不变，已存在的 DOM 测试与 E2E smoke 不因迁移回退。
5. navigation benchmark 继续可运行，并覆盖关键页面切换基线。
6. 暗色模式中组件包控件不出现明显反色、透明底漏色或文字不可读问题。
7. 不新增后端 API，`docs/API.md` 无需变更。

## 验证计划

- 迁移前运行相关前端测试，记录基线。
- 迁移后运行组件 DOM 测试、wiki smoke、navigation benchmark、主应用 E2E smoke、lint 与 build。
- 对 `@jianmanager/ui` 导出面做 `rg` 检查，确认主应用通用控件导入已切换。
- 对暗色主题至少覆盖 wiki、平台首页、客户端分发安全页、群组网络页和节点页。

## 风险

- 机械全量搬迁 `web/src/components` 会把业务依赖带进通用包，必须按第一版范围逐项筛选。
- 样式入口重复导入可能导致暗色模式回退，实施时要保持 token 单一来源。
- 新增 wiki 子项目会增加前端脚本与构建面，需限制为最小 Vite 子项目，不引入新依赖。
- 旧入口 re-export 若长期保留会制造双入口，交付后需要在后续 FR 中清理。

## 交付记录

- 新增 `web/packages/ui` 源码包，主应用通过 `@jianmanager/ui` alias 消费第一版通用 UI、charts 与 helper。
- 旧 `web/src/components/ui`、第一版通用 chart 与 helper 入口保留为兼容 re-export。
- 新增 `web/wiki` Vite 子项目，控件博物馆直接消费 `@jianmanager/ui` 并展示 Foundation / Actions / Forms / Data / Overlay / Monitoring。
- Playwright benchmark 继续覆盖关键页面切换，并将本地 dev server 显式绑定到 `127.0.0.1`，避免当前 Windows 环境下 `localhost` 等待不稳定。
