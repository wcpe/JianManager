import { Activity, Fragment, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Activity as ActivityIcon, AlertTriangle, Bot, ChevronDown, ChevronUp, Coins, Copy, DatabaseBackup, FileCog, FolderTree, Gauge, Hammer, HardDrive, HeartPulse, Layers, LayoutDashboard, Loader2, MoreHorizontal, Network, Play, Puzzle, RotateCw, Square, TerminalSquare, Users, type LucideIcon } from 'lucide-react'

import { useInstance, useKillInstance, useRebuildInstance, useRestartInstance, useStartInstance, useStopInstance, isProvisioningInstance } from '@/api/instances'
import { usePermissionsStore } from '@/stores/permissions'
import { runtimeDriftOf } from '@/lib/runtime-drift'
import DangerConfirm from '@/components/DangerConfirm'
import { useInstanceMetrics, useMetricSeries } from '@/api/metrics'
import { useLogs } from '@/api/logs'
import { useNodes } from '@/api/nodes'
import { useServerState } from '@/api/serverState'
import { Button } from '@jianmanager/ui/components/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@jianmanager/ui/components/dropdown-menu'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { cn, instanceStatusLevel } from '@jianmanager/ui'
import { copyToClipboard } from '@/lib/clipboard'
import { instanceStatusGlowClass } from '@/lib/instance-glow'
import { useInstanceCapabilities, hasCapability, type Capability } from '@/lib/capabilities'
import type { CardType } from '@/lib/workspace-card'
import InstanceActivityFeed from './InstanceActivityFeed'
import InstanceBackupSegment from './InstanceBackupSegment'
import InstancePlayersSegment from './InstancePlayersSegment'
import InstanceResourceSegment, { type ResourceSegment } from './InstanceResourceSegment'
import BcSegment from './BcSegment'
import BcPlayersPanel from './BcPlayersPanel'
import BinarySegment from './BinarySegment'
import BinaryVersionPanel from './BinaryVersionPanel'
import QuotaPanel from './QuotaPanel'
import SnapshotPanel from './SnapshotPanel'
import GenericConfigSegment from './GenericConfigSegment'
import { HealthPanel } from './HealthPanel'
import { RuntimeDriftBanner } from './RuntimeDriftNotice'
import { MetricSourceChips } from './MetricSourceChips'
import WorkspaceCardBody from './WorkspaceCardBody'
import { recordRecentServer } from './server-selection'

type TabKey = 'overview' | 'terminal' | 'resource' | 'metrics' | 'players' | 'plugins' | 'backup' | 'business' | 'bot' | 'bcTopology' | 'process' | 'health' | 'config'

const TAB_CARD_TYPE: Partial<Record<TabKey, CardType>> = {
  terminal: 'terminal',
  metrics: 'metrics',
  plugins: 'plugins',
  business: 'business',
  bot: 'bot',
}

// Tab 全量登记表（FR-445/448）：这是「有哪些 Tab、顺序如何、图标标签是什么」的唯一登记处；
// **实际渲染的可见集合由能力画像 `capabilities` 过滤**（见 useInstanceCapabilities），不再硬编码角色分支。
// Tab 重组（FR-413）：原「环境变量」并入「文件配置」的一个分段；分隔线按职能分组（运行/配置/观测/运营）。
const TAB_KEYS: TabKey[] = ['overview', 'terminal', 'resource', 'plugins', 'metrics', 'players', 'business', 'bot', 'backup', 'bcTopology', 'process', 'health', 'config']

/** 在该 key 之前插入分组分隔线。 */
const TAB_GROUP_BREAK: ReadonlySet<TabKey> = new Set<TabKey>(['resource', 'metrics', 'business', 'bcTopology', 'process'])

