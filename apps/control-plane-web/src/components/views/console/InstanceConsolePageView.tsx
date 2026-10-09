/**
 * @file InstanceConsolePageView：服务器统一控制台页的受控视图——页头（状态/标题/元信息/操作/可收起指标条）、
 *       页签栏（WAI-ARIA tabs + 键盘循环 + 溢出渐隐）与概览面板（KPI / TPS 火花线 / 日志预览 / 动态与告警）
 *       接收容器注入的数据与回调；页签内容经 `renderTabPanel` 插槽按页签注入（各页签本体是应用侧取数接线层）。
 * @input lib/instance-types（InstanceInfo）、lib/instance-metrics（InstanceMetricsData）、lib/console-log-types（LogEntry）、
 *        lib/instance-console-tabs（TabKey 与 TAB_ICON/TAB_LABEL_KEY/TAB_GROUP_BREAK/TAB_CARD_TYPE）、
 *        lib/instance-glow（instanceStatusGlowClass）、lib/threshold（instanceStatusLevel）、lib/clipboard（copyToClipboard）、
 *        views/console/console-kpi-parts（KpiCard/formatNumber/formatUptime）、views/console/metric-segment（顶栏指标段原语）、
 *        views/instances/MetricSourceChips（来源标注）、Button/StatusBadge/Table/DropdownMenu 原语、layout（PageShell）、翻译上下文
 * @output InstanceConsolePageView、InstanceConsolePageViewProps、InstanceConsoleOverviewPanel、
 *         InstanceConsoleOverviewPanelProps、InstanceConsoleInstanceActions、InstanceConsolePlayersSummary、
 *         InstanceConsoleLinkArgs
 * @sync apps/control-plane-web/src/components/console/InstanceConsolePage.tsx、
 *        apps/control-plane-web/src/components/console/InstanceConsolePage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-269 统一控制台、FR-293 最近打开、FR-295/ADR-067 页签 keep-alive、
 *        FR-312 失败原因横幅、FR-331 搭建中禁启、FR-342 损毁重建、FR-343 TPS 真时序、FR-412 瘦身顶栏、
 *        FR-413/445/448 页签重组与能力门控、FR-417 失败原因复制、FR-422/423 视口自适应与分栏、FR-471 运行态漂移）
 */
import { Activity, Fragment, useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Activity as ActivityIcon,
  AlertTriangle,
  ChevronDown,
  ChevronUp,
  Copy,
  Gauge,
  Hammer,
  HardDrive,
  Layers,
  Loader2,
  MoreHorizontal,
  Play,
  RotateCw,
  Square,
  Users,
} from 'lucide-react'

import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { PageShell } from '@jianmanager/ui/components/layout'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { KpiCard, formatNumber, formatUptime } from '@/components/views/console/console-kpi-parts'
import { MetricDivider, MetricSegment, ProbeMissingChip } from '@/components/views/console/metric-segment'
import { MetricSourceChips } from '@/components/views/instances/MetricSourceChips'
import { copyToClipboard } from '@/lib/clipboard'
import type { LogEntry } from '@/lib/console-log-types'
import { instanceStatusGlowClass } from '@/lib/instance-glow'
import { TAB_CARD_TYPE, TAB_GROUP_BREAK, TAB_ICON, TAB_LABEL_KEY } from '@/lib/instance-console-tabs'
import type { TabKey } from '@/lib/instance-console-tabs'
import type { InstanceMetricsData } from '@/lib/instance-metrics'
import type { InstanceInfo } from '@/lib/instance-types'
import { instanceStatusLevel } from '@jianmanager/ui/lib/threshold'

/** 路由链接渲染参数：`to` 为应用侧路由路径，包内不认路由实现。 */
export interface InstanceConsoleLinkArgs {
  /** 应用侧路由路径（如 `/tasks`、`/logs?instanceId=1`）。 */
  to: string
  className?: string
  children: ReactNode
}

/**
 * 在线人数四元组：探针 server-state 与实例指标直探（SLP/Query）合并后的口径。
 *
 * 顶栏指标条与概览 KPI 共用同一份合并结果（故在容器算好注入，不在两处各算一次）；
 * 两个可用性位是 FR-446/447 的诚实标记：缺测时显「不可用」，不得以 0 冒充。
 */
export interface InstanceConsolePlayersSummary {
  /** 在线人数（探针优先，其次直探；缺测给 0，由 `onlineAvailable` 标注）。 */
  online: number
  /** 最大人数（同上）。 */
  maxPlayers: number
  /** 在线数是否可用。 */
  onlineAvailable: boolean
  /** 最大人数是否可用。 */
  maxAvailable: boolean
}

/** 页头写操作的回调集合：动作本体（mutation、权限、二次确认）全部在容器，本视图只上报意图。 */
export interface InstanceConsoleInstanceActions {
  /** 启动（STOPPED / CRASHED 可用）。 */
  start: () => void
  /** 重建（DAMAGED 可用：复用原搭建参数重跑）。 */
  rebuild: () => void
  /** 重启（运行态可用）。 */
  restart: () => void
  /** 停止（运行态可用）。 */
  stop: () => void
  /** 强杀入口：只上报意图，危险操作确认框由容器经 `killConfirmDialog` 注入。 */
  kill: () => void
}

