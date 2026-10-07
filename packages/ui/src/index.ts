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
export * from './lib/focus-ring'
export * from './lib/interaction-overlay'
export * from './lib/utils'
export * from './lib/threshold'
export * from './lib/brush'
export * from './lib/chart-hover'
export * from './lib/monitor-metrics'

// 业务视图（原 @jianmanager/biz-views 并入）：受控复合组件，不取数/不碰路由/不发请求/不弹 toast
export * from './components/views/TopLoadingBar'
export * from './components/views/ReleaseNotes'
export * from './components/views/UnifiedDiff'
export * from './components/views/UntrustedFieldBadge'
export * from './components/views/ClientDistFlowGuide'
export * from './components/views/client-dist/ReadinessStepper'
export * from './components/views/client-dist/ChannelCards'
export * from './components/views/client-dist/KeySecretDialogs'
export * from './components/views/client-dist/CreateChannelDialog'
export * from './components/views/client-dist/KeyEditDialogs'
export * from './components/views/client-dist/KeysSegment'
export * from './components/views/client-dist/ChannelSecuritySummaryBar'
export * from './components/views/alerts/ChannelDialogView'
export * from './components/views/alerts/RuleDialogView'
export * from './components/views/alerts/QQQrCode'
export * from './components/views/alerts/QQBindDialogView'
export * from './components/views/bots/BotListParts'
export * from './components/views/bot-load/session/DisclaimerBanner'
export * from './components/views/bot-load/session/ThresholdVerdict'
export * from './components/views/bot-load/session/SessionHeaderView'
export * from './components/views/bot-load/session/ConnectionFunnel'
export * from './components/views/bot-load/session/ExecutorDistribution'
export * from './components/views/bot-load/session/FailureTraceDrawer'
export * from './components/views/bot-load/LoadProfileEditor'
export * from './components/views/bot-load/ThresholdEditor'
export * from './components/views/bot-load/CapacityPlan'
export * from './components/views/client-dist/ObsTimeRangePicker'
export * from './components/views/console/BotHealthBar'
export * from './components/views/console/WorkspaceEmpty'
export * from './components/views/explorer/PromptDialog'
export * from './components/views/explorer/Toolbar'
export * from './components/views/explorer/editor/EditorShortcutsHelp'
export * from './components/views/file-browser/FileBrowserTree'

// ADR-097 起：应用业务组件按 a+b 双范式受控化并入，按域归档
export * from './components/views/nodes/NodePortsPanel'
export * from './components/views/nodes/NodeArtifactCachePanel'
export * from './components/views/nodes/NodeProbeVersionPanel'
export * from './components/views/nodes/NodeProxyPanel'
export * from './components/views/nodes/NodeGlobalPackagesSection'
export * from './components/views/nodes/NodeRepairPanel'
export * from './components/views/nodes/NodeLogRuntimePanel'
export * from './components/views/nodes/NodeRuntimeSection'
export * from './components/views/nodes/AddNodeDialog'
export * from './components/views/nodes/NodeJDKPanel'
export * from './components/views/nodes/PingNodeButton'
export * from './components/views/nodes/DirectoryPicker'