const TAB_LABEL_KEY: Record<TabKey, string> = {
  overview: 'serverConsole.overview',
  terminal: 'serverConsole.console',
  resource: 'serverConsole.filesConfig',
  metrics: 'serverConsole.metrics',
  players: 'serverConsole.players',
  plugins: 'serverConsole.plugins',
  backup: 'serverConsole.backupSchedule',
  business: 'serverConsole.business',
  bot: 'serverConsole.bot',
  bcTopology: 'serverConsole.bcTopology',
  process: 'serverConsole.process',
  health: 'serverConsole.health',
  config: 'serverConsole.config',
}

const TAB_ICON: Record<TabKey, LucideIcon> = {
  overview: LayoutDashboard,
  terminal: TerminalSquare,
  resource: FolderTree,
  metrics: ActivityIcon,
  players: Users,
  plugins: Puzzle,
  backup: DatabaseBackup,
  business: Coins,
  bot: Bot,
  bcTopology: Network,
  process: Gauge,
  health: HeartPulse,
  config: FileCog,
}

/**
 * 能力 → Tab 映射（FR-445）：画像 `capabilities` 中的每项**若对应一个 Tab**则落成 Tab。
 * 注意 `files` 能力对应 `resource` Tab（页签名「文件配置」），二者命名不同是历史包袱。
 * 动作级能力（如 `clone`：可克隆，用于行菜单显隐）刻意**不登记**，故为 Partial，
 * 会被 `visibleTabsFor` 过滤掉，不产生幽灵页签。
 */
const CAPABILITY_TAB: Partial<Record<Capability, TabKey>> = {
  overview: 'overview',
  terminal: 'terminal',
  files: 'resource',
  plugins: 'plugins',
  metrics: 'metrics',
  players: 'players',
  business: 'business',
  bot: 'bot',
  backup: 'backup',
  bcTopology: 'bcTopology',
  process: 'process',
  health: 'health',
  config: 'config',
}

/** 按画像能力集合推导有序可见 Tab（画像为空回退全量，保证非白屏）。 */
function visibleTabsFor(capabilities: Capability[]): TabKey[] {
  const tabs = capabilities.map((c) => CAPABILITY_TAB[c]).filter((k): k is TabKey => !!k)
  return tabs.length > 0 ? tabs : TAB_KEYS
}

interface InstanceConsolePageProps {
  instanceId: number
}

function readActiveTab(searchParams: URLSearchParams): TabKey {
  const tab = searchParams.get('tab')
  // 旧深链兼容（FR-413）：`?tab=env` 曾是独立页签，现落到「文件配置」的环境变量分段。
  if (tab === 'env') return 'resource'
  return TAB_KEYS.includes(tab as TabKey) ? (tab as TabKey) : 'overview'
}

function readResourceSegment(searchParams: URLSearchParams): ResourceSegment {
  // 旧深链兼容（FR-413）：`?tab=env` 落到「环境变量」分段；`?seg=config` 落到「关键配置」分段（FR-451）。
  if (searchParams.get('tab') === 'env') return 'env'
  const seg = searchParams.get('seg')
  return seg === 'env' || seg === 'config' ? seg : 'files'
}

/**
 * 服务器统一控制台（FR-269）：固定分区的单服默认入口。
 * 页签 keep-alive（FR-295，ADR-067）：访问过的页签进入 mountedTabs 全部渲染，
 * 非活跃者包 `<Activity mode="hidden">`——DOM 与本地状态保留、effects 卸载
 * （TanStack Query 订阅随之暂停 → 隐藏页签自动停轮询），切回瞬时呈现。
 */