/**
 * 控制台页受控视图的 props（注入契约）。
 *
 * 命名约定：会触发取数的状态（`activeTab` / `mountedTabs`）一律归容器；
 * 纯展示态（指标条折叠、窄视口分支、页签溢出）留本视图，不出现在这里。
 */
export interface InstanceConsolePageViewProps {
  /** 实例详情（页头标题/状态/端口/节点归属，以及操作按钮可用态的派生源）。 */
  instance: InstanceInfo
  /** 节点归属展示信息（缺 `name` 时回落「未知节点」文案）。 */
  node?: { name?: string; diskUsage?: number }
  /** 实例实时指标一拍（页头指标条与概览 KPI 共用）。 */
  metrics?: InstanceMetricsData
  /** 在线人数四元组（页头指标条与概览 KPI 共用同一合并口径）。 */
  players: InstanceConsolePlayersSummary
  /** 是否具备 MC 世界语义（FR-448）：只影响页内字段取舍与探针提示，不作页签级门控。 */
  mcSemantics: boolean
  /** 能力画像推导出的可见页签（有序，既是渲染集合也是方向键循环顺序）。 */
  visibleTabs: TabKey[]
  /** 当前激活页签（受控：与容器 `?tab=` 深链解析结果同源）。 */
  activeTab: TabKey
  /** 页签切换上报（容器写回 URL；切换会改变子组件取数，故不上提到本视图）。 */
  onActiveTabChange: (tab: TabKey) => void
  /** 已访问（保活）页签集合：容器在渲染期并入，本视图据此渲染 `<Activity>` 外壳。 */
  mountedTabs: TabKey[]
  /** 搭建中（FR-331）：禁用启动入口并给琥珀状态横幅。 */
  provisioning: boolean
  /** 重建在途（FR-342）：禁用重建按钮且不落红色失败横幅。 */
  rebuilding: boolean
  /** 实例写权限（FR-432）：false 时全部写操作禁用并给 tooltip。 */
  canOperate: boolean
  /** 页头写操作回调集合。 */
  actions: InstanceConsoleInstanceActions
  /** 提示通道：复制回执等文案由本视图算好，交给容器弹 toast（包内不引 sonner）。 */
  onNotify?: (kind: 'success' | 'error', message: string) => void
  /** 渲染路由链接（构造 `/tasks`、查看全部日志等入口）；缺省退化为原生 `<a href>`。 */
  renderLink?: (args: InstanceConsoleLinkArgs) => ReactNode
  /** 运行态漂移横幅（FR-471）：自带接管 mutation 的接线组件，由容器注入。 */
  runtimeDriftBanner?: ReactNode
  /** 强杀二次确认弹窗（FR-059）：含角色门禁与 kill mutation，由容器注入；本视图只负责挂载位置。 */
  killConfirmDialog?: ReactNode
  /** 渲染页签内容：容器按页签注入对应接线层组件（各页签本体自取数）。 */
  renderTabPanel: (tab: TabKey) => ReactNode
}

/**
 * 服务器统一控制台页的受控视图（FR-269）。
 *
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast、不触达路由**。
 * - `activeTab` / `mountedTabs` 归容器：前者与 `?tab=` 深链同源且决定子组件取数，后者是 keep-alive 生命周期宿主；
 * - 首屏实例未就绪的早退分支（`serverConsole.noInstance`）留容器——它是实例查询的错误/加载态，不是本视图的呈现；
 * - 纯展示态留本视图：指标条折叠偏好（localStorage）、窄视口分支、页签溢出渐隐与激活项滚动。
 */