// instances 域（实例详情与控制台的分段/面板）
export * from './components/views/instances/HealthPanel'
export * from './components/views/instances/BinarySegment'
export * from './components/views/instances/InstanceEnvSegment'
export * from './components/views/instances/WorkspaceCard'
export * from './components/views/instances/RuntimeDriftNotice'
export * from './components/views/instances/InstancePicker'
export * from './components/views/instances/BinaryVersionPanel'
export * from './components/views/instances/SnapshotPanel'
export * from './components/views/instances/InstanceWorktableCard'
export * from './components/views/instances/ProcessPanel'
export * from './components/views/instances/MetricsFallbackSegment'
export * from './components/views/instances/MetricsTabSegment'
export * from './components/views/instances/InstanceTagsDialog'
export * from './components/views/instances/EditInstanceLimitsDialog'
export * from './components/views/instances/CreateBotDialog'
export * from './components/views/instances/InstanceBatchBar'
export * from './components/views/instances/RollingBatchDialog'
export * from './components/views/instances/ProxyRegistrationsDialog'
export * from './components/views/instances/EditInstanceConfigDialog'
export * from './components/views/instances/InstanceStatusDot'
export * from './components/views/instances/InstanceLibrary'
export * from './components/views/instances/InstanceGroupTree'
export * from './components/views/instances/InstanceGroupManager'
export * from './components/views/instances/ConfigSwitch'
export * from './components/views/instances/InstanceBackupSegment'
export * from './components/views/instances/CrashDiagnosticsCard'
export * from './components/views/instances/InstancePlayersSegment'
export * from './components/views/instances/ServerStateSegment'
export * from './components/views/instances/ServerSelector'
export * from './components/views/instances/InventorySegment'
export * from './components/views/instances/EconomySegment'
export * from './components/views/instances/BotStatusDot'
export * from './components/views/instances/BotSegment'
export * from './components/views/instances/MetricSourceChips'
export * from './components/views/instances/AttributionCard'
export * from './components/views/instances/CapacityForecastCard'
export * from './components/views/instances/MetricsSegment'
export * from './components/views/instances/PlayerTrendCard'
export * from './components/views/instances/SLOSection'
export * from './components/views/instances/InstanceRankingPanel'
export * from './components/views/instances/DrillTargetPicker'
export * from './components/views/instances/MonitoringPage'
export * from './components/views/instances/BusinessSegment'
export * from './components/views/CreateUserDialog'
export * from './components/views/CreateInvitationDialog'
export * from './components/views/CreateInstanceDialog'
export * from './components/views/instances/SidebarServerList'
export * from './components/views/instances/CommandPalette'
export * from './components/views/instances/VirtualizedInstanceTables'
export * from './components/views/instances/InstanceTableParts'
export * from './components/views/instances/InstanceCardViews'
export * from './components/views/DangerConfirm'
export * from './components/views/InstanceWizardPage'
export * from './lib/instance-wizard-options'
export * from './components/views/TemplatesPage'
export * from './lib/template-apply'
export * from './lib/roles'
export * from './lib/nav-config'
export * from './lib/header-layout'
export * from './lib/sidebar-logo'
export * from './lib/task-status'
export * from './components/views/console/TasksMenu'
export * from './components/views/console/NotificationBell'
export * from './components/views/console/ClusterBadges'
export * from './components/views/console/AccountMenu'
export * from './lib/explorer-selection'
export * from './lib/explorer-clipboard'
export * from './lib/explorer-tabs'
export * from './lib/explorer-clipboard-bus'
export * from './lib/explorer-save-key'
export * from './components/views/explorer/FileList'
export * from './lib/file-entry'
export * from './lib/explorer-language'
export * from './lib/explorer-comment'
export * from './lib/explorer-ide-extensions'
export * from './components/views/explorer/CodeEditor'
export * from './components/views/explorer/DecompileViewer'
export * from './lib/archive-tree'
export * from './components/views/explorer/SearchPanel'
export * from './components/views/explorer/ArchiveViewer'
export * from './lib/file-version'
export * from './components/views/explorer/VersionDrawer'
export * from './lib/discard-guard'
export * from './lib/nav-history'
export * from './components/views/explorer/ResourceExplorer'
export * from './components/views/explorer/ExplorerTabHost'
export { default as FileExplorer } from './components/views/explorer/FileExplorer'
export type { FileExplorerProps } from './components/views/explorer/FileExplorer'
export * from './lib/file-browser-capability'
export * from './components/views/file-browser/FilePreview'
export * from './components/views/file-browser/FileBrowser'
export * from './components/views/file-browser/UnifiedExplorerShell'
export * from './lib/storage-types'
// 显式列出而非 `export *`：`formatBytes` 与 `./lib/monitor-metrics` 同名（两者语义不同，
// 前者格式化存储占用、后者格式化监控指标），barrel 里不能同时通配导出。
// 需要存储版 `formatBytes` 的调用方走深路径 `@jianmanager/ui/lib/storage-view`。
export {
  deriveArchive,
  sortDirsByUsage,
  buildCrumbs,
  joinStoragePath,
} from './lib/storage-view'
export type { ArchiveDerived, Crumb } from './lib/storage-view'
export * from './lib/storage-source'
export * from './components/views/StoragePage'
export * from './components/views/console/BcSegment'
export * from './components/views/console/BcPlayersPanel'
export * from './components/views/console/InstanceActivityFeed'
export * from './components/views/console/TopologyGraph'
export * from './lib/node-types'
export * from './lib/node-summary'
export * from './lib/instance-summary'
export * from './lib/node-list'
export * from './lib/health-wall-types'
export * from './lib/health-wall'
export * from './lib/danger'
export * from './lib/instance-prefetch'
export * from './lib/console-log-types'
export * from './lib/capabilities'
export * from './lib/console-history'
export * from './lib/client-upload-plan'
export * from './lib/chunked-upload'
export * from './lib/efficient-upload'
export * from './lib/instance-grouping'
export * from './lib/instance-tree'
export * from './lib/instance-types'
export * from './lib/workspace-navigation'
export * from './lib/use-workspace-navigation'
export * from './lib/bot-load-types'
export * from './lib/bot-load-filters'
export * from './lib/bot-load-url-state'
export * from './lib/bot-load-report'
export * from './lib/bot-load-presets'
// 显式列出而非 `export *`：`formatBytes` 与 `./lib/monitor-metrics`、`FieldError` 与
// `./components/field-label` 同名（语义不同），barrel 里不能同时通配导出。
// 需要这两者的调用方走深路径 `@jianmanager/ui/lib/bot-load-metrics`。
export {
  LIVE_METRIC_MAX_POINTS,
  CHART_MAX_POINTS,
  clampChartPoints,
  formatLatencyMs,
  formatRatio,
  pickLatency,
  seriesWithNulls,
  appendMetricPoints,
} from './lib/bot-load-metrics'
// 同上：`FieldError` 与 `./components/field-label` 同名（此处是「校验错误条目」，
// 那边是「表单字段错误壳」）。需要本模块版的调用方走深路径。
export {
  validateCommandSchedule,
  validateLoadProfile,
  validateThresholds,
  validateConnection,
  validateCountMatchesProfile,
  previewBotNames,
} from './lib/bot-load-validation'
export * from './lib/bot-load-summaries'
export * from './lib/bot-load-draft'
export * from './lib/bot-load-session-store'
export * from './lib/bot-load-session-event-client'
export * from './lib/topology'
export * from './lib/inventory-view'
export * from './lib/director'
export * from './lib/quota-status'
export * from './lib/config-surface'
export * from './lib/terminal-session-manager'
export * from './lib/console-immersive-layout'
export * from './lib/console-players'
export * from './lib/director-render'
export * from './lib/runtime-drift'
export * from './lib/config-contracts'
export * from './lib/plugin-contracts'
export * from './lib/asset-contracts'
export * from './lib/api-error'
export * from './lib/privacy-mask'
export * from './lib/relative-time'
export * from './lib/password-strength'
export * from './lib/permission-explain'
export * from './lib/page-title'
export * from './lib/platform-stats'
export * from './lib/licenses'
export * from './lib/jwt'
export * from './lib/config-baseline'
export * from './lib/client-readiness'
export * from './lib/console-draft-registry'
export * from './lib/console-hot-cache'
export * from './lib/download-failure'
export * from './lib/metrics-source'
export * from './lib/instance-console-tabs'
export * from './lib/alert-helpers'
export * from './lib/alert-contracts'
export * from './lib/client-channel-types'
export * from './lib/bot-realtime-types'
export * from './lib/config-discover'
export * from './lib/config-favorites'
export * from './lib/webkit-entry-adapter'
export * from './lib/client-dist-query'
export * from './lib/logs-filters'
export * from './lib/schedule-form'
export * from './lib/db-contracts'
export * from './lib/db-rows-view'
export * from './components/views/console/NodeWorktableCard'
export * from './components/views/bot-load/CommandPlanEditor'
export * from './lib/client-dist-stats-contracts'
export * from './lib/client-dist-events-contracts'
export * from './lib/client-runtime-contracts'
export * from './lib/client-dist-machines-contracts'
export * from './components/views/client-dist/OpsShared'
export * from './components/views/client-dist/OpsStatisticsTab'
export * from './components/views/client-dist/OpsRealtimeTab'
export * from './components/views/client-dist/ObsOverviewView'
export * from './components/views/client-dist/OpsClientsTabView'
export * from './components/views/client-dist/MachineListPanelView'
export * from './components/views/client-dist/MachineTimelineView'
export * from './components/views/client-dist/SecurityEventsView'
export * from './components/views/client-dist/SecurityEventRowView'
// 显式列出而非 `export *`：`fmtTime` / `fmtBytes` 与 OpsShared、machine-format 同名（口径不同），
// 需要安全侧格式化函数的调用方走深路径 `@jianmanager/ui/components/views/client-dist/security-format`。
export { SECURITY_EMPTY, levelVariant, statusVariant, EmptyState } from './components/views/client-dist/security-format'
export * from './lib/client-dist-security-contracts'
// 显式列出而非 `export *`：`fmtBytes` / `fmtTime` 与 `./components/views/client-dist/OpsShared` 同名（口径不同），
// 需要机器清单版格式化函数的调用方走深路径 `@jianmanager/ui/components/views/client-dist/machine-format`。
export { resultBadge, lagBadge } from './components/views/client-dist/machine-format'
// 显式列出而非 `export *`：`SummaryChip` 与 `./components/summary-chips` 同名但语义不同
// （前者是可点击筛选 chip、后者是带状态等级的汇总 chip）；`ConfigSwitch` 已由
// `./components/views/instances/ConfigSwitch` 导出。两者都不能在 barrel 里同时通配。
// 需要 ConfigRow 版 SummaryChip 的调用方走深路径 `@jianmanager/ui/components/views/config-explorer/ConfigRow`。
export {
  ConfigViewToggle,
  ConfigSummaryChips,
  ConfigRow,
} from './components/views/config-explorer/ConfigRow'
export type { ConfigView } from './components/views/config-explorer/ConfigRow'
export * from './components/views/config-explorer/FavoritesBarView'
export * from './lib/client-dist-kpi'
export * from './lib/client-dist-observability-contracts'
export * from './components/views/client-dist/InsightCards'
export * from './components/views/client-dist/UpdateHeatmap'
export * from './components/views/client-dist/ClientFileTree'
export * from './components/views/client-dist/CleanScopeEditor'
export * from './lib/client-dist-ops-tab'
export * from './lib/zip-filename-decode'
export * from './lib/logs-federation/types'
export * from './lib/logs-federation/i18n'
export * from './lib/logs-federation/helpers'
export * from './lib/client-publish-wizard'
export * from './lib/settings-form'
export * from './lib/audit-contracts'
export * from './lib/audit-filters'
export * from './lib/bots-overview'
export * from './lib/runtime-assets-contracts'
// 显式列出而非 `export *`：`formatBytes` 与 `./lib/monitor-metrics` 同名（两者格式不同，
// 前者 1024 进制带 B/KB/MB 后缀、后者是监控指标的 G/M/K 缩写），barrel 里不能同时通配导出。
// 需要制品占用版 `formatBytes` 的调用方走深路径 `@jianmanager/ui/lib/runtime-assets-view`。
export {
  buildJDKMatrix,
  RUNTIME_TYPE_LABEL,
  buildRuntimeGrid,
  DEFAULT_ASSET_FILTER,
  filterAssetGroups,
  shortSha,
} from './lib/runtime-assets-view'
export type {
  JDKMatrixCell,
  JDKMatrix,
  JDKMatrixColumn,
  JDKMatrixRow,
  RuntimeGridCell,
  RuntimeGridColumn,
  RuntimeGridRow,
  RuntimeGrid,
  AssetFilter,
} from './lib/runtime-assets-view'
export * from './components/views/console/DirectorSceneStrip'
export * from './components/views/console/QuotaPanel'
export * from './components/views/console/InstanceConfigSurfacePanel'
export * from './components/views/console/MobileConsoleNav'
export * from './components/views/console/ConsoleCommandBar'
export * from './components/views/console/ConsoleOutputView'
export * from './components/views/console/InstanceConsoleView'
export * from './components/views/console/ConsoleImmersiveMode'
export * from './components/views/console/StoppedLogsView'
export * from './components/views/console/TerminalPane'
export * from './components/views/console/DirectorConsolePage'
export * from './components/views/console/SuperWorkbenchPage'
export * from './components/views/config-explorer/ConfigFileEditor'
export * from './components/views/plugins/PluginManager'
export * from './lib/theme'
export * from './components/views/console/ThemeSwitcher'
export * from './components/views/console/SidebarNavLink'
export * from './components/views/console/WorkspaceSidebar'
export * from './components/views/console/ConsoleSidebar'
export * from './components/views/console/sidebar-link'
export * from './components/views/console/console-header-parts'
export * from './components/views/console/metric-segment'
export * from './components/views/console/console-kpi-parts'
export * from './lib/artifact-cache'
export * from './lib/attribution'
export * from './lib/backup'
export * from './lib/bot-health'
// 显式列出而非 `export *`：`BotStatusKind` 与 `./lib/bots-overview` 的后端状态枚举同名但语义不同
// （此处是前端语义分桶 online/connecting/offline/error）；需要它的调用方走深路径 `@jianmanager/ui/lib/bot-list`。
export {
  indexBotBadgesByInstance,
  botStatusKind,
  summaryCounts,
  groupBots,
  parseBotConfig,
  suggestBotServer,
} from './lib/bot-list'
export type { InstanceBotBadge, BotStatusCounts, BotGroupBy, BotGroup } from './lib/bot-list'
export * from './lib/bot'
export * from './lib/breadcrumb'
export * from './lib/business-actions'
export * from './lib/business'
export * from './lib/clipboard'
export * from './lib/color-contrast'
export * from './lib/combobox'
export * from './lib/command-palette'
export * from './lib/console-ansi'
export * from './lib/console-command-history'
export * from './lib/console-completion'
export * from './lib/console-filter'
export * from './lib/console-line-buffer'
export * from './lib/console-log-line'
export * from './lib/console-search'
export * from './lib/console-selection'
export * from './lib/console-stack-block'
export * from './lib/console-wrap'
export * from './lib/crash'
export * from './lib/cron'
export * from './lib/economy'
export * from './lib/economy-view'
export * from './lib/file-browser-tree'
export * from './lib/file-browser-types'
export * from './lib/file-sort'
// 显式列出而非 `export *`：`FieldError` 与 `./components/field-label` 的展示组件同名但语义不同
// （此处是校验错误文本类型）；需要它的调用方走深路径 `@jianmanager/ui/lib/form-validation`。
export {
  validateRequired,
  minLength,
  validatePort,
  validatePositiveInt,
  validateNonNegativeNumber,
  validateResourceLimitNumber,
  validateAbsPath,
  validateUrl,
  validateEnvRef,
  validateHost,
  validateFields,
  hasErrors,
} from './lib/form-validation'
export type { FieldRules } from './lib/form-validation'
export * from './lib/instance-batch'
export * from './lib/instance-glow'
export * from './lib/instance-group-path'
export * from './lib/instance-group-tree'
export * from './lib/instance-group'
// 显式列出而非 `export *`：`DragPayload` 与 `./lib/explorer-clipboard-bus` 的同名类型语义不同
// （此处是工作台卡拖拽载荷）；需要它的调用方走深路径 `@jianmanager/ui/lib/instance-library`。
export {
  WORKSPACE_DND_MIME,
  encodeDragPayload,
  parseDragPayload,
  dragPayloadToCards,
  dedupeCards,
} from './lib/instance-library'
export * from './lib/instance-metrics'
export * from './lib/instance-rolling'
export * from './lib/instance-status'
export * from './lib/instance-tags'
export * from './lib/managed-process'
export * from './lib/metrics-availability'
export * from './lib/metric-series'
// 不导出：该模块仅有的 `fmtBytes` 与 `./components/views/client-dist/OpsShared` 同名（两者格式口径不同），
// 需要它的调用方走深路径 `@jianmanager/ui/lib/metrics-format`。
export * from './lib/node-ports'
export * from './lib/obs-window'
export * from './lib/paths'
export * from './lib/player-trend'
export * from './lib/player'
export * from './lib/proxy-registration'
export * from './lib/ranking'
export * from './lib/release-notes-link'
export * from './lib/schedule'
// 显式列出而非 `export *`：`HoverPrefetcher` 与 `./lib/instance-prefetch` 的同名类型语义不同；
// 需要它的调用方走深路径 `@jianmanager/ui/lib/server-selection`。
export type { StoredInstance } from './lib/server-selection'
export * from './lib/server-state'
export * from './lib/shortcuts'
export * from './lib/slo'
export * from './lib/stat-card'
export * from './lib/tone'
export * from './lib/use-card-columns'
export * from './lib/use-debounced'
export * from './lib/use-field-gate'
// 显式列出而非 `export *`：`VirtualWindow` / `VirtualWindowInput` 与 `./lib/logs-filters` 的同名类型语义不同
// （此处是通用虚拟列表窗口、后者是日志中心专用）；需要它们的调用方走深路径 `@jianmanager/ui/lib/virtual-list`。
export {
  virtualWindow,
  virtualWindowVaried,
  useVirtualRows,
} from './lib/virtual-list'
export type { VirtualWindowVariedInput } from './lib/virtual-list'
export * from './lib/workspace-card'
export * from './lib/workspace-preset'