export default function InstanceConsolePage({ instanceId }: InstanceConsolePageProps) {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()
  const resourceSegment = readResourceSegment(searchParams)
  const { data: instance } = useInstance(instanceId)
  // 能力画像（FR-445）：Tab 显隐与顺序的唯一门控来源——后端下发的 capabilities 优先，
  // 缺失时按 (type, role) 本地兜底（离线/mock 兼容）。不再有零散 role === '...' 分支。
  const profile = useInstanceCapabilities(instance)
  const visibleTabs = useMemo(
    () => (instance ? visibleTabsFor(profile.capabilities) : TAB_KEYS),
    [instance, profile],
  )
  const activeTabFromUrl = readActiveTab(searchParams)
  // 深链落在隐藏 Tab（FR-448）：回退 overview，不白屏。
  const activeTab: TabKey = visibleTabs.includes(activeTabFromUrl) ? activeTabFromUrl : 'overview'
  // 访问过即保活：渲染期把新激活页签并入集合（React 官方「渲染期间调整状态」模式）。
  const [mountedTabs, setMountedTabs] = useState<TabKey[]>([activeTab])
  if (!mountedTabs.includes(activeTab)) {
    setMountedTabs((prev) => (prev.includes(activeTab) ? prev : [...prev, activeTab]))
  }
  const { data: nodes = [] } = useNodes({ refetchInterval: 30_000 })
  const { data: metrics } = useInstanceMetrics(instanceId, true)
  const { data: serverState } = useServerState(instanceId, true, 15_000)
  const { data: logs } = useLogs({ source: 'instance', instanceId, page: 1, pageSize: 8 }, { refetchInterval: 10_000 })
  const restart = useRestartInstance()
  const stop = useStopInstance()
  const start = useStartInstance()
  const kill = useKillInstance()
  const rebuild = useRebuildInstance()
  // FR-432 首批写门禁：无 instance.operate 时主操作按钮禁用（平台管理员 hasPerm 恒 true）。
  const canOperate = usePermissionsStore((s) => s.hasPerm('instance.operate'))
  // 强杀走统一危险操作确认（FR-059），不直发请求。
  const [killConfirmOpen, setKillConfirmOpen] = useState(false)
  // 指标条折叠偏好（FR-412）：跨实例与刷新保留，收起后顶栏再省一行给内容区。
  const [metricsBarOpen, setMetricsBarOpen] = useState(readMetricsBarPref)
  useEffect(() => {
    try { localStorage.setItem(METRICS_BAR_KEY, metricsBarOpen ? '1' : '0') } catch { /* 隐私模式忽略 */ }
  }, [metricsBarOpen])

  // FR-293：直接经路由/深链进入实例也计入「最近打开」（与选择器/侧栏常驻列同一存储）；
  // store 侧对内容未变的写入不广播，轮询刷新不会造成订阅方空转。
  useEffect(() => {
    if (instance) recordRecentServer(instance)
  }, [instance])

  const node = nodes.find((n) => n.id === instance?.nodeId)
  const serverStatePlayers = serverState?.state?.server?.onlinePlayers
  const serverStateMax = serverState?.state?.server?.maxPlayers
  const playersAvailable = serverStatePlayers != null || (metrics?.playersAvailable ?? false)
  const online = serverStatePlayers ?? (metrics?.playersAvailable ? metrics!.onlinePlayers : 0)
  const maxPlayers = serverStateMax ?? (metrics?.maxPlayersAvailable ? metrics!.maxPlayers : 0)
  const maxPlayersAvailable = serverStateMax != null || (metrics?.maxPlayersAvailable ?? false)
  const richMetricsAvailable = metrics?.probeAvailable ?? false
  // 探针缺失芯片只在 MC 世界语义实例出现——非 MC 实例本不该有 ServerProbe，提示只会误导。
  const showProbeChip = profile.mcSemantics && !richMetricsAvailable
  // 关注事项走 i18n（修硬编码中文）：告警文案直接进英文界面是验收硬伤。
  const watchItems = useMemo(
    () => buildWatchItems({ status: instance?.status, metrics, probeConnected: serverState?.connected, mcSemantics: profile.mcSemantics, t }),
    [instance?.status, metrics, serverState?.connected, profile.mcSemantics, t],
  )

  // ---- 以下 hooks 必须全部位于 `if (!instance)` 早退之前（hooks 顺序不变量）----
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
    setActiveTab(next)
  }
  const activateEdgeTab = (edge: 'first' | 'last') => {
    const key = edge === 'first' ? visibleTabs[0] : visibleTabs[visibleTabs.length - 1]
    tabRefs.current.get(key)?.focus()
    setActiveTab(key)
  }

  if (!instance) {
    return <div className="rounded-lg border bg-card p-6 text-sm text-muted-foreground shadow-soft">{t('serverConsole.noInstance')}</div>
  }

  const canStart = instance.status === 'STOPPED' || instance.status === 'CRASHED'
  const canControl = instance.status === 'RUNNING' || instance.status === 'STARTING' || instance.status === 'STOPPING'
  // 搭建中硬性禁启（FR-331）：provision 未终态期间启动按钮禁用 + tooltip 引导看任务中心，
  // 与后端启动闸（FR-319 二轮②）同一信号源（statusReason「搭建中」），任务终态自然解禁。
  const provisioning = isProvisioningInstance(instance)
  const isDamaged = instance.status === 'DAMAGED'
  // 重建在途（FR-342）：损毁实例重建期间 statusReason 标「重建中…」，据此禁用重建按钮、且不落红色失败横幅。
  const rebuilding = isDamaged && (instance.statusReason?.startsWith('重建中') ?? false)
  // 失败原因横幅（FR-312）：只看 statusReason 非空、不看 status——Worker 心跳会把 CRASHED
  // 冲回 STOPPED，若以状态为前置条件横幅会随之消失；再次启动时 CP transition 清空 reason，
  // 横幅纯受查询数据驱动消失，不留本地状态。
  // 搭建中的 statusReason 是进行时状态而非失败（FR-331）：不落红色失败横幅，走下方琥珀状态横幅。
  const startFailReason = provisioning || rebuilding ? undefined : instance.statusReason?.trim()
  // 运行态漂移（FR-471）：>0 即存在未纳管活进程；无漂移时为 undefined（不渲染任何标记）。
  const runtimeDrift = runtimeDriftOf(instance)
  // <md 主操作（可用性增强）：按状态给唯一带文字的主按钮，其余收进「更多」菜单。
  const primaryAction = canStart
    ? { label: t('instances.start'), icon: Play, disabled: provisioning || !canOperate, title: !canOperate ? t('permissions.operateDenied') : provisioning ? t('instances.provisioningBlocked') : undefined, onClick: () => start.mutate(instance.id) }
    : isDamaged
      ? { label: t('serverConsole.rebuild'), icon: Hammer, disabled: rebuilding || !canOperate, title: !canOperate ? t('permissions.operateDenied') : rebuilding ? t('serverConsole.rebuilding') : undefined, onClick: () => rebuild.mutate(instance.id) }
      : canControl
        ? { label: t('serverConsole.restart'), icon: RotateCw, disabled: !canOperate, title: !canOperate ? t('permissions.operateDenied') : undefined, onClick: () => restart.mutate(instance.id) }
        : null
  const setActiveTab = (tab: TabKey) => {
    const next = new URLSearchParams(searchParams)
    if (tab === 'overview') next.delete('tab')
    else next.set('tab', tab)
    // 离开文件配置就清掉分段参数，避免 URL 残留无意义的 seg。
    if (tab !== 'resource') next.delete('seg')
    setSearchParams(next)
  }
  const setResourceSegment = (segment: ResourceSegment) => {
    const next = new URLSearchParams(searchParams)
    next.set('tab', 'resource')
    if (segment === 'files') next.delete('seg')
    else next.set('seg', segment)
    setSearchParams(next)
  }

  return (
    // 视口自适应骨架（FR-422）：根与内层都是 flex 列，横幅/顶栏/Tab 栏 flex-none、
    // Tab 内容区 flex-1 min-h-0——滚动收口到页内卡片，顶栏常驻可见、底部不留白。
    <div data-page="instance-console" className="jm-page-stack flex min-h-0 flex-1 flex-col text-[13px] text-foreground">
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
                原先只能拖选。复制走 copyToClipboard（含 HTTP 非安全上下文兜底）并给回执。 */}
            <button
              type="button"
              onClick={() => {
                void copyToClipboard(`${t('serverConsole.lastStartFailed')}: ${startFailReason}`).then((ok) => {
                  if (ok) toast.success(t('common.copied'))
                  else toast.error(t('common.copyFailed'))
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
              <Link to="/tasks" className="mt-0.5 inline-block font-medium underline underline-offset-2">
                {t('instances.provisioningGoTasks')}
              </Link>
            </div>
          </div>
        )}
        {/* 运行态漂移告警（FR-471）：工作目录下存在未纳管的活进程——面板可能显示已停止而磁盘在跑，
            直接「启动」会双开（后端预检拦，但用户要先看到才有机会接管）。漂移字段由心跳写入，
            接管成功后后端清零 → 本条随查询刷新自动消失，不留本地状态。 */}
        {runtimeDrift && (
          <RuntimeDriftBanner
            instanceId={instance.id}
            instanceName={instance.name}
            pid={runtimeDrift.pid}
            cmdline={runtimeDrift.cmdline}
            canOperate={canOperate}
          />
        )}
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
                  <Button size="sm" disabled={provisioning || !canOperate} data-testid="instance-operate-start" onClick={() => start.mutate(instance.id)}>
                    <Play className="size-3.5" />
                    {t('instances.start')}
                  </Button>
                </span>
              )}
              {isDamaged && (
                // 损毁实例（FR-342）：显「重建」复用参数重跑搭建；重建在途禁用（tooltip 提示）。
                <span title={!canOperate ? t('permissions.operateDenied') : rebuilding ? t('serverConsole.rebuilding') : undefined}>
                  <Button size="sm" disabled={rebuilding || !canOperate} data-testid="instance-operate-rebuild" onClick={() => rebuild.mutate(instance.id)}>
                    <Hammer className="size-3.5" />
                    {t('serverConsole.rebuild')}
                  </Button>
                </span>
              )}
              <Button size="sm" variant="outline" disabled={!canControl || !canOperate} data-testid="instance-operate-restart" title={!canOperate ? t('permissions.operateDenied') : undefined} onClick={() => restart.mutate(instance.id)}>
                <RotateCw className="size-3.5" />
                {t('serverConsole.restart')}
              </Button>
              <Button size="sm" variant="outline" disabled={!canControl || !canOperate} data-testid="instance-operate-stop" title={!canOperate ? t('permissions.operateDenied') : undefined} onClick={() => stop.mutate(instance.id)}>
                <Square className="size-3.5" />
                {t('serverConsole.stop')}
              </Button>
              <Button size="sm" variant="destructive" disabled={!canControl || !canOperate} data-testid="instance-operate-kill" title={!canOperate ? t('permissions.operateDenied') : undefined} onClick={() => setKillConfirmOpen(true)}>
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
                  <DropdownMenuItem disabled={!canControl} onClick={() => restart.mutate(instance.id)}>
                    <RotateCw className="size-3.5" />
                    {t('serverConsole.restart')}
                  </DropdownMenuItem>
                  <DropdownMenuItem disabled={!canControl} onClick={() => stop.mutate(instance.id)}>
                    <Square className="size-3.5" />
                    {t('serverConsole.stop')}
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    disabled={!canControl}
                    className="text-status-danger focus:text-status-danger"
                    onClick={() => setKillConfirmOpen(true)}
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
              {profile.mcSemantics && (richMetricsAvailable ? (
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
                value={playersAvailable ? `${online}/${maxPlayersAvailable ? maxPlayers : '—'}` : t('metrics.unavailable')}
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
                  onClick={() => setActiveTab(key)}
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
            {tab === 'overview' ? (
              /* 概览（FR-445）：动态与告警流 + 指标条。FR-467/468 起把「该实例当前的可操作面」
                 两个面板并入概览——配额（限额来源 + 实时用量 + 强制状态）与二进制版本
                 （受控升级 / 一级回滚）。选概览而非「进程」页签：overview 是所有画像（含未知
                 回退）都存在的唯一 Tab，而 process 只在部分画像下出现——放 process 会让
                 backend 画像（唯一没有 process 的画像）看不到这两个能力。 */
              <div className="space-y-3">
                <QuotaPanel instanceId={instance.id} />
                <BinaryVersionPanel instanceId={instance.id} />
                <OverviewPanel
                  instanceId={instance.id}
                  instanceUuid={instance.uuid}
                  metrics={metrics}
                  online={online}
                  maxPlayers={maxPlayers}
                  playersAvailable={playersAvailable}
                  maxPlayersAvailable={maxPlayersAvailable}
                  nodeDiskUsage={node?.diskUsage}
                  logs={logs?.items ?? []}
                  watchItems={watchItems}
                  probeConnected={serverState?.connected ?? false}
                  uptimeSeconds={metrics?.uptimeSeconds}
                  mcSemantics={profile.mcSemantics}
                />
              </div>
            ) : tab === 'resource' ? (
              /* 文件配置（FR-413）：文件管理器 + 环境变量（FR-344）两分段，均保活。 */
              <InstanceResourceSegment
                instanceId={instance.id}
                segment={resourceSegment}
                onSegmentChange={setResourceSegment}
              />
            ) : tab === 'players' ? (
              /* 玩家分区（FR-445 §2.3 按角色语义）：backend = 本实例单服实名名单（FR-339）；
                 proxy 语义（画像含 bcTopology）= 跨服玩家分布（FR-449 §2.2.2）。
                 Tab 显隐仍只由 capabilities 决定，此处仅决定同一 Tab 内的呈现形态。 */
              hasCapability(profile, 'bcTopology') ? (
                <BcPlayersPanel instanceId={instance.id} />
              ) : (
                <InstancePlayersSegment instanceId={instance.id} />
              )
            ) : tab === 'backup' ? (
              /* 备份·定时分区接真（FR-339）：本实例定时任务启停/删 + 备份创建/恢复/删除。
                 FR-466 起追加「整机快照」面板：快照底层复用同一套归档通道（全量备份 +
                 回放），与备份同页签是单一归属——两者放一起运维才看得到「归档 vs 时间点」
                 的分工，也不会在 backend/proxy/generic 三种画像里各缺一处。 */
              <div className="space-y-3">
                <InstanceBackupSegment instanceId={instance.id} />
                <SnapshotPanel instanceId={instance.id} />
              </div>
            ) : tab === 'bcTopology' ? (
              /* BC 子服拓扑（FR-449）：子服列表 + 各自状态，由 bcTopology 能力驱动。
                 跨服玩家/进程指标/config 分别归 players/process/config 页签（单一归属）。 */
              <BcSegment instanceId={instance.id} />
            ) : tab === 'process' ? (
              /* 进程视图（FR-450）：进程指标 + 启动参数。端口健康归 health 页签。 */
              <BinarySegment instanceId={instance.id} />
            ) : tab === 'health' ? (
              /* 端口 + 主动健康检查（FR-450）。 */
              <HealthPanel instanceId={instance.id} />
            ) : tab === 'config' ? (
              /* 结构化配置编辑（FR-451 衔接）：BC config.yml / 原生二进制配置。 */
              <GenericConfigSegment instanceId={instance.id} />
            ) : TAB_CARD_TYPE[tab] ? (
              // 去掉原 min-h-[520px]（FR-422）：卡片吃满剩余高度，内部自行滚动。
              <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border bg-card shadow-soft">
                {/* persistTerminal：终端连接由管理器常驻，页签隐藏/切换不断 WS（FR-295）。 */}
                <WorkspaceCardBody instanceId={instance.id} type={TAB_CARD_TYPE[tab]!} persistTerminal />
              </div>
            ) : null}
            </div>
          </Activity>
        ))}
      </div>

      {/* 强杀二次确认（FR-059）：与实例列表页同款 DangerConfirm，组管理员及以上可确认。 */}
      <DangerConfirm
        open={killConfirmOpen}
        title={t('danger.killInstanceTitle', { name: instance.name })}
        description={t('danger.killInstanceDesc')}
        confirmLabel={t('instances.kill')}
        scope="group"
        onConfirm={() => { kill.mutate(instance.id); setKillConfirmOpen(false) }}
        onCancel={() => setKillConfirmOpen(false)}
      />
    </div>
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