export function InstanceConsolePageView({
  instance,
  node,
  metrics,
  players,
  mcSemantics,
  visibleTabs,
  activeTab,
  onActiveTabChange,
  mountedTabs,
  provisioning,
  rebuilding,
  canOperate,
  actions,
  onNotify,
  renderLink,
  runtimeDriftBanner,
  killConfirmDialog,
  renderTabPanel,
}: InstanceConsolePageViewProps) {
  const { t } = useTranslation()

  // 指标条折叠偏好（FR-412）：跨实例与刷新保留，收起后顶栏再省一行给内容区。
  const [metricsBarOpen, setMetricsBarOpen] = useState(readMetricsBarPref)
  useEffect(() => {
    try { localStorage.setItem(METRICS_BAR_KEY, metricsBarOpen ? '1' : '0') } catch { /* 隐私模式忽略 */ }
  }, [metricsBarOpen])

  // Tab 栏：激活项滚进视野 + 仅在真溢出时加边缘渐隐。
  const tabRefs = useRef(new Map<TabKey, HTMLButtonElement>())
  const navRef = useRef<HTMLElement>(null)
  const [navOverflowing, setNavOverflowing] = useState(false)
  useEffect(() => {
    const el = navRef.current
    if (!el) return
    const sync = () => setNavOverflowing(el.scrollWidth > el.clientWidth + 4)
    sync()
    window.addEventListener('resize', sync)
    return () => window.removeEventListener('resize', sync)
  }, [])
  useEffect(() => {
    // matchMedia/scrollIntoView 均带存在性守卫：jsdom 未实现，测试环境不应炸。
    const reduceMotion =
      typeof window !== 'undefined' &&
      typeof window.matchMedia === 'function' &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches
    tabRefs.current.get(activeTab)?.scrollIntoView?.({
      inline: 'nearest',
      block: 'nearest',
      behavior: reduceMotion ? 'auto' : 'smooth',
    })
  }, [activeTab])
  const isNarrow = useIsNarrowViewport()
  // roving tabindex 简化版：方向键移动焦点并激活（Tab 数量少，激活随焦点走最直觉）。
  const activateSiblingTab = (current: TabKey, delta: 1 | -1) => {
    const index = visibleTabs.indexOf(current)
    const next = visibleTabs[(index + delta + visibleTabs.length) % visibleTabs.length]
    tabRefs.current.get(next)?.focus()
    onActiveTabChange(next)
  }
  const activateEdgeTab = (edge: 'first' | 'last') => {
    const key = edge === 'first' ? visibleTabs[0] : visibleTabs[visibleTabs.length - 1]
    tabRefs.current.get(key)?.focus()
    onActiveTabChange(key)
  }

  const canStart = instance.status === 'STOPPED' || instance.status === 'CRASHED'
  const canControl = instance.status === 'RUNNING' || instance.status === 'STARTING' || instance.status === 'STOPPING'
  const isDamaged = instance.status === 'DAMAGED'
  // 失败原因横幅（FR-312）：只看 statusReason 非空、不看 status——Worker 心跳会把 CRASHED
  // 冲回 STOPPED，若以状态为前置条件横幅会随之消失；再次启动时 CP transition 清空 reason，
  // 横幅纯受查询数据驱动消失，不留本地状态。
  // 搭建中的 statusReason 是进行时状态而非失败（FR-331）：不落红色失败横幅，走下方琥珀状态横幅。
  const startFailReason = provisioning || rebuilding ? undefined : instance.statusReason?.trim()
  // <md 主操作（可用性增强）：按状态给唯一带文字的主按钮，其余收进「更多」菜单。
  const primaryAction = canStart
    ? { label: t('instances.start'), icon: Play, disabled: provisioning || !canOperate, title: !canOperate ? t('permissions.operateDenied') : provisioning ? t('instances.provisioningBlocked') : undefined, onClick: actions.start }
    : isDamaged
      ? { label: t('serverConsole.rebuild'), icon: Hammer, disabled: rebuilding || !canOperate, title: !canOperate ? t('permissions.operateDenied') : rebuilding ? t('serverConsole.rebuilding') : undefined, onClick: actions.rebuild }
      : canControl
        ? { label: t('serverConsole.restart'), icon: RotateCw, disabled: !canOperate, title: !canOperate ? t('permissions.operateDenied') : undefined, onClick: actions.restart }
        : null

  const richMetricsAvailable = metrics?.probeAvailable ?? false
  // 探针缺失芯片只在 MC 世界语义实例出现——非 MC 实例本不该有 ServerProbe，提示只会误导。
  const showProbeChip = mcSemantics && !richMetricsAvailable

  const linkTo = (args: InstanceConsoleLinkArgs) =>
    renderLink ? renderLink(args) : <a href={args.to} className={args.className}>{args.children}</a>

  return (
    // 视口自适应骨架（FR-422）：根与内层都是 flex 列，横幅/顶栏/Tab 栏 flex-none、
    // Tab 内容区 flex-1 min-h-0——滚动收口到页内卡片，顶栏常驻可见、底部不留白。
    //
    // 阶段 6 页面迁移：外壳改用布局层 PageShell 的 tool 变体（gap-0 p-0）。
    // 实例路由在 Workspace 里走 isFixedViewportRoute 分支、外层已带 p-3，
    // 页内若再叠加 PageShell 默认的 px-[25px] py-[22px]，间距会翻倍。
    // data-page 由 PageShell spread 透传，e2e 的就绪信号依赖它。
    <PageShell variant="tool" data-page="instance-console" className="text-[13px] text-foreground">
      <div className="flex min-h-0 flex-1 flex-col gap-2">
        {startFailReason && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-md border border-status-danger/40 bg-status-danger/10 px-3 py-2 text-xs text-status-danger"
          >
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <p className="font-semibold">{t('serverConsole.lastStartFailed')}</p>
              {/* 原因全文可读：不 truncate / line-clamp，长错误换行展示。 */}
              <p className="mt-0.5 whitespace-pre-wrap break-words">{startFailReason}</p>
            </div>
            {/* FR-417：失败原因常是一段带路径/堆栈的长文本，要贴去搜索或问人——
                原先只能拖选。复制走 copyToClipboard（含 HTTP 非安全上下文兜底），回执经 onNotify 上报。 */}
            <button
              type="button"
              onClick={() => {
                void copyToClipboard(`${t('serverConsole.lastStartFailed')}: ${startFailReason}`).then((ok) => {
                  if (ok) onNotify?.('success', t('common.copied'))
                  else onNotify?.('error', t('common.copyFailed'))
                })
              }}
              aria-label={t('serverConsole.copyFailReason')}
              className="mt-0.5 flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 hover:bg-status-danger/15"
            >
              <Copy className="size-3" />
              {t('common.copy')}
            </button>
          </div>
        )}
        {/* 搭建中状态横幅（FR-331）：琥珀而非红（是进行时不是失败），随 provision 任务终态清 reason 自动消失。 */}
        {provisioning && (
          <div
            role="status"
            className="flex items-start gap-2 rounded-md border border-status-warning/40 bg-status-warning/10 px-3 py-2 text-xs text-status-warning"
          >
            <Loader2 className="mt-0.5 size-3.5 shrink-0 animate-spin" />
            <div className="min-w-0">
              <p className="font-semibold">{t('instances.provisioningTitle')}</p>
              <p className="mt-0.5 whitespace-pre-wrap break-words">{instance.statusReason}</p>
              {linkTo({
                to: '/tasks',
                className: 'mt-0.5 inline-block font-medium underline underline-offset-2',
                children: t('instances.provisioningGoTasks'),
              })}
            </div>
          </div>
        )}
        {/* 运行态漂移告警（FR-471）：工作目录下存在未纳管的活进程——面板可能显示已停止而磁盘在跑，
            直接「启动」会双开（后端预检拦，但用户要先看到才有机会接管）。漂移字段由心跳写入，
            接管成功后后端清零 → 本条随查询刷新自动消失，不留本地状态。 */}
        {runtimeDriftBanner}
        {/* 瘦身顶栏（FR-412）：标题只留实例名（原「服务器控制台 /」前缀与无信息副标题已删），
            节点/端口/运行时长压成一行内联元信息，指标从 7 格 MetaCell 网格改为可收起的 pill 条。 */}
        <header className={cn('flex-none rounded-lg border bg-card/95 px-3 py-2 shadow-soft backdrop-blur-sm', instanceStatusGlowClass(instance.status))}>
          <div className="flex flex-wrap items-center gap-2">
            <div className="flex min-w-0 items-center gap-2">
              <StatusBadge
                level={instanceStatusLevel(instance.status)}
                label={t(`instances.${instance.status.toLowerCase()}`, instance.status)}
                pulse={instance.status === 'STARTING' || instance.status === 'STOPPING'}
              />
              <h1 className="truncate text-base font-semibold tracking-tight">{instance.name}</h1>
              <div className="hidden min-w-0 items-center gap-1.5 border-l pl-2 text-xs text-muted-foreground md:flex">
                <span className="truncate">{node?.name ?? t('console.unknownNode', { id: instance.nodeId })}</span>
                <span aria-hidden>·</span>
                <span className="font-mono">:{instance.serverPort || '—'}</span>
                <span aria-hidden>·</span>
                <span className="whitespace-nowrap">{t('serverConsole.uptime')} <em className="font-mono not-italic text-foreground">{formatUptime(metrics?.uptimeSeconds)}</em></span>
                <span aria-hidden>·</span>
                <span className="font-mono">{instance.uuid.slice(0, 8)}</span>
              </div>
            </div>
            {/* 桌面：五钮平铺（原样）。 */}
            {!isNarrow && (
            <div className="ml-auto flex flex-wrap items-center gap-1.5">
              {canStart && (
                // 禁用按钮带 disabled:pointer-events-none，tooltip 由外层 span 承载（FR-331）。
                <span title={!canOperate ? t('permissions.operateDenied') : provisioning ? t('instances.provisioningBlocked') : undefined}>
                  <Button size="sm" disabled={provisioning || !canOperate} data-testid="instance-operate-start" onClick={actions.start}>
                    <Play className="size-3.5" />
                    {t('instances.start')}
                  </Button>
                </span>
              )}
              {isDamaged && (
                // 损毁实例（FR-342）：显「重建」复用参数重跑搭建；重建在途禁用（tooltip 提示）。
                <span title={!canOperate ? t('permissions.operateDenied') : rebuilding ? t('serverConsole.rebuilding') : undefined}>
                  <Button size="sm" disabled={rebuilding || !canOperate} data-testid="instance-operate-rebuild" onClick={actions.rebuild}>
                    <Hammer className="size-3.5" />
                    {t('serverConsole.rebuild')}
                  </Button>
                </span>
              )}
              <Button size="sm" variant="outline" disabled={!canControl || !canOperate} data-testid="instance-operate-restart" title={!canOperate ? t('permissions.operateDenied') : undefined} onClick={actions.restart}>
                <RotateCw className="size-3.5" />
                {t('serverConsole.restart')}
              </Button>
              <Button size="sm" variant="outline" disabled={!canControl || !canOperate} data-testid="instance-operate-stop" title={!canOperate ? t('permissions.operateDenied') : undefined} onClick={actions.stop}>
                <Square className="size-3.5" />
                {t('serverConsole.stop')}
              </Button>
              <Button size="sm" variant="destructive" disabled={!canControl || !canOperate} data-testid="instance-operate-kill" title={!canOperate ? t('permissions.operateDenied') : undefined} onClick={actions.kill}>
                <AlertTriangle className="size-3.5" />
                {t('serverConsole.kill')}
              </Button>
            </div>
            )}
            {/* 窄屏：主操作 + 「更多」菜单，顶栏不再被四五个按钮挤成三行。 */}
            {isNarrow && (
            <div className="ml-auto flex items-center gap-1.5">
              {primaryAction && (
                <span title={primaryAction.title}>
                  <Button size="sm" disabled={primaryAction.disabled} onClick={primaryAction.onClick}>
                    <primaryAction.icon className="size-3.5" />
                    {primaryAction.label}
                  </Button>
                </span>
              )}
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button size="sm" variant="outline" aria-label={t('serverConsole.moreActions')} title={t('serverConsole.moreActions')}>
                    <MoreHorizontal className="size-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem disabled={!canControl} onClick={actions.restart}>
                    <RotateCw className="size-3.5" />
                    {t('serverConsole.restart')}
                  </DropdownMenuItem>
                  <DropdownMenuItem disabled={!canControl} onClick={actions.stop}>
                    <Square className="size-3.5" />
                    {t('serverConsole.stop')}
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    disabled={!canControl}
                    className="text-status-danger focus:text-status-danger"
                    onClick={actions.kill}
                  >
                    <AlertTriangle className="size-3.5" />
                    {t('serverConsole.kill')}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
            )}
          </div>

          {metricsBarOpen && (
            <div className="mt-1.5 flex flex-wrap items-center gap-x-2.5 gap-y-1">
              {/* 方案 B 分段状态条（FR-412/446/447/448）：TPS/MSPT 是世界语义专属字段（mcSemantics），
                  仅在 MC 实例出现；探针可用显真实值，否则显「不可用」；在线数由探针 → SLP → Query
                  任一来源提供，三源皆无亦显「不可用」——不再以 0 冒充「0 人在线」；
                  来源由右侧来源芯片标注（探针/SLP/Query）。 */}
              {mcSemantics && (richMetricsAvailable ? (
                <>
                  <MetricSegment label={t('serverConsole.tps')} value={formatNumber(metrics?.tps, 1)} tone={metrics?.tps != null && metrics.tps < 18 ? 'warn' : undefined} dot />
                  <MetricDivider />
                  <MetricSegment label={t('serverConsole.mspt')} value={`${formatNumber(metrics?.msptMillis, 0)}ms`} tone={metrics?.msptMillis != null && metrics.msptMillis > 50 ? 'danger' : undefined} dot />
                  <MetricDivider />
                </>
              ) : (
                <>
                  <MetricSegment label={t('serverConsole.tps')} value={t('metrics.unavailable')} />
                  <MetricDivider />
                </>
              ))}
              <MetricSegment
                label={t('serverConsole.online')}
                value={players.onlineAvailable ? `${players.online}/${players.maxAvailable ? players.maxPlayers : '—'}` : t('metrics.unavailable')}
              />
              <MetricDivider />
              <MetricSegment label={t('serverConsole.cpu')} value={`${Math.round(metrics?.cpuPercent ?? 0)}%`} tone={(metrics?.cpuPercent ?? 0) > 85 ? 'warn' : undefined} />
              <MetricDivider />
              <MetricSegment label={t('serverConsole.memory')} value={(metrics?.heapMaxMb ?? 0) > 0 ? `${formatNumber(metrics?.memoryMb, 0)}/${formatNumber(metrics?.heapMaxMb, 0)}M` : `${formatNumber(metrics?.memoryMb, 0)}M`} />
              <MetricDivider />
              <MetricSegment label={t('serverConsole.diskNode')} value={`${Math.round(node?.diskUsage ?? 0)}%`} tone={(node?.diskUsage ?? 0) > 85 ? 'warn' : undefined} />
              <span className="ml-auto flex items-center gap-1.5">
                <MetricSourceChips metrics={metrics ?? {}} />
                {showProbeChip && <ProbeMissingChip />}
              </span>
              {/* 元信息在窄屏没进标题行，补一条 pill 兜住（md 以下） */}
              <span className="rounded-full border bg-muted/70 px-2 py-0.5 text-[11px] text-muted-foreground md:hidden">
                {node?.name ?? t('console.unknownNode', { id: instance.nodeId })} · <span className="font-mono">:{instance.serverPort || '—'}</span>
              </span>
              <button
                type="button"
                onClick={() => setMetricsBarOpen(false)}
                aria-expanded={metricsBarOpen}
                className="flex items-center gap-0.5 rounded-full px-2 py-0.5 text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
              >
                <ChevronUp className="size-3" />
                {t('serverConsole.collapseMetrics', '收起指标')}
              </button>
            </div>
          )}
          {!metricsBarOpen && (
            <button
              type="button"
              onClick={() => setMetricsBarOpen(true)}
              aria-expanded={metricsBarOpen}
              className="mt-1 flex items-center gap-0.5 rounded-full px-2 py-0.5 text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            >
              <ChevronDown className="size-3" />
              {t('serverConsole.expandMetrics', '展开指标')}
            </button>
          )}
        </header>

        {/* WAI-ARIA tabs（可用性增强）：方向键循环 + roving tabindex + aria-selected；
            溢出时两侧渐隐提示可横滚，激活项由 effect 自动滚进视野。 */}
        <nav
          ref={navRef}
          role="tablist"
          aria-label={t('serverConsole.instanceTabs')}
          onKeyDown={(event) => {
            if (event.key === 'ArrowRight') {
              event.preventDefault()
              activateSiblingTab(activeTab, 1)
            } else if (event.key === 'ArrowLeft') {
              event.preventDefault()
              activateSiblingTab(activeTab, -1)
            } else if (event.key === 'Home') {
              event.preventDefault()
              activateEdgeTab('first')
            } else if (event.key === 'End') {
              event.preventDefault()
              activateEdgeTab('last')
            }
          }}
          className={cn(
            'flex flex-none gap-1 overflow-x-auto rounded-lg border bg-card/95 px-2 pt-1.5 shadow-soft backdrop-blur-sm',
            navOverflowing && '[mask-image:linear-gradient(to_right,transparent,black_16px,black_calc(100%-16px),transparent)]',
          )}
        >
          {visibleTabs.map((key) => {
            const Icon = TAB_ICON[key]
            return (
              <Fragment key={key}>
                {TAB_GROUP_BREAK.has(key) && <span aria-hidden className="my-1.5 w-px shrink-0 bg-border" />}
                <button
                  ref={(el) => {
                    if (el) tabRefs.current.set(key, el)
                    else tabRefs.current.delete(key)
                  }}
                  id={`instance-tab-${key}`}
                  role="tab"
                  type="button"
                  aria-selected={activeTab === key}
                  aria-controls={`instance-tabpanel-${key}`}
                  tabIndex={activeTab === key ? 0 : -1}
                  onClick={() => onActiveTabChange(key)}
                  className={cn(
                    'inline-flex shrink-0 items-center gap-1.5 border-b-2 px-2.5 py-1.5 text-xs font-medium transition-colors',
                    activeTab === key
                      ? 'border-primary text-primary'
                      : 'border-transparent text-muted-foreground hover:text-foreground',
                  )}
                >
                  <Icon className="size-3.5 shrink-0" />
                  {t(TAB_LABEL_KEY[key])}
                </button>
              </Fragment>
            )
          })}
        </nav>

        {/* 页签 keep-alive（FR-295）：访问过的页签全部保持挂载，非活跃者 Activity 隐藏——
            DOM/本地状态（终端缓冲、文件树展开态、未保存草稿）保留，轮询自动暂停。
            保活集合由容器持有（它会改变子组件取数）；本视图只渲染外壳，内容经 renderTabPanel 注入。
            FR-448：仅渲染画像可见 Tab（实例加载后画像可能收窄，须过滤掉残留的旧 mounted 项）。 */}
        {mountedTabs.filter((tab) => visibleTabs.includes(tab)).map((tab) => (
          <Activity key={tab} mode={tab === activeTab ? 'visible' : 'hidden'}>
            {/* 每个页签自身是 flex 列容器（FR-422）：吃满内容区剩余高度。
                卡片型页签（终端/文件/监控…）内部自己滚，故此层 hidden；
                纵向堆叠型页签（概览/环境变量/玩家/备份）在此层滚。 */}
            <div
              id={`instance-tabpanel-${tab}`}
              role="tabpanel"
              aria-labelledby={`instance-tab-${tab}`}
              className={cn(
              'flex min-h-0 flex-col',
              tab === activeTab ? 'flex-1' : 'hidden',
              TAB_CARD_TYPE[tab] ? 'overflow-hidden' : 'overflow-auto',
            )}>
            {renderTabPanel(tab)}
            </div>
          </Activity>
        ))}
      </div>

      {/* 强杀二次确认（FR-059）：与实例列表页同款 DangerConfirm，组管理员及以上可确认。 */}
      {killConfirmDialog}
    </PageShell>
  )
}

