import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { AlertTriangle, Download, Info, MoreHorizontal, Radio } from 'lucide-react'
import { useLegacyLogs, useLogs, exportLogs, type LogQueryParams } from '@/api/logs'
import {
	buildFederationResult,
	exportFederatedLogs,
	useLogsFederation,
	useLogsFederationFacets,
	useLogsFederationStats,
	type LogsFederationResult,
} from '@/api/logFederation'
import { useAuthStore } from '@/stores/auth'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { cn } from '@jianmanager/ui'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { LOGS_FEDERATION_KEYS, type CoverageBannerProps } from '@/lib/logs-federation'
import type { LogEntry } from '@/api/logs'
import {
  logLevelStatus,
  timeRangeToParams,
  buildExportParams,
  computeVirtualWindow,
  TIME_RANGE_PRESETS,
  LOG_VIEWS,
  type TimeRangePreset,
  type LogExportScope,
} from './logs-filters'

/** 联邦事件 → LogEntry 行（F-003：列表主数据源）。 */
function mapFederationItems(items: unknown[] | null | undefined): LogEntry[] {
  if (!Array.isArray(items)) return []
  return items.map((raw, index) => {
    const o = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>
    const message = typeof o.message === 'string' ? o.message : typeof o._msg === 'string' ? o._msg : ''
    const level = typeof o.level === 'string' ? o.level.toLowerCase() : 'info'
    const time = typeof o.event_time_utc === 'string'
      ? o.event_time_utc
      : typeof o.eventTimeUTC === 'string'
        ? o.eventTimeUTC
        : typeof o.time === 'string'
          ? o.time
          : ''
    const instRaw = o.instance_id ?? o.instanceId
    const nodeRaw = o.node_id ?? o.nodeId
    return {
      id: typeof o.id === 'number' ? o.id : -(index + 1),
      source: typeof o.source === 'string' ? o.source : typeof o.log_source_id === 'string' ? o.log_source_id : 'instance',
      level,
      instanceId: typeof instRaw === 'number' ? instRaw : 0,
      instanceUuid: typeof o.instance_uuid === 'string' ? o.instance_uuid : '',
      nodeId: typeof nodeRaw === 'number' ? nodeRaw : 0,
      stream: typeof o.stream === 'string' ? o.stream : undefined,
      message,
      time,
    } satisfies LogEntry
  })
}

// Radix Select 不允许空字符串值，用哨兵代表「全部」。
const SENTINEL_ALL = '__all__'
const PAGE_SIZE = 100
const SOURCES = ['instance', 'control_plane', 'worker']
const ROLE_PLATFORM_ADMIN = 10
const LEVELS = ['error', 'warn', 'info', 'debug']
const EXPORT_SCOPES: LogExportScope[] = ['currentPage', 'allMatched', 'range']

// 虚拟滚动：固定行高 + 视口上下各预渲染 8 行。
const ROW_HEIGHT = 30
const OVERSCAN = 8
const VIEWPORT_HEIGHT = 460
// 实时跟随轮询间隔（ms）。
const FOLLOW_INTERVAL = 3000

/**
 * 日志中心（FR-049 查看 / FR-050 检索 / FR-150 增强 + FR-481 覆盖/失败态接线）。
 * 套「流水检索」范式：强筛选（级别 pill + 来源/节点/实例/时间范围 + 关键字）→ 时间线行（虚拟滚动）；
 * 「实时跟随」开关锁定首页并按间隔轮询（tail）；导出可选范围（当前页/全部匹配/时间段）。
 * 级别用 StatusBadge 着色，token 驱动，与告警页语义统一。
 *
 * FR-481 接线（最小改动）：
 * - 可选联邦 API（`/logs/federation`）经 `useLogsFederation` 探测；404 → 降级 legacy 视图 + 横幅。
 * - 覆盖/失败态一律经 `classifyLogViewState` + `toCoverageBannerProps`，失败/partial 禁止空成功。
 * - `sourceTag=legacy` 打 Legacy 徽标；ErrFederatedNotReady 显示联邦未就绪说明。
 * - 导出在 `resolveExportDownloadAffordance` 阻断时禁用；404 降级保留经典 `/logs/export`。
 */