/** 顶栏指标分段（方案 B 状态条）：label + 等宽值成一段，阈值越线才上色；dot 为状态圆点。 */
function MetricSegment({ label, value, tone, dot }: { label: string; value: string; tone?: 'warn' | 'danger'; dot?: boolean }) {
  return (
    // title 补全精确值与口径：分段只显「12%」，悬停能看到「CPU 12%」上下文。
    <span className="inline-flex items-center gap-1.5" title={`${label} ${value}`}>
      {dot && (
        <span
          aria-hidden
          className={cn(
            'size-1.5 rounded-full',
            tone === 'warn' ? 'bg-status-warning' : tone === 'danger' ? 'bg-status-danger' : 'bg-status-success',
          )}
        />
      )}
      <span className="text-[11px] text-muted-foreground">{label}</span>
      <b className={cn(
        'font-mono text-xs font-semibold tabular-nums',
        tone === 'warn' && 'text-status-warning',
        tone === 'danger' && 'text-status-danger',
      )}>{value}</b>
    </span>
  )
}

/** 分段之间的细分隔线。 */
function MetricDivider() {
  return <span aria-hidden className="h-3 w-px shrink-0 bg-border" />
}

/** 探针缺失聚合芯片（方案 B）：取代逐项「需探针」，悬停列出不可用项。对齐交由外层容器管理。 */
function ProbeMissingChip() {
  const { t } = useTranslation()
  return (
    <span
      title={t('serverConsole.probeUnavailable')}
      className="inline-flex items-center gap-1.5 rounded-md bg-muted px-2 py-0.5 text-[11px] text-muted-foreground"
    >
      <span aria-hidden className="size-1.5 rounded-full bg-muted-foreground/60" />
      {t('serverConsole.probeChip')}
    </span>
  )
}

