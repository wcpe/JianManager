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

export * from './charts/RangePicker'
export * from './charts/Sparkline'
export * from './charts/TimeSeriesChart'
export * from './charts/MonitorChart'
export * from './charts/MonitorSkeleton'
export * from './charts/MetricsOverviewStrip'
// 全量对齐补录：它此前既漏在 barrel 外、又漏在边界测试清单外，于是主控台只能
// 自己留一份副本（见 apps/control-plane-web/src/components/charts/MetricComparePanel.tsx）。
export * from './charts/MetricComparePanel'

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
