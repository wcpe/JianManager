export * from './components/accordion'
export * from './components/alert'
export * from './components/avatar'
export * from './components/badge'
export * from './components/button'
export * from './components/card'
export * from './components/checkbox'
export * from './components/collapsible'
export * from './components/combobox'
export * from './components/context-menu'
export * from './components/context-menu-surface'
export * from './components/dialog'
export * from './components/dropdown-menu'
export * from './components/field-label'
export * from './components/gauge'
export * from './components/hover-card'
export * from './components/input'
export * from './components/kbd'
export * from './components/label'
export * from './components/mini-bar'
export * from './components/native-select'
export * from './components/pagination'
export * from './components/panel'
export * from './components/layout'
export * from './components/password-input'
export * from './components/popover'
export * from './components/progress'
export * from './components/radio-group'
export * from './components/scrollable-dialog'
export * from './components/select'
export * from './components/separator'
export * from './components/sheet'
export * from './components/nav'
export * from './components/SizeProvider'
export * from './components/slider'
export * from './components/spinner'
export * from './components/stat-card'
export * from './components/status-badge'
export * from './components/summary-chips'
export * from './components/switch'
export * from './components/table'
export * from './components/tabs'
export * from './components/textarea'
export * from './components/toggle'
export * from './components/toggle-group'
export * from './components/tooltip'
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

// 跨组件基础设施：焦点环 / 交互覆盖层常量与 a11y hooks
export * from './hooks'
export * from './lib/interaction-overlay'
export * from './lib/utils'
export * from './lib/threshold'
export * from './lib/brush'
export * from './lib/chart-hover'
export * from './lib/monitor-metrics'
export * from './lib/theme'
export * from './lib/combobox'
export * from './lib/color-contrast'
export * from './lib/focus-ring'
export * from './lib/stat-card'
export * from './lib/tone'

// 【说明】本文件只导出设计系统（ADR-103）。
//
// 早前这里堆了十几条“显式列出而非 export *：XXX 与 YYY 同名”的注释，
// 而那些模块（bots-overview / logs-filters / instance-prefetch / …）
// 早随业务代码回迁应用侧，在包内已不存在——注释比代码活得久，
// 让后来者以为包里还有这些模块。回迁完成后它们已全部删除。
//
// 新增设计系统模块时：能 `export *` 就通配；确需同名冲突时，
// 就地写明「是哪两个同名、语义差在哪、需要另一份时走哪个深路径」，
// 并删除对应模块回迁后遗留的注释。