function OverviewPanel({
  instanceId,
  instanceUuid,
  metrics,
  online,
  maxPlayers,
  playersAvailable,
  maxPlayersAvailable,
  nodeDiskUsage,
  logs,
  watchItems,
  probeConnected,
  uptimeSeconds,
  mcSemantics,
}: {
  instanceId: number
  instanceUuid: string
  metrics?: { tps: number; msptMillis: number; memoryMb: number; heapMaxMb: number; cpuPercent: number; onlinePlayers: number; probeAvailable: boolean }
  online: number
  maxPlayers: number
  /** 在线数是否可用（探针 server-state 或直探 SLP/Query 任一命中）。 */
  playersAvailable: boolean
  /** 最大人数是否可用（同上，缺测不得以 0 冒充）。 */
  maxPlayersAvailable: boolean
  nodeDiskUsage?: number
  logs: Array<{ id: number; level: string; message: string; time: string }>
  watchItems: string[]
  probeConnected: boolean
  uptimeSeconds?: number
  /** 是否具备 MC 世界语义（FR-448）：只决定本 Tab 内字段取舍（TPS/世界 vs 连接/跨服），不作 Tab 级门控。 */
  mcSemantics: boolean
}) {
  const { t } = useTranslation()
  const hasHeapMax = (metrics?.heapMaxMb ?? 0) > 0
  const memoryPct = hasHeapMax ? Math.min(100, Math.round((metrics!.memoryMb / metrics!.heapMaxMb) * 100)) : 0
  const cpuPct = Math.round(metrics?.cpuPercent ?? 0)
  const diskPct = Math.round(nodeDiskUsage ?? 0)
  const alertCount = watchItems.length
  const hasProbe = metrics?.probeAvailable ?? false
  // FR-343 去 mock-api：实例 TPS 真实时序火花线（取末段点位），无数据/无探针显空态而非假图。
  // 非 MC 世界语义（proxy/generic）不适用 TPS，不拉序列（省一次请求且避免空图）。
  const { data: seriesData } = useMetricSeries({ scope: 'instance', targetId: instanceUuid, range: '1h', metrics: ['inst_tps'], enabled: !!instanceUuid && mcSemantics })
  const tpsPoints = (seriesData?.series.find((s) => s.metricKey === 'inst_tps' && s.world === '')?.points ?? [])
    .filter((p) => p.avg != null)
    .map((p) => p.avg as number)
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
              <Link
                to={`/logs?instanceId=${instanceId}`}
                className="shrink-0 text-[11px] text-muted-foreground underline-offset-2 transition-colors hover:text-primary hover:underline"
              >
                {t('serverConsole.viewAllLogs')} →
              </Link>
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
          <InstanceActivityFeed
            instanceId={instanceId}
            logs={logs}
            watchItems={watchItems}
            uptimeSeconds={uptimeSeconds}
          />
        </section>
      </div>
    </div>
  )
}