export default function LogsPage() {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()
  const { data: instances } = useInstances()
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const isPlatformAdmin = useAuthStore((state) => state.role === ROLE_PLATFORM_ADMIN)

  const [view, setView] = useState<(typeof LOG_VIEWS)[number]>('node_instance')
  const [source, setSource] = useState('')
  const [level, setLevel] = useState('')
  const [nodeId, setNodeId] = useState<number | null>(null)
  // 深链预筛选（FR-345）：从 URL ?instanceId= 初始化实例筛选（如从终端「查看完整历史」进入）。
  const [instanceId, setInstanceId] = useState<number | null>(() => {
    const q = searchParams.get('instanceId')
    const n = q ? Number(q) : NaN
    return Number.isFinite(n) && n > 0 ? n : null
  })
  const [keyword, setKeyword] = useState('')
  const [range, setRange] = useState<TimeRangePreset>('all')
  const [page, setPage] = useState(1)
	const [federationCursors, setFederationCursors] = useState<Record<number, string>>({ 1: '' })
  // 「仅查在线节点」：显式开启后联邦扇出收缩到在线目标（FR-479 online_only）。
  const [onlineOnly, setOnlineOnly] = useState(false)
  const [follow, setFollow] = useState(false)
  const [exporting, setExporting] = useState(false)
  /**
   * 时间窗锚点：非跟随态下冻结，避免每次渲染 `new Date()` 使 useLogs /
   * useLogsFederation 的 queryKey 每毫秒变化导致请求风暴（FR-481 接线后暴露）。
   * 跟随态在渲染时取当前时间，配合 refetchInterval 滚动时间窗。
   */
  const [rangeAnchor, setRangeAnchor] = useState(() => Date.now())
  useEffect(() => {
    // 时间范围/跟随态变化时重置锚点：读取挂钟的「同步外部时间」语义，非渲染期纯计算。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setRangeAnchor(Date.now())
  }, [range, follow])

  // 跟随态钉在第 1 页（最新）；锚点 now 在每次构建参数时取，配合轮询滚动时间窗。
  const timeParams = timeRangeToParams(
    range,
    follow ? new Date() : new Date(rangeAnchor),
  )
  const params: LogQueryParams = {
    view,
    page: follow ? 1 : page,
    pageSize: PAGE_SIZE,
    ...(view === 'all' && source ? { source } : {}),
    ...(level ? { level } : {}),
    ...(nodeId !== null ? { nodeId } : {}),
    ...(instanceId !== null ? { instanceId } : {}),
    ...(keyword.trim() ? { keyword: keyword.trim() } : {}),
    ...timeParams,
  }

  const legacyMode = view === 'legacy'
  const { data, isLoading: classicLoading, isError: classicError, refetch: refetchLogs } = useLogs(params, {
    refetchInterval: follow ? FOLLOW_INTERVAL : false,
    enabled: !legacyMode,
  })
	const legacyQuery = useLegacyLogs(params, legacyMode)

  // FR-481：可选联邦覆盖探测。与 useLogs 同筛选参数；API 404 → degraded legacy。
	const federationParams: Record<string, unknown> = {
		...params,
		limit: PAGE_SIZE,
		...(follow ? { tailMode: 'FOLLOW_LIVE' } : {}),
		...(onlineOnly ? { onlineOnly: true } : {}),
		...(!follow && page > 1 && federationCursors[page] ? { cursor: federationCursors[page] } : {}),
	}
  const federationQuery = useLogsFederation(federationParams, {
		enabled: !legacyMode,
		refetchInterval: follow ? FOLLOW_INTERVAL : false,
	})
  const federation = legacyMode && legacyQuery.data
    ? buildFederationResult({ sourceTag: 'legacy', items: legacyQuery.data.items, coverage: legacyQuery.data.coverage })
    : federationQuery.data
  // F-003：联邦可用（非降级）时列表以 federation.items 为主数据源；
  // 404 降级才回退 useLogs 的 Legacy 表。禁止「联邦横幅 + Legacy 内容」错配。
  const federationActive = !!federation && !federation.degraded && federation.available
  // 供标题区状态提示使用（与横幅同源，避免两处判定漂移）。
  const federationDegraded = federation?.degraded === true
  const federationNotReady = federation?.federatedNotReady === true
	const aggregateParams: Record<string, unknown> = {
		...timeParams,
		...(view === 'all' && source ? { source } : {}),
		...(level ? { level } : {}),
		...(nodeId !== null ? { nodeId } : {}),
		...(instanceId !== null ? { instanceId } : {}),
		...(keyword.trim() ? { keyword: keyword.trim() } : {}),
		...(federation?.response.viewId ? { viewId: federation.response.viewId } : {}),
	}
	const federationStats = useLogsFederationStats(
		{ ...aggregateParams, groupBy: 'level' },
		federationActive && !follow && !!federation?.response.viewId,
	)
	const federationFacets = useLogsFederationFacets(
		{ ...aggregateParams, dimensions: 'level,stream', dimensionLimit: 8 },
		federationActive && !follow && !!federation?.response.viewId,
	)
	// Stats 计数来自后端 `agg.count`（wire 真源）；兼容扁平 `count`。
	const statsCount = federationStats.data?.points?.reduce(
		(sum, point) => sum + Number(point.agg?.count ?? point.count ?? 0),
		0,
	)
	const levelFacets = federationFacets.data?.dimensions?.find((dimension) => dimension.dimension === 'level')?.values ?? []
  const federationItems = federationActive
    ? mapFederationItems(federation.response.items)
    : null

  const items = legacyMode ? (legacyQuery.data?.items ?? []) : federationActive ? (federationItems ?? []) : (data?.items ?? [])
  const total = legacyMode ? (legacyQuery.data?.total ?? 0) : federationActive ? items.length : (data?.total ?? 0)
	const federationHasNext = federationActive && !!federation.response.nextCursor
	const totalPages = federationActive
		? Math.max(1, page + (federationHasNext ? 1 : 0))
		: Math.max(1, Math.ceil(total / PAGE_SIZE))

  // 改任一筛选都回到第 1 页，避免停留在越界页。
  const resetTo = useCallback(
    <T,>(setter: (v: T) => void) =>
      (v: T) => {
        setter(v)
        setPage(1)
		setFederationCursors({ 1: '' })
      },
    [],
  )

  // 导出门禁：联邦可用且覆盖/校验未过 → 禁用；404 降级保留经典导出路径。
  const classicExportAllowed = !federation || federation.degraded
  const exportBlockedByFederation =
    !!federation && !classicExportAllowed && !federation.exportAffordance.showDownload
  const exportDisabled = legacyMode || exporting || total === 0 || exportBlockedByFederation
	// federated 探测（FR-481）未落地前，items 可能暂时为空——此时不得判为「空结果」，
	// 否则会先闪 empty-state（且 404 降级期间显示非成功空态），把尚未就绪误报成失败。
	// 故把「联邦探测未完成」并入 loading：只有探测结束、数据源确定后才允许判空。
	const federationProbePending = !legacyMode && federationQuery.isLoading
	const isLoading = legacyMode
		? legacyQuery.isLoading
		: classicLoading || federationProbePending
	const isError = legacyMode ? legacyQuery.isError : classicError
	const hasData = legacyMode ? !!legacyQuery.data : !!data

  // 空结果语义：失败/partial 不得呈「暂无日志」空成功（FR-481）。
  const emptySuccessAllowed =
    !federation || federation.degraded || federation.classified.emptySuccessAllowed

  const handleExport = async (scope: LogExportScope) => {
    if (exportDisabled) return
    setExporting(true)
    try {
		if (federationActive) {
			await exportFederatedLogs({
				...buildExportParams(params, scope),
				viewId: federation.response.viewId,
			})
		} else {
			await exportLogs(buildExportParams(params, scope))
		}
      toast.success(t('logs.exportStarted'))
    } catch {
      toast.error(t('logs.exportFailed'))
    } finally {
      setExporting(false)
    }
  }

  /**
   * 失败态可操作引导（FR-481 §3.1）：把 helper 给出的 actionKey 落成真实动作。
   * openNodes/startRehydrate → 节点页（受管运行时/归档管理入口）；reopenView → 重开固定视图；
   * requestOnline → 显式「仅查在线节点」；contactAdmin → 提示联系管理员。
   */
  const handleFederationAction = useCallback(
    (key: string) => {
      switch (key) {
        case LOGS_FEDERATION_KEYS.actionOpenNodes:
        case LOGS_FEDERATION_KEYS.actionStartRehydrate:
          navigate('/nodes')
          break
        case LOGS_FEDERATION_KEYS.actionReopenView:
          setFederationCursors({ 1: '' })
          setPage(1)
          void federationQuery.refetch()
          break
        case LOGS_FEDERATION_KEYS.actionRequestOnline:
          setOnlineOnly(true)
          setPage(1)
          setFederationCursors({ 1: '' })
          break
        case LOGS_FEDERATION_KEYS.actionContactAdmin:
          toast.info(t('logsFederation.action.contactAdmin'))
          break
        default:
          void federationQuery.refetch()
      }
    },
    [navigate, federationQuery, t],
  )

  return (
    <div data-page="logs" className="jm-page-stack flex h-full min-h-0 flex-col gap-4">
      <div className="jm-page-header">
        <div className="flex min-w-0 items-center gap-2">
          <h1 className="jm-page-title">{t('logs.title')}</h1>
          {/* 状态与提示固定在标题区：不占用列表空间，也不因状态变化推动下方布局。 */}
          <span data-testid="logs-source-tag" className="inline-flex shrink-0">
            {federation?.sourceTag && (
              <StatusBadge
                level={federation.sourceTag === 'legacy' ? 'warning' : 'info'}
                label={t(`logsFederation.sourceTag.${federation.sourceTag}`)}
              />
            )}
          </span>
          {(federationDegraded || federationNotReady) && (
            <span
              data-testid="logs-not-ready-hint"
              className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground"
              title={t('logsFederation.degradedHint')}
            >
              <Info className="size-3.5 shrink-0" />
              <span className="truncate">{t('logsFederation.degradedTitleShort')}</span>
            </span>
          )}
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="outline"
              size="sm"
              disabled={exportDisabled}
              data-testid="logs-export-trigger"
              title={
                exportBlockedByFederation && federation?.exportAffordance.blockedKey
                  ? t(federation.exportAffordance.blockedKey)
                  : federation && !federation.degraded && federation.exportAffordance.showDownload
                    ? t(LOGS_FEDERATION_KEYS.exportPermissionRecheckHint)
                    : undefined
              }
            >
              <Download className="size-3.5" />
              {exporting ? t('logs.exporting') : t('logs.export')}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {EXPORT_SCOPES.map((scope) => (
              <DropdownMenuItem
                key={scope}
                disabled={scope === 'range' && range === 'all'}
                onSelect={() => handleExport(scope)}
              >
                {t(`logs.exportScope_${scope}`)}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <div
        className="jm-toolbar-surface flex flex-wrap gap-1 p-1"
        role="tablist"
        aria-label={t('logs.viewLabel')}
      >
        {LOG_VIEWS.filter(
          // legacy 是「切换前存量日志」的只读入口，不是平级主视图：收纳到右侧「更多」，
          // 避免与平台/节点视图并列造成误解，也减少工具栏元素数量。
          (candidate) =>
            candidate !== 'legacy' && (isPlatformAdmin || candidate === 'node_instance'),
        ).map(
          (candidate) => (
            <Button
              key={candidate}
              variant={view === candidate ? 'default' : 'ghost'}
              size="sm"
              role="tab"
              aria-selected={view === candidate}
              onClick={() => {
                setView(candidate)
                setPage(1)
				setFederationCursors({ 1: '' })
				if (candidate === 'legacy') setFollow(false)
              }}
            >
              {t(`logs.view_${candidate}`)}
            </Button>
          ),
        )}
        {/* 右侧收纳：非常用入口（Legacy 只读）进「更多」，保持主视图区稳定。 */}
        <div className="ml-auto flex items-center">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="sm"
                data-testid="logs-more-trigger"
                aria-label={t('logs.more')}
              >
                <MoreHorizontal className="size-4" />
                {t('logs.more')}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                data-testid="logs-view-legacy"
                disabled={!isPlatformAdmin}
                onSelect={() => {
                  setView('legacy')
                  setPage(1)
                  setFederationCursors({ 1: '' })
                  setFollow(false)
                }}
              >
                {t('logs.view_legacy')}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>

      {/* 强筛选工具栏 */}
      <div className="jm-toolbar-surface flex flex-wrap items-center gap-2 p-2">
        {/* 级别快速 pill（全部 + 四级） */}
        <div className="flex items-center gap-1">
          <LevelPill active={level === ''} onClick={() => resetTo(setLevel)('')}>
            {t('logs.allLevels')}
          </LevelPill>
          {LEVELS.map((l) => (
            <LevelPill
              key={l}
              level={l}
              active={level === l}
              onClick={() => resetTo(setLevel)(level === l ? '' : l)}
            >
              {t(`logs.level_${l}`)}
            </LevelPill>
          ))}
        </div>

        <Input
          value={keyword}
          onChange={(e) => resetTo(setKeyword)(e.target.value)}
          placeholder={t('logs.searchPlaceholder')}
          className="h-9 w-52"
        />
        <Select
          value={range}
          onValueChange={(v: string) => resetTo(setRange)(v as TimeRangePreset)}
        >
          <SelectTrigger size="sm" className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {TIME_RANGE_PRESETS.map((r) => (
              <SelectItem key={r} value={r}>
                {t(`logs.range_${r}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {view === 'all' && (
          <Select
            value={source === '' ? SENTINEL_ALL : source}
            onValueChange={(v: string) => resetTo(setSource)(v === SENTINEL_ALL ? '' : v)}
          >
            <SelectTrigger size="sm" className="w-32">
              <SelectValue placeholder={t('logs.allSources')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={SENTINEL_ALL}>{t('logs.allSources')}</SelectItem>
              {SOURCES.map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`logs.source_${s}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {/* 节点/实例选择器始终占位（platform 视图下禁用），避免切换视图时工具栏元素增减导致布局位移。 */}
        <Select
            disabled={view === 'platform'}
            value={nodeId === null ? SENTINEL_ALL : String(nodeId)}
            onValueChange={(v: string) =>
              resetTo(setNodeId)(v === SENTINEL_ALL ? null : Number(v))
            }
          >
            <SelectTrigger size="sm" className="w-36">
              <SelectValue placeholder={t('logs.allNodes')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={SENTINEL_ALL}>{t('logs.allNodes')}</SelectItem>
              {nodes?.map((node) => (
                <SelectItem key={node.id} value={String(node.id)}>
                  {node.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        <Select
            disabled={view === 'platform'}
            value={instanceId === null ? SENTINEL_ALL : String(instanceId)}
            onValueChange={(v: string) =>
              resetTo(setInstanceId)(v === SENTINEL_ALL ? null : Number(v))
            }
          >
            <SelectTrigger size="sm" className="w-44">
              <SelectValue placeholder={t('logs.allInstances')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={SENTINEL_ALL}>{t('logs.allInstances')}</SelectItem>
              {instances?.map((inst) => (
                <SelectItem key={inst.id} value={String(inst.id)}>
                  {inst.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

        {/* 实时跟随开关 pill，靠右 */}
        <button
          type="button"
          aria-pressed={follow}
		  onClick={() => {
			setFollow((f) => !f)
			setPage(1)
			setFederationCursors({ 1: '' })
		  }}
		  disabled={legacyMode}
          className={cn(
            'ml-auto inline-flex items-center gap-1.5 rounded-full px-3 py-1.5 text-xs font-medium transition-colors duration-200 ease-ios',
            follow
              ? 'bg-status-success/15 text-status-success'
              : 'bg-muted text-muted-foreground hover:bg-accent',
          )}
        >
          <Radio className={cn('size-3.5', follow && 'animate-pulse')} />
          {t('logs.follow')}
        </button>
      </div>

	  {/* 洞察栏固定占位（高度恒为 min-h-8）：此前仅在联邦活跃且有数据时整条插入，
	      进入页面后会「弹」出一条并把日志列表往下推。现改为始终渲染容器，内容为空时
	      仅占位不显示任何文字，列表位置保持稳定。 */}
	  {!follow && (
		<div data-testid="logs-federation-insights" className="flex min-h-8 flex-wrap items-center gap-2 border-y px-1 py-1 text-xs text-muted-foreground">
		  {federationActive && statsCount !== undefined && (
			<span>{t('logs.statsEvents', { count: statsCount })}</span>
		  )}
		  {levelFacets.map((facet) => (
			<StatusBadge
			  key={facet.value}
			  level={logLevelStatus(facet.value.toLowerCase())}
			  label={`${facet.value} ${facet.count}`}
			/>
		  ))}
		</div>
	  )}

      {/* FR-481 覆盖/失败态横幅：partial/offline/failed 不得当空成功。 */}
      {federation && (
        <CoverageBanner
          federation={federation}
          onAction={handleFederationAction}
          onRetry={() => {
			if (legacyMode) void legacyQuery.refetch()
			else { void federationQuery.refetch(); void refetchLogs() }
          }}
        />
      )}

      {isLoading && !hasData ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        <p className="text-destructive">{t('logs.loadError')}</p>
      ) : (
        <Panel
          className="min-h-0 flex-1"
          bodyClassName="flex min-h-0 flex-col p-0"
        >
          <LogTimeline
            items={items}
            total={total}
            follow={follow}
            loading={isLoading}
            emptySuccessAllowed={emptySuccessAllowed}
          />
          <LogFooter
            follow={follow}
            total={total}
            page={follow ? 1 : page}
            totalPages={totalPages}
            onPrev={() => setPage((p) => Math.max(1, p - 1))}
			onNext={() => {
				if (federationActive) {
					const cursor = federation.response.nextCursor
					if (!cursor) return
					setFederationCursors((current) => ({ ...current, [page + 1]: cursor }))
				}
				setPage((p) => Math.min(totalPages, p + 1))
			}}
          />
        </Panel>
      )}
    </div>
  )
}

/**
 * 覆盖/失败态横幅（FR-481）：消费 foundation 的 classify → toCoverageBannerProps。
 * visible=false 时不渲染；tone 驱动 token 配色；导出被阻断时展示原因。
 */
function CoverageBanner({
  federation,
  onRetry,
  onAction,
}: {
  federation: LogsFederationResult
  onRetry: () => void
  onAction: (key: string) => void
}) {
  const { t } = useTranslation()
  const props: CoverageBannerProps = federation.banner
  const classified = federation.classified
  const sourceTag = federation.sourceTag
  const degraded = federation.degraded
  const federatedNotReady = federation.federatedNotReady
  // 404 降级始终展示横幅（即使 foundation 因 items 路径判定 visible=false）。
  const visible = props.visible || degraded || federatedNotReady
  if (!visible) return null

  // retry 有独立按钮与处理；其余可操作引导逐条落成按钮，避免计算了却不呈现。
  const guidanceActions = props.actionKeys.filter(
    (key) => key !== LOGS_FEDERATION_KEYS.actionRetry,
  )

  const tone = degraded ? 'info' : federatedNotReady ? 'warning' : props.tone
  const title = degraded
    ? t('logsFederation.degraded')
    : federatedNotReady
      ? t('logsFederation.federatedNotReady')
      : t(props.titleKey)

  return (
    <div
      data-testid="logs-coverage-banner"
      data-tone={tone}
      data-state={classified.state}
      data-degraded={degraded ? 'true' : 'false'}
      data-export-allowed={federation.exportAffordance.showDownload ? 'true' : 'false'}
      className={cn(
        'flex flex-col gap-1.5 rounded-md border px-3 py-2 text-sm',
        {
          'border-status-danger/40 bg-status-danger/10 text-status-danger': tone === 'danger',
          'border-status-warning/40 bg-status-warning/10 text-status-warning':
            tone === 'warning',
          'border-status-info/40 bg-status-info/10 text-status-info': tone === 'info',
          'border-status-success/40 bg-status-success/10 text-status-success':
            tone === 'success',
        },
      )}
      role="status"
    >
      <div className="flex flex-wrap items-center gap-2 font-medium">
        {tone === 'danger' || tone === 'warning' ? (
          <AlertTriangle className="size-4 shrink-0" />
        ) : (
          <Info className="size-4 shrink-0" />
        )}
        <span data-testid="logs-coverage-title">{title}</span>
        {sourceTag && (
          <span data-testid="logs-source-tag-banner" className="inline-flex">
            <StatusBadge
              level={sourceTag === 'legacy' ? 'warning' : 'info'}
              label={t(`logsFederation.sourceTag.${sourceTag}`)}
            />
          </span>
        )}
      </div>
      {(props.reasonKeys.length > 0 || classified.primaryReason) && (
        <ul className="flex flex-wrap gap-x-3 gap-y-0.5 text-xs opacity-90">
          {props.reasonKeys.map((key) => (
            <li key={key} data-testid="logs-coverage-reason">
              {t(key)}
            </li>
          ))}
        </ul>
      )}
      {!federation.exportAffordance.showDownload &&
        federation.exportAffordance.blockedKey && (
          <p data-testid="logs-export-blocked" className="text-xs">
            {t(federation.exportAffordance.blockedKey)}
          </p>
        )}
      {!classified.emptySuccessAllowed && (
        <p data-testid="logs-empty-not-success" className="text-xs opacity-80">
          {t('logsFederation.emptyNotSuccess')}
        </p>
      )}
      {classified.viewEnumerationEnded && !classified.historyFullyEnumerated && (
        <p data-testid="logs-next-cursor-hint" className="text-xs opacity-80">
          {t('logsFederation.export.nextCursorHint')}
        </p>
      )}
      {(props.showRetry || guidanceActions.length > 0) && !degraded && (
        <div className="flex flex-wrap gap-2">
          {props.showRetry && (
            <Button size="sm" variant="outline" onClick={onRetry} data-testid="logs-coverage-retry">
              {t('logsFederation.action.retry')}
            </Button>
          )}
          {guidanceActions.map((key) => (
            <Button
              key={key}
              size="sm"
              variant="outline"
              data-testid={`logs-coverage-action-${key}`}
              onClick={() => onAction(key)}
            >
              {t(key)}
            </Button>
          ))}
        </div>
      )}
    </div>
  )
}

/** 级别快速筛选 pill：选中态主色淡染，非选中态弱色；带级别时前导状态色点。 */
function LevelPill({
  level,
  active,
  onClick,
  children,
}: {
  level?: string
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  const status = level ? logLevelStatus(level) : null
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium transition-colors duration-200 ease-ios',
        active
          ? 'bg-primary/10 text-primary'
          : 'text-muted-foreground hover:bg-accent hover:text-foreground',
      )}
    >
      {status && (
        <span
          className={cn('size-1.5 rounded-full', {
            'bg-status-danger': status === 'danger',
            'bg-status-warning': status === 'warning',
            'bg-status-info': status === 'info',
            'bg-muted-foreground': status === 'neutral',
          })}
        />
      )}
      {children}
    </button>
  )
}

/**
 * 日志时间线（虚拟滚动）：固定行高，仅渲染视口附近的窗口，千行级日志不一次性入 DOM。
 * 跟随态下每次数据更新自动滚到顶部（最新在前）。
 */
function LogTimeline({
  items,
  total,
  follow,
  loading = false,
  emptySuccessAllowed = true,
}: {
  items: import('@/api/logs').LogEntry[]
  total: number
  follow: boolean
  /** 查询尚未完成：此时不判空态，避免把「未就绪」误报为空结果。 */
  loading?: boolean
  /** FR-481：false 时禁止把空列表呈成「无日志」成功空态。 */
  emptySuccessAllowed?: boolean
}) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement>(null)
  const [scrollTop, setScrollTop] = useState(0)

  // 跟随态：新数据到来时滚回顶部，保证最新行可见。
  useEffect(() => {
    if (follow && containerRef.current) {
      containerRef.current.scrollTop = 0
      setScrollTop(0)
    }
  }, [follow, items])

  const win = computeVirtualWindow({
    scrollTop,
    viewportHeight: VIEWPORT_HEIGHT,
    rowHeight: ROW_HEIGHT,
    total: items.length,
    overscan: OVERSCAN,
  })
  const visible = items.slice(win.startIndex, win.endIndex)

  // 查询尚未完成时不判空：交由上方 loading 分支呈现，避免把「未就绪」误报为空结果。
  if (items.length === 0 && !loading) {
    return (
      <div
        data-testid="logs-empty-state"
        data-empty-success={emptySuccessAllowed ? 'true' : 'false'}
        className="flex items-center justify-center text-sm text-muted-foreground"
        style={{ height: VIEWPORT_HEIGHT }}
      >
        {emptySuccessAllowed ? t('logs.empty') : t('logsFederation.emptyNotSuccess')}
      </div>
    )
  }

  return (
    <div
      ref={containerRef}
      onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
      data-testid="logs-virtual"
      data-total-count={total}
      className="min-h-0 flex-1 overflow-y-auto"
      style={{ maxHeight: VIEWPORT_HEIGHT }}
    >
      <div style={{ height: win.padTop }} />
      {visible.map((log) => (
        <LogRow key={log.id} log={log} />
      ))}
      <div style={{ height: win.padBottom }} />
    </div>
  )
}

/** 单条日志行（固定高度，等宽内容）：时间 + 级别徽标 + 来源 + 消息，hover 行高亮。 */
function LogRow({ log }: { log: import('@/api/logs').LogEntry }) {
  const { t } = useTranslation()
  return (
    <div
      data-testid="log-row"
      className="flex items-center gap-3 border-b border-border/60 px-3 text-xs transition-colors hover:bg-accent/50"
      style={{ height: ROW_HEIGHT }}
    >
      <span className="w-40 shrink-0 font-mono text-[11px] text-muted-foreground">
        {new Date(log.time).toLocaleString()}
      </span>
      <StatusBadge
        level={logLevelStatus(log.level)}
        label={t(`logs.level_${log.level}`, log.level)}
        className="w-16 shrink-0 justify-center"
      />
      <span className="w-14 shrink-0 text-[11px] text-muted-foreground">
        {t(`logs.source_${log.source}`, log.source)}
      </span>
      <span className="min-w-0 flex-1 truncate font-mono text-[11px]" title={log.message}>
        {log.message}
      </span>
    </div>
  )
}

/** 时间线底部条：跟随态显实时指示；否则显分页。 */
function LogFooter({
  follow,
  total,
  page,
  totalPages,
  onPrev,
  onNext,
}: {
  follow: boolean
  total: number
  page: number
  totalPages: number
  onPrev: () => void
  onNext: () => void
}) {
  const { t } = useTranslation()
  if (follow) {
    return (
      <div className="flex shrink-0 items-center gap-2 border-t px-3 py-2 text-[11px] text-status-success">
        <span className="size-1.5 animate-pulse rounded-full bg-status-success" />
        {t('logs.followingHint')}
        <span className="ml-auto text-muted-foreground">{t('logs.totalCount', { count: total })}</span>
      </div>
    )
  }
  return (
    <div className="flex shrink-0 items-center justify-between border-t px-3 py-2 text-sm text-muted-foreground">
      <span>{t('logs.totalCount', { count: total })}</span>
      <div className="flex items-center gap-2">
        <Button variant="outline" size="sm" disabled={page <= 1} onClick={onPrev}>
          {t('logs.prevPage')}
        </Button>
        <span>{t('logs.pageInfo', { page, totalPages })}</span>
        <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={onNext}>
          {t('logs.nextPage')}
        </Button>
      </div>
    </div>
  )
}