const METRICS_BAR_KEY = 'console.metricsBar'

function readMetricsBarPref(): boolean {
  try { return localStorage.getItem(METRICS_BAR_KEY) !== '0' } catch { return true }
}

/**
 * 窄视口检测（顶栏按钮收敛用）。
 *
 * 用 JS 状态而不是 `md:hidden` CSS：隐藏式双渲染会让两组按钮同时进可访问树，
 * 屏幕阅读器会读到重复的「启动/强杀」；真渲染分支在桌面端完全不挂载移动组。
 * matchMedia 缺失（jsdom/老 WebView）回退桌面布局。
 */
function useIsNarrowViewport(): boolean {
  const [narrow, setNarrow] = useState(() =>
    typeof window !== 'undefined' && typeof window.matchMedia === 'function'
      ? window.matchMedia('(max-width: 767px)').matches
      : false,
  )
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const mql = window.matchMedia('(max-width: 767px)')
    const sync = () => setNarrow(mql.matches)
    sync()
    mql.addEventListener?.('change', sync)
    return () => mql.removeEventListener?.('change', sync)
  }, [])
  return narrow
}

/** 「查看全部日志」入口样式（renderLink 注入与原生 `<a>` 兜底共用）。 */
const LOGS_LINK_CLASS =
  'shrink-0 text-[11px] text-muted-foreground underline-offset-2 transition-colors hover:text-primary hover:underline'