function KpiCard({ icon: Icon, label, value, sub, progress, danger }: { icon: LucideIcon; label: string; value: string; sub?: string; progress: number; danger?: boolean }) {
  return (
    <div className="rounded-lg border bg-card p-2 shadow-soft">
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="text-[11px] text-muted-foreground">{label}</p>
          <p className={cn('mt-0.5 font-mono text-lg font-semibold tabular-nums', danger && 'text-status-danger')}>{value}</p>
          {sub && <p className="truncate text-[10px] text-muted-foreground">{sub}</p>}
        </div>
        <Icon className={cn('size-4 shrink-0', danger ? 'text-status-danger' : 'text-primary')} />
      </div>
      <div className="mt-2 h-1.5 overflow-hidden rounded-sm bg-muted">
        <div className={cn('h-full rounded-sm', danger ? 'bg-status-danger' : progress > 80 ? 'bg-status-warning' : 'bg-primary')} style={{ width: `${Math.max(4, Math.min(100, progress))}%` }} />
      </div>
    </div>
  )
}

function formatNumber(value: number | undefined, digits: number) {
  if (value == null || Number.isNaN(value)) return '—'
  return value.toFixed(digits)
}

/** 运行时长（秒）人性化：Xd Yh / Xh Ym / Xm / Xs；无值显 —。 */
function formatUptime(sec: number | undefined): string {
  if (!sec || sec <= 0) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m`
  return `${Math.floor(sec)}s`
}

/** 关注事项文案的翻译签名（够用即可，不引 i18next 全量类型）。 */
type Translate = (key: string, opts?: Record<string, unknown>) => string

function buildWatchItems({
  status,
  metrics,
  probeConnected,
  mcSemantics,
  t,
}: {
  status?: string
  metrics?: { tps: number; msptMillis: number; cpuPercent: number; probeAvailable: boolean }
  probeConnected?: boolean
  /** 是否 MC 世界语义（FR-448）：TPS/MSPT 与探针相关告警只对世界语义实例成立。 */
  mcSemantics: boolean
  t: Translate
}) {
  const items: string[] = []
  // 走 i18n（修硬编码中文）：这些文案会出现在英文界面的「动态与告警」时间线里。
  if (status === 'CRASHED') items.push(t('serverConsole.watch.crashed'))
  if (status === 'STARTING' || status === 'STOPPING') items.push(t('serverConsole.watch.transition'))
  // TPS/MSPT 与探针在线是世界语义专属（FR-448）：proxy/二进制/beacon 不该出现这些 MC 告警。
  if (mcSemantics && metrics?.probeAvailable && metrics.tps < 18) items.push(t('serverConsole.watch.tpsLow'))
  if (mcSemantics && metrics?.probeAvailable && metrics.msptMillis > 50) items.push(t('serverConsole.watch.msptHigh'))
  if (metrics?.cpuPercent != null && metrics.cpuPercent > 85) items.push(t('serverConsole.watch.cpuHigh'))
  if (mcSemantics && !probeConnected) items.push(t('serverConsole.watch.probeOffline'))
  return items
}
