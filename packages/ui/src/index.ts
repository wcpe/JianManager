export * from './components/badge'
export * from './components/button'
export * from './components/card'
export * from './components/checkbox'
export * from './components/combobox'
export * from './components/context-menu'
export * from './components/context-menu-surface'
export * from './components/dialog'
export * from './components/dropdown-menu'
export * from './components/field-label'
export * from './components/gauge'
export * from './components/input'
export * from './components/label'
export * from './components/mini-bar'
export * from './components/panel'
export * from './components/layout'
export * from './components/password-input'
export * from './components/scrollable-dialog'
export * from './components/select'
export * from './components/sheet'
export * from './components/shell'
export * from './components/SizeProvider'
export * from './components/spinner'
export * from './components/stat-card'
export * from './components/status-badge'
export * from './components/summary-chips'
export * from './components/table'
export * from './components/tabs'
export * from './components/textarea'
export * from './components/view-toggle'

// ---- 图表模块：barrel 只透出**类型**，组件一律走深路径（`@jianmanager/ui/charts/*`）----
//
// 【为什么值不能再从 barrel 出】图表模块（TimeSeriesChart / MonitorChart）静态 import recharts，
// 而 barrel 被 120+ 个应用文件引用、几乎每个路由分块都静态可达。于是 recharts 的「被引用面」
// 与 react 同量级，构建器把它与 react/react-dom 合并进同一个共享 chunk——实测该 chunk
// 就是 359 kB 的 `charts-*.js`，且**每个 chunk 都要 import 它拿 `require_react`**，
// 最终被写进 dist/index.html 的 modulepreload：任何页面（含落地页）首屏都得先下完它。
//
// 值改走深路径后，recharts 只被真正画图的少数视图同步引用；配合 vite.config.ts 撤掉
// 第三方命名分块，它才真正落成独立 chunk（LineChart-*.js）并退出首屏预加载——
// 两处缺一不可：只改分块配置，recharts 仍会被每个 barrel 消费者同步依赖。
//
// 类型不受影响：类型导出编译期擦除、不产生任何运行时依赖，故仍从 barrel 透出，
// 调用方的 `import type { MetricRange } from '@jianmanager/ui'` 无需改动。
export type { MetricRange, MetricResolution } from './charts/RangePicker'
export type { SparkPoint } from './charts/Sparkline'
export type { ChartSeries, ChartReferenceLine } from './charts/TimeSeriesChart'
export type { MonitorSource, MonitorSeriesHook } from './charts/MonitorSkeleton'

// 跨组件基础设施（FR-496 阶段 6）：焦点环/交互覆盖层常量与 a11y hooks
export * from './hooks'
export * from './lib/interaction-overlay'
export * from './lib/utils'
export * from './lib/threshold'
export * from './lib/brush'
export * from './lib/chart-hover'
export * from './lib/monitor-metrics'


// 显式列出而非 `export *`：`formatBytes` 与 `./lib/monitor-metrics` 同名（两者语义不同，
// 前者格式化存储占用、后者格式化监控指标），barrel 里不能同时通配导出。
// 显式列出而非 `export *`：`formatBytes` 与 `./lib/monitor-metrics`、`FieldError` 与
// `./components/field-label` 同名（语义不同），barrel 里不能同时通配导出。
// 同上：`FieldError` 与 `./components/field-label` 同名（此处是「校验错误条目」，
// 那边是「表单字段错误壳」）。需要本模块版的调用方走深路径。
// 显式列出而非 `export *`：`SummaryChip` 与 `./components/summary-chips` 同名但语义不同
// （前者是可点击筛选 chip、后者是带状态等级的汇总 chip）；`ConfigSwitch` 已由
// 显式列出而非 `export *`：`formatBytes` 与 `./lib/monitor-metrics` 同名（两者格式不同，
// 前者 1024 进制带 B/KB/MB 后缀、后者是监控指标的 G/M/K 缩写），barrel 里不能同时通配导出。
export * from './lib/theme'
// WorkbenchLeafParts 的 PageBreadcrumb 与 './components/layout' 的同名导出（类型）冲突，
// 故显式列出其余导出；PageBreadcrumb 组件走深路径
// 显式列出而非 `export *`：`BotStatusKind` 与 `./lib/bots-overview` 的后端状态枚举同名但语义不同
export * from './lib/combobox'
export * from './lib/color-contrast'
export * from './lib/focus-ring'
// 显式列出而非 `export *`：`FieldError` 与 `./components/field-label` 的展示组件同名但语义不同
// 显式列出而非 `export *`：`DragPayload` 与 `./lib/explorer-clipboard-bus` 的同名类型语义不同
// 不导出：该模块仅有的 `fmtBytes` 与 client-dist 的 OpsShared 同名（两者格式口径不同），
// 显式列出而非 `export *`：`HoverPrefetcher` 与 `./lib/instance-prefetch` 的同名类型语义不同；
// server-selection 的 store 与类型（FR-240 / FR-293）。
export * from './lib/stat-card'
export * from './lib/tone'
// 显式列出而非 `export *`：`VirtualWindow` / `VirtualWindowInput` 与 `./lib/logs-filters` 的同名类型语义不同

// ==== FR-502 迁移批：新增视图按域补录 ====
// 逐条列出以便逐个核对；同名冲突沿用本文件既有先例（显式列出，冲突名走深路径）。
// 显式列出而非 export *：AgentTokenOption 与同域其它视图同名（字段不同），
// barrel 里不能同时通配；需要本模块版该类型的调用方走深路径。
// audit 域
// client-dist 域
// config-baselines 域
// config-explorer 域
// console 域
// import-server 域
// instances 域
// logs 域
// networks 域
// notifications 域
// overview 域
// permissions 域
// players 域
// runtime-assets 域
// settings 域
// statistics 域
// system-update 域
// tasks 域