/** 概览面板的注入契约（数据由容器取数后传入；本面板不取数、不弹 toast）。 */
export interface InstanceConsoleOverviewPanelProps {
  /** 实例 id（日志预览「查看全部」深链的 `?instanceId=` 参数）。 */
  instanceId: number
  /** 实例实时指标一拍（KPI 与 TPS 可用性判定共用）。 */
  metrics?: InstanceMetricsData
  /** 在线人数（探针 → SLP → Query 任一命中；缺测给 0，由 `playersAvailable` 标注不可用）。 */
  online: number
  /** 最大人数（缺测给 0，由 `maxPlayersAvailable` 标注不可用——不以 0 冒充）。 */
  maxPlayers: number
  /** 在线数是否可用。 */
  playersAvailable: boolean
  /** 最大人数是否可用。 */
  maxPlayersAvailable: boolean
  /** 节点磁盘占用百分比（无节点数据时 undefined，按 0 呈现）。 */
  nodeDiskUsage?: number
  /** 最近日志（新的在前；概览只渲染前 8 条）。 */
  logs: LogEntry[]
  /** 关注事项（容器 `buildWatchItems` 产出，空数组=一切正常）。 */
  watchItems: string[]
  /** 探针连接态（探针横幅与动态流行共用）。 */
  probeConnected: boolean
  /** 是否具备 MC 世界语义（FR-448）：只决定本面板内字段取舍，不作页签级门控。 */
  mcSemantics: boolean
  /** TPS 时序点位（容器取数后注入；空数组=暂无序列，不画假图）。 */
  tpsPoints: number[]
  /** 动态与告警流（自带崩溃诊断接线的组件，容器注入）。 */
  activityFeed?: ReactNode
  /** 渲染「查看全部日志」路由链接；缺省退化为原生 `<a href>`。 */
  renderLink?: (args: InstanceConsoleLinkArgs) => ReactNode
}

/**
 * 概览页签面板（FR-269 / FR-423 / FR-343 / FR-446·447·448）。
 *
 * 纯展示 + 派生计算：KPI 卡、TPS 火花线（由 `tpsPoints` 折条与统计，aria 摘要读得到 min/max/avg）、
 * 日志预览表与右栏「动态与告警」流。所有数据、时序点位与日志都由容器注入——本面板不取数。
 */
export function InstanceConsoleOverviewPanel({
  instanceId,
  metrics,
  online,
  maxPlayers,
  playersAvailable,
  maxPlayersAvailable,
  nodeDiskUsage,
  logs,
  watchItems,
  probeConnected,
  mcSemantics,
  tpsPoints,
  activityFeed,
  renderLink,
}: InstanceConsoleOverviewPanelProps) {
  const { t } = useTranslation()
  const hasHeapMax = (metrics?.heapMaxMb ?? 0) > 0
  const memoryPct = hasHeapMax ? Math.min(100, Math.round((metrics!.memoryMb / metrics!.heapMaxMb) * 100)) : 0
  const cpuPct = Math.round(metrics?.cpuPercent ?? 0)
  const diskPct = Math.round(nodeDiskUsage ?? 0)
  const alertCount = watchItems.length
  const hasProbe = metrics?.probeAvailable ?? false
  // FR-343 去 mock-api：实例 TPS 真实时序火花线（取末段点位），无数据/无探针显空态而非假图。
  // 点位由容器按实例 uuid 取数注入（隐藏页签停轮询的既有语义不变）。
  const tpsBars = tpsPoints.slice(-24).map((v) => Math.max(2, Math.min(100, (v / 20) * 100)))
  // 火花线的 aria 摘要（可访问性）：屏幕阅读器拿得到 min/max/avg，而不是一片空 span。
  const tpsStats = tpsPoints.length > 0
    ? {
        min: Math.min(...tpsPoints),
        max: Math.max(...tpsPoints),
        avg: tpsPoints.reduce((sum, v) => sum + v, 0) / tpsPoints.length,
      }
    : null

  return (
    // 分栏重排（FR-423）：KPI 一行在上，下方左 62%（图表 + 日志）/ 右 38%（动态与告警）。
    // 全部 flex 撑满内容区，不再整块纵向堆叠后在底部留白。
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="grid flex-none gap-2 [grid-template-columns:repeat(auto-fit,minmax(122px,1fr))]">
        <KpiCard icon={Gauge} label={t('serverConsole.cpu')} value={`${cpuPct}%`} progress={Math.min(100, cpuPct)} />
        <KpiCard icon={HardDrive} label={t('serverConsole.memory')} value={hasHeapMax ? `${memoryPct}%` : `${formatNumber(metrics?.memoryMb, 0)} MB`} sub={hasHeapMax ? `${formatNumber(metrics?.memoryMb, 0)} / ${formatNumber(metrics?.heapMaxMb, 0)} MB` : 'RSS'} progress={memoryPct} />
        {/* TPS 是世界语义专属字段（FR-448）：仅具备 MC 世界语义的实例展示，proxy/generic 不显示伪 TPS；
            探针不可用时显「不可用」而非 0（FR-446/447 诚实标记）。 */}
        {mcSemantics && (
          <KpiCard icon={ActivityIcon} label={t('serverConsole.tps')} value={hasProbe ? formatNumber(metrics?.tps, 1) : t('metrics.unavailable')} progress={hasProbe ? Math.min(100, ((metrics?.tps ?? 0) / 20) * 100) : 0} />
        )}
        <KpiCard icon={Users} label={t('serverConsole.online')} value={mcSemantics ? (playersAvailable ? `${online}/${maxPlayersAvailable ? maxPlayers : '—'}` : t('metrics.unavailable')) : String(online)} progress={mcSemantics && playersAvailable && maxPlayers > 0 ? (online / maxPlayers) * 100 : 0} />
        <KpiCard icon={Layers} label={t('serverConsole.diskNode')} value={`${diskPct}%`} progress={diskPct} />
        <KpiCard icon={AlertTriangle} label={t('serverConsole.alerts')} value={String(alertCount)} danger={alertCount > 0} progress={alertCount > 0 ? 100 : 0} />
      </div>

      {/* 探针缺失横幅是世界语义专属（FR-448）：非 MC 实例（proxy/generic/beacon）本不该有
          ServerProbe，横幅只会误导；与顶栏 showProbeChip 的 mcSemantics 门控保持一致。 */}
      {mcSemantics && !probeConnected && (
        <div className="rounded-md border border-status-warning/40 bg-status-warning/10 px-3 py-2 text-xs text-status-warning">
          {t('serverConsole.probeUnavailable')}
        </div>
      )}

      <div className="flex min-h-0 flex-1 flex-col gap-2 xl:flex-row">
        {/* 左栏 62%：图表在上（占 42% 高）、日志表在下吃满剩余——日志需要横向空间放消息全文。 */}
        <div className="flex min-h-0 flex-[1.6] flex-col gap-2">
          <section className="flex min-h-0 flex-[0_0_42%] flex-col rounded-lg border bg-card shadow-soft">
            <h2 className="flex-none border-b px-3 py-2 text-sm font-semibold">{mcSemantics ? `${t('serverConsole.tps')} / ${t('serverConsole.mspt')}` : t('serverConsole.nonMcTitle')}</h2>
            {!mcSemantics ? (
              <div className="flex min-h-0 flex-1 items-center justify-center px-3 text-center text-xs text-muted-foreground">
                {t('serverConsole.nonMcHint')}
              </div>
            ) : hasProbe && tpsBars.length > 0 ? (
              <div
                role="img"
                aria-label={tpsStats ? t('serverConsole.tpsSparklineAria', {
                  min: tpsStats.min.toFixed(1),
                  max: tpsStats.max.toFixed(1),
                  avg: tpsStats.avg.toFixed(1),
                }) : undefined}
                className="grid min-h-0 flex-1 grid-cols-24 items-end gap-1 p-3"
              >
                {tpsBars.map((v, i) => (
                  <span key={i} className="rounded-t-sm bg-primary/75" style={{ height: `${v}%` }} />
                ))}
              </div>
            ) : (
              <div className="flex min-h-0 flex-1 items-center justify-center px-3 text-center text-xs text-muted-foreground">
                {hasProbe ? t('serverConsole.noSeriesYet') : t('serverConsole.probeUnavailable')}
              </div>
            )}
          </section>

          <section className="flex min-h-0 flex-1 flex-col rounded-lg border bg-card shadow-soft">
            <div className="flex flex-none items-center justify-between gap-2 border-b px-3 py-2">
              <h2 className="text-sm font-semibold">{t('serverConsole.logsPreview')}</h2>
              {/* 预览只有 8 行：给一条到日志中心的出口（FR-403 页面接受 ?instanceId=），行与全量接起来。 */}
              {renderLink
                ? renderLink({
                    to: `/logs?instanceId=${instanceId}`,
                    className: LOGS_LINK_CLASS,
                    children: `${t('serverConsole.viewAllLogs')} →`,
                  })
                : (
                  <a href={`/logs?instanceId=${instanceId}`} className={LOGS_LINK_CLASS}>
                    {t('serverConsole.viewAllLogs')} →
                  </a>
                )}
            </div>
            <div className="min-h-0 flex-1 overflow-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-20">{t('serverConsole.logTime')}</TableHead>
                    <TableHead className="w-16">{t('serverConsole.logLevel')}</TableHead>
                    <TableHead>{t('serverConsole.logMessage')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {logs.slice(0, 8).map((log) => (
                    <TableRow key={log.id}>
                      <TableCell className="font-mono text-xs text-muted-foreground">{new Date(log.time).toLocaleTimeString()}</TableCell>
                      <TableCell className="font-mono text-xs uppercase">{log.level}</TableCell>
                      <TableCell className="max-w-0 truncate">{log.message}</TableCell>
                    </TableRow>
                  ))}
                  {logs.length === 0 && (
                    <TableRow>
                      <TableCell colSpan={3} className="text-center text-muted-foreground">{t('serverConsole.noLogs')}</TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            </div>
          </section>
        </div>

        {/* 右栏 38%：三卡合流（FR-423）——最近事件 + 关注事项 + 崩溃诊断同一条时间线。 */}
        <section className="flex min-h-0 flex-1 flex-col rounded-lg border bg-card shadow-soft">
          <div className="flex flex-none items-center justify-between gap-2 border-b px-3 py-2">
            <h2 className="text-sm font-semibold">{t('serverConsole.activityFeed')}</h2>
            <span className="text-[11px] text-muted-foreground">{t('serverConsole.activityFeedHint')}</span>
          </div>
          {activityFeed}
        </section>
      </div>
    </div>
  )
}
