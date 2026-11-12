import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useNavigate, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { AlertTriangle, Download, Info, Radio } from 'lucide-react'
import { type LogQueryParams } from '@/api/logs'
import {
	buildFederationResult,
  describeLogAggregate,
  logQueryErrorMessage,
  isLogRequestCanceled,
	exportFederatedLogs,
	FederatedExportIncompleteError,
	isRateLimitError,
	rateLimitedEnvelope,
  useLogsFederation,
  useLogsFederationFacets,
  useLogsFederationFields,
  useLogsFederationStats,
  useLogsFederationStreamFieldNames,
  useLogsFederationStreamFieldValues,
  useLogsFederationStreams,
	type LogsFederationResult,
} from '@/api/logFederation'
import { useAuthStore } from '@/stores/auth'
import { useLiveLogStream } from '@/lib/logs/use-live-log-stream'
import { buildLogTimeBuckets } from '@/lib/logs/log-time-buckets'
import { subscribeLiveLogs } from '@/api/logLive'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { LogExportRangeDialog } from '@/components/logs/LogExportRangeDialog'
import {
  EventsTotalCard,
  LevelDistributionCard,
  TimeDistributionCard,
  TopSourcesCard,
} from '@/components/logs/LogsInsightCards'
import { LogTimeline } from '@/components/logs/LogTimeline'
import { TextLogWorkbench } from '@/components/logs/TextLogWorkbench'
import { LogFieldExplorer } from '@/components/logs/LogFieldExplorer'
import { PageHeader, PageHelp, PageShell } from '@jianmanager/ui/components/layout'
import { resolveSourceLabel } from '@/lib/logs/logs-source-label'
import { useDebounced } from '@/lib/hooks/use-debounced'
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
import { LOGS_FEDERATION_KEYS, exportBlockedKeyForReasons, type CoverageBannerProps } from '@/lib/logs-federation'
import type { LogEntry } from '@/api/logs'
import {
  logLevelStatus,
  timeRangeToParams,
  normalizeLogScope,
  logScopeKey,
  windowForAnchor,
  TIME_RANGE_PRESETS,
  LOG_VIEWS,
  type TimeRangePreset,
  type LogExportScope,
} from '@/lib/logs/logs-filters'

/**
 * 时间窗参数（P2-9）：跟随态**只下发下界 `from`，不下发上界 `to`**。
 *
 * 根因：跟随态旧实现把 `to` 钉在浏览器 `now`，任何时钟偏移都会让「正在写入」的事件落在
 * 窗外——Worker/实例时钟略快，或落盘时区未校正（+8h）产生的「未来时间戳」，都被 `to=now`
 * 排除，于是实时跟随轮询永远为空（用户观感「不实时」）。
 * 加固定宽限（now+5m）只能吸收固定量级的偏移，覆盖不了整时区偏移；而跟随视图的语义本就是
 * 「窗口内的最新事件」，没有「截至某时刻」的含义，故 FOLLOW_LIVE 直接不下发上界：
 * 下界仍由 `from` 界定（默认最近 24h），扫描上界仍受分区日 / VL 窗口约束，不会无界查询。
 */
function timeParamsFor(
  preset: TimeRangePreset,
  anchor: Date,
  follow: boolean,
): Pick<LogQueryParams, 'from' | 'to'> {
  const bounds = timeRangeToParams(preset, anchor)
  if (!follow) return bounds
  // `all` 预设本就无边界；相对预设保留 from、丢掉 to。
  return bounds.from ? { from: bounds.from } : {}
}

/**
 * 把事件的原始来源标识归一为下拉取值域（instance/control_plane/worker）。
 *
 * 事件结构（SourceIdentity）只带 log_source_id、不含类别字段，其取值形如
 * `inst:<实例ID>/<流>`（实例进程日志）或 `node:<节点ID>/<流>`（Worker/Node 自身日志），
 * 与下拉取值域不同构——直接渲染会查不到 i18n 键而显示原始 ID。
 * 前端与 CP 侧 federationSourcePrefix 使用同一套前缀约定，两侧须同步修改。
 */
function normalizeSource(raw: string): string {
  if (raw.startsWith('inst:') || raw.startsWith('instance:')) return 'instance'
  if (raw.startsWith('node:') || raw.startsWith('worker:')) return 'worker'
  if (raw.startsWith('control_plane:') || raw.startsWith('cp:')) return 'control_plane'
  // 已是取值域内的裸类别（含旧数据）原样返回；其余保持原值由 i18n 兜底显示。
  return raw
}


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
    // 原始来源串（`inst:2/file`）必须原样留下：展示「具体是哪个实例/节点」
    // 靠的就是它。`source` 存归一后的类别（instance/worker），两者不可互相替代。
    const sourceRef = typeof o.log_source_id === 'string'
      ? o.log_source_id
      : typeof o.source === 'string'
        ? o.source
        : undefined
    const fields = o.fields && typeof o.fields === 'object' && !Array.isArray(o.fields)
      ? Object.fromEntries(Object.entries(o.fields as Record<string, unknown>))
      : undefined
    return {
      id: typeof o.id === 'number' ? o.id : -(index + 1),
      source: normalizeSource(
        typeof o.source === 'string'
          ? o.source
          : typeof o.log_source_id === 'string'
            ? o.log_source_id
            : 'instance',
      ),
      ...(sourceRef ? { sourceRef } : {}),
      level,
      instanceId: typeof instRaw === 'number' ? instRaw : 0,
      instanceUuid: typeof o.instance_uuid === 'string' ? o.instance_uuid : '',
      nodeId: typeof nodeRaw === 'number' ? nodeRaw : 0,
      stream: typeof o.stream === 'string' ? o.stream : undefined,
      ...(fields ? { fields } : {}),
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

// 虚拟滚动参数已随 LogTimeline 迁入 components/logs/LogTimeline.tsx。
// 实时跟随轮询间隔（ms）。
const FOLLOW_INTERVAL = 3000

/**
 * 把时刻向下取整到最近一个 FOLLOW_INTERVAL 桶（纯函数，便于单测）。
 *
 * 跟随态的时间窗锚点必须落在桶边界上：若直接取挂钟毫秒，`from` 每次渲染都变，
 * queryKey 随之变化，react-query 视为新查询 → 响应永远落不到地。
 */
function bucketFloor(ms: number): number {
  return Math.floor(ms / FOLLOW_INTERVAL) * FOLLOW_INTERVAL
}

function timeBucketForRange(range: TimeRangePreset): string {
  switch (range) {
    case '15m': return '1m'
    case '1h': return '5m'
    case '24h': return '15m'
    case '7d': return '1h'
    case 'all': return '6h'
    default: return '15m'
  }
}

function isSafeLogsField(value: string): boolean {
  return /^[A-Za-z_][A-Za-z0-9_.-]*$/.test(value)
}

function escapeLogsQLString(value: string): string {
  return value.replaceAll('\\', '\\\\').replaceAll('"', '\\"')
}


/**
 * 日志中心（查看 / 检索 / 增强 + 覆盖/失败态接线）。
 * 实例日志使用 VL 的授权 Query View、字段分析和高级过滤；节点/平台使用文件窗口。
 * 实例跟随优先订阅原生 SSE，只有明确不支持时才轮询；原生选区与回看冻结显示。
 * 列表、统计和导出共享查询身份；失败与覆盖不完整均不得呈现为空成功。
 * 工具栏、洞察、状态与页脚保留固定槽位，日志只在结果区域内滚动。
 */
export default function LogsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [cancelledQueryKey, setCancelledQueryKey] = useState<string | null>(null)
  const { data: nodes } = useNodes()
  const { data: instances } = useInstances()
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const isPlatformAdmin = useAuthStore((state) => state.role === ROLE_PLATFORM_ADMIN)

	// 视图从 URL `?view=` 初始化（深链/书签/分享必须落到正确视图）。
	//
	// 2026-10-29 修：此前硬编码 `'node_instance'`，导致 `?view=platform` 这类深链
	// 一律静默回到「节点/实例日志」——页面不报错、只是视图不对，最难察觉的一类。
	// 非法值回退默认，不把 URL 里的垃圾值当成视图名。
	const [view, setView] = useState<(typeof LOG_VIEWS)[number]>(() => {
		const raw = searchParams.get('view')
		return (LOG_VIEWS as readonly string[]).includes(raw ?? '')
			? (raw as (typeof LOG_VIEWS)[number])
			: 'node_instance'
	})
  const [source, setSource] = useState(() => {
    const raw = searchParams.get('source') ?? ''
    return isPlatformAdmin && SOURCES.includes(raw) ? raw : ''
  })
  const [level, setLevel] = useState('')
  const [nodeId, setNodeId] = useState<number | null>(() => {
    const raw = searchParams.get('nodeId')
    const value = raw ? Number(raw) : NaN
    return isPlatformAdmin && Number.isSafeInteger(value) && value > 0 ? value : null
  })
  // 深链预筛选：从 URL ?instanceId= 初始化实例筛选（如从终端「查看完整历史」进入）。
  const [instanceId, setInstanceId] = useState<number | null>(() => {
    const q = searchParams.get('instanceId')
    const n = q ? Number(q) : NaN
    return Number.isFinite(n) && n > 0 ? n : null
  })
  const [keyword, setKeyword] = useState('')
  const [advancedMode, setAdvancedMode] = useState(false)
  const [advancedQuery, setAdvancedQuery] = useState('*')
  const [appliedAdvancedQuery, setAppliedAdvancedQuery] = useState('*')
  const debouncedKeyword = useDebounced(keyword.trim(), 250)
  // 首屏保留有界查询，默认覆盖最近一小时；更短或更长窗口由时间筛选器显式选择。
  const [range, setRange] = useState<TimeRangePreset>('1h')
  const [page, setPage] = useState(1)
	// 页历史保存原窗口和服务端复合游标；只有窗口枚举结束后才允许向更早窗口移动。
	const [federationCursors, setFederationCursors] = useState<Record<number,
		Pick<LogQueryParams, 'from' | 'to'> & { viewId?: string; cursor?: string }
	>>({})
  const [appliedKeyword, setAppliedKeyword] = useState(debouncedKeyword)
  if (appliedKeyword !== debouncedKeyword) {
    // 去抖后的条件生效时才回首页；输入中的旧页仍作占位，不抢先读旧条件首页。
    setAppliedKeyword(debouncedKeyword)
    setPage(1)
    setFederationCursors({})
  }
  // 「仅查在线节点」：显式开启后联邦扇出收缩到在线目标（online_only）。
  const [onlineOnly, setOnlineOnly] = useState(false)
  const [follow, setFollow] = useState(false)
  const [manualPaused, setManualPaused] = useState(false)
  const [interactionPaused, setInteractionPaused] = useState(false)
  const [streamField, setStreamField] = useState<'stream' | 'log_source_id'>('stream')
  const [exporting, setExporting] = useState(false)

  const [exportRangeOpen, setExportRangeOpen] = useState(false)
  const exportRequest = useRef<AbortController | null>(null)
  useEffect(() => () => exportRequest.current?.abort(), [])
  /**
   * 时间窗锚点：非跟随态下冻结，避免每次渲染 `new Date()` 使 useLogs /
   * useLogsFederation 的 queryKey 每毫秒变化导致请求风暴（接线后暴露）。
   * 跟随态在渲染时取当前时间，配合 refetchInterval 滚动时间窗。
   */
  // 仅显式切换范围/跟随时更新锚点，首屏不再由 effect 生成近重复查询。
  const [rangeAnchor, setRangeAnchor] = useState(() => Date.now())
  const [followAnchorMs, setFollowAnchorMs] = useState(() => bucketFloor(Date.now()))

  const timeParams = timeParamsFor(
    range,
    follow ? new Date(followAnchorMs) : new Date(rangeAnchor),
    follow,
  )
  // 平台页固定平台来源；节点/实例页可选 Worker 文件或实例 VL，非管理员只查实例。
  const selectedSource = view === 'platform' ? 'control_plane'
    : !isPlatformAdmin || (view === 'node_instance' && source === 'control_plane') ? 'instance'
      : source || 'instance'
  const advancedAllowed = view !== 'platform' && selectedSource === 'instance'
  const activeAdvanced = advancedMode && advancedAllowed
  const queryDraftDirty = activeAdvanced
    ? advancedQuery.trim() !== appliedAdvancedQuery
    : keyword.trim() !== debouncedKeyword
  const queryInputError = activeAdvanced && advancedQuery.trim() === ''
    ? t('logs.advancedEmpty')
    : activeAdvanced && new TextEncoder().encode(advancedQuery.trim()).length > 8192
      ? t('logs.advancedTooLong')
      : undefined
  const scope = normalizeLogScope({
    view,
    source: 'instance',
    level,
    ...(nodeId !== null ? { nodeId } : {}),
    ...(instanceId !== null ? { instanceId } : {}),
    ...(activeAdvanced
      ? { advanced: true, logsQL: appliedAdvancedQuery }
      : { keyword: debouncedKeyword }),
    ...(view !== 'platform' && onlineOnly ? { onlineOnly: true } : {}),
    ...timeParams,
    ...(!follow && page > 1 ? { from: federationCursors[page]?.from, to: federationCursors[page]?.to } : {}),
  })
  const params: LogQueryParams = { ...scope, page: follow ? 1 : page, pageSize: PAGE_SIZE }
  const scopeKey = logScopeKey(scope)

  // Platform and Worker process logs use bounded file windows, never VL.
  const textKind = isPlatformAdmin && selectedSource === 'control_plane'
    ? 'control_plane' : isPlatformAdmin && selectedSource === 'worker' ? 'worker' : null
  const federationEnabled = !textKind && view !== 'platform'
  // 可选联邦覆盖探测。与 useLogs 同筛选参数；API 404 → degraded legacy。
	const federationParams: Record<string, unknown> = {
		...params,
		limit: PAGE_SIZE,
		...(follow ? { tailMode: 'FOLLOW_LIVE' } : {}),
		...(onlineOnly ? { onlineOnly: true } : {}),
		// 续页不解析游标、不从日志时间重建窗口，保留纳秒同戳组和原视图身份。
		...(!follow && page > 1 ? federationCursors[page] : {}),
	}
  const liveParams = { ...scope, ...timeParamsFor(range, new Date(rangeAnchor), true), to: undefined }
  const liveState = useLiveLogStream(liveParams, follow && federationEnabled, manualPaused || interactionPaused, subscribeLiveLogs)
  const pollingFallback = follow && liveState.code === 'UNSUPPORTED'
  const liveDisplay = follow && federationEnabled && !pollingFallback
  const federationQuery = useLogsFederation(federationParams, {
    enabled: federationEnabled && (!follow || (pollingFallback && !manualPaused && !interactionPaused)),
  })
  /**
   * 最近一份「联邦可用且带 items」的成功结果，用于限流期间继续渲染已有行。
   *
   * 用 state 而非 ref：渲染期要**读**它来决定列表内容，ref 读值不触发重渲染且违反
   * 「渲染期不得访问 ref」；state 才能让保留生效。
   * 只记联邦结果：联邦不可用时本视图不取经典表（见 classicAuthoritative），
   * 记进别的来源会让限流期间把无关的行冒充成联邦结果。
   * 更新时机用「渲染期按需调整 state」而非 effect（React 官方推荐的派生 state 写法，
   * 也避免先渲染一帧空列表再把数据补回来的闪烁）。
   *
   * **必须携带筛选身份**：保留项若不含筛选键，切到新筛选后新请求持续 429（重试耗尽）时，
   * 列表会继续渲染**上一个筛选**的行（可能是另一个节点/实例）——筛选控件已显示新条件，
   * 内容却属于旧条件，运维会把 A 实例的日志读成 B 实例的。复用前必须比对 filterKey。
   */
  const [lastGoodFederation, setLastGoodFederation] = useState<{ filterKey: string; result: LogsFederationResult } | null>(null)
  /**
   * 限流提示闩锁：重试耗尽观测到 429 即置位，直到重新拿到**真实成功**结果才清除。
   *
   * 为什么不直接用 `federationQuery.error`：跟随态每 3 秒滚动时间桶 ⇒ queryKey 每次都变，
   * 「重试耗尽」只属于某一个 key，下一个 key 一开跑 error 就没了。实测（假后端持续返回 429、
   * 前进 9 个 tick）：列表确实保住了，但横幅**一次都没渲染**——提示跟着 error 一起闪没，
   * 用户只看到「数据不再更新」而没有任何原因说明。闩锁把「本轮请求正被限流」这一事实
   * 保持到真正恢复为止。
   */
  const [rateLimited, setRateLimited] = useState(false)
  // 筛选身份键（view/来源/在线过滤/实例/节点/级别/关键字/范围；**不含 page** ✗——翻页需保留滚动位置）。
  // 既驱动滚动复位（LogTimeline 的 filterKey effect），也是限流保留项的归属判据：
  // 保留项记的是这个键，筛选一变，旧保留项即判失效、不得冒充新筛选的结果。
  //
  // **必须与真正参与查询的筛选维度逐一对应**：`source`（仅 all 视图）与 `onlineOnly`
  // 都进 federationParams ⇒ 都是独立 queryKey。漏掉它们时，在该视图下切换这两项会「改筛选
  // 不改身份」：键不变 ⇒ 保留项继续被复用，而新查询若 429（生产该端点 23.8%），
  // 页面就把**上一个来源/未过滤**的行渲染成新筛选的结果（例如只看 worker 却显示 control_plane 的行）。
  // `onlineOnly` 尤其容易漏判成「只是排序/展示开关」——它实际是服务端过滤条件。
  const filterKey = follow ? `${logScopeKey({ ...scope, from: undefined, to: undefined })}|${range}` : scopeKey
  const latestFederation = federationQuery.data
  /**
   * 「这一轮请求真的拿到结果了」——用于清除闩锁。
   *
   * 不含 degraded 条件：queryFn 只对 429 抛出，其它失败（404/连接失败）都会被转成信封、
   * 在 react-query 层面算成功。若限定「非降级才算成功」，那么「先 429、随后接口变成 404」
   * 时闩锁永远清不掉，限流横幅会一直盖住后续状态。
   * 但必须排除占位数据：key 滚动期间 keepPreviousData 会用上一份顶替成「有数据」，
   * 那不是新的结果，不能据此宣告恢复。
   */
  const federationSettled = federationQuery.isSuccess && !federationQuery.isPlaceholderData
  if (federationQuery.isError && isRateLimitError(federationQuery.error)) {
    if (!rateLimited) setRateLimited(true)
  } else if (rateLimited && federationSettled) {
    setRateLimited(false)
  }
  if (
    !federationQuery.isError
    && !federationQuery.isPlaceholderData
    && latestFederation
    && !latestFederation.degraded
    && Array.isArray(latestFederation.response.items)
    && lastGoodFederation?.result !== latestFederation
  ) {
    // 记录当时的筛选身份：切筛选后旧保留项即失效（见 retainedFederation 的复用条件）。
    setLastGoodFederation({ filterKey, result: latestFederation })
  }
  const federationRateLimited = rateLimited
  const federation = federationEnabled
    ? (federationRateLimited
      ? buildFederationResult(rateLimitedEnvelope(
        federationQuery.error instanceof Error ? federationQuery.error.message : undefined,
      ))
      : federationQuery.data)
    : undefined

  // F-003：联邦视图的列表**只**以 federation.items 为主数据源；降级不再回退 useLogs 的
  // Legacy 表（用户裁定：没有就是没有）。禁止「联邦横幅 + 平台/历史内容」错配。
  const federationActive = !!federation && !federation.degraded && federation.available
  // 供标题区状态提示使用（与横幅同源，避免两处判定漂移）。
  const federationDegraded = federation?.degraded === true
  const federationNotReady = federation?.federatedNotReady === true
  const requestFailure = federation?.failure ?? federationQuery.error
  const queryFailureStatus = (requestFailure as { response?: { status?: number } } | null)?.response?.status
  const rawQueryFailure = !federationQuery.isPlaceholderData && (activeAdvanced || queryFailureStatus === 400)
    ? logQueryErrorMessage(requestFailure)
    : undefined
  const queryFailure = rawQueryFailure === 'LOG_ADVANCED_UNSUPPORTED' ? t('logs.advancedUnsupported') : rawQueryFailure
  const queryMeta = !federationQuery.isPlaceholderData ? federationQuery.data?.queryMeta : undefined
  const queryCancelled = cancelledQueryKey === filterKey
  const queryMetaLabel = !follow && queryMeta && !queryFailure && !queryInputError && !queryDraftDirty && !queryCancelled && !federationQuery.isFetching
    ? t('logs.queryMeta', {
      ms: Math.round(queryMeta.elapsed_ms), rows: queryMeta.returned_rows,
      limit: queryMeta.limit, bytes: Math.round(queryMeta.max_bytes / (1024 * 1024)),
      seconds: queryMeta.timeout_ms / 1000,
    })
    : null
  const applyAdvancedQuery = () => {
    if (queryInputError) return
    exportRequest.current?.abort()
    setCancelledQueryKey(null)
    setAppliedAdvancedQuery(advancedQuery.trim())
    setPage(1)
    setFederationCursors({})
    if (advancedQuery.trim() === appliedAdvancedQuery) void federationQuery.refetch()
  }
  const toggleAdvancedQuery = () => {
    exportRequest.current?.abort()
    setCancelledQueryKey(null)
    setAdvancedMode((current) => !current)
    setPage(1)
    setFederationCursors({})
  }
  const cancelQuery = () => {
    setCancelledQueryKey(filterKey)
    setFollow(false)
    void queryClient.cancelQueries({ queryKey: ['logsFederation', federationParams], exact: true })
  }
  /**
   * 质量标记（与横幅同源）：**身份冲突**与**未对账重复**是两个不同状态。
   * 只有 CONFLICT 曾可由人工清空诊断记录；UNRESOLVED（重复事件无法对账）从来没有
   * 可人工清空的东西。两者仍分开呈现，不得混成一个布尔。
   */
  const duplicateConflict = federation?.classified.coverage.duplicateConflict === true
  const duplicateUnresolved = federation?.classified.coverage.duplicateUnresolved === true

  /**
   * 质量维度阻断导出时的**自救引导**。
   *
   * 为什么必须有：导出被质量标记禁用后，若不给任何说明，用户会停在一个「按钮灰着、
   * 不知为何」的死路。引导只在「确实因为质量被阻断」时出现，正常态不加噪音。
   * 两类标记都没有可人工清空的入口，引导只说实情与可执行范围（缩小范围/稍后重试）。
   */
  const qualityGuidanceKey = !federationActive
    ? undefined
    : duplicateConflict
      ? LOGS_FEDERATION_KEYS.qualityConflictNeedsNode
      : duplicateUnresolved
        ? LOGS_FEDERATION_KEYS.qualityUnresolvedNoRemedy
        : undefined


  // 同条件的聚合可复用搜索视图；实际时间窗与在线目标必须逐字段同源。
  const aggregateParams: Record<string, unknown> = {
    ...scope,
    ...(federation?.response.viewId ? { viewId: federation.response.viewId } : {}),
  }
  const mainFetching = federationQuery.isFetching
  const refetchMain = federationQuery.refetch
  useEffect(() => {
    if (!pollingFallback || manualPaused || interactionPaused || mainFetching || exporting) return
    const timer = window.setTimeout(() => {
      if (timeParams.from) setFollowAnchorMs(bucketFloor(Date.now()))
      else void refetchMain()
    }, FOLLOW_INTERVAL)
    return () => window.clearTimeout(timer)
  }, [pollingFallback, manualPaused, interactionPaused, mainFetching, exporting, timeParams.from, refetchMain])

	// 编排（§1 延后串行）：① 主列表先出——stats/facets 均已 gate 在「主查询有数据且拿到 viewId」之后 ✓；
	// ② stats → facets 再串行——facets 额外等 stats 落定（成功或失败），
	// 任一时刻最多一个聚合请求在飞 ⇒ 不再与主列表/彼此并发挤 VL 的 maxConcurrentRequests=2 两个槽 ✗。
	const federationStats = useLogsFederationStats(
		{ ...aggregateParams, groupBy: '', timeBucket: timeBucketForRange(range) },
		federationActive && !follow && !federationQuery.isPlaceholderData && !federationQuery.isFetching && !!federation?.response.viewId,
	)
	const federationFacets = useLogsFederationFacets(
		{ ...aggregateParams, dimensions: 'level,stream', dimensionLimit: 8 },
		federationActive && !follow && !federationQuery.isPlaceholderData && !federationQuery.isFetching && !!federation?.response.viewId
			&& (federationStats.isSuccess || federationStats.isError),
	)
	// 流清单：等 facets 落定后取（保持「任一时刻最多一个聚合请求在飞」的串行纪律）。
	// 与 facets 的区别：facets 给「级别分布」，streams 给「哪些源在刷日志、多少条」——
	// 后者才是「哪个实例在刷屏」这个问题的直接答案。
	const federationStreams = useLogsFederationStreams(
		aggregateParams,
		federationActive && !follow && !federationQuery.isPlaceholderData && !federationQuery.isFetching && !!federation?.response.viewId
			&& (federationFacets.isSuccess || federationFacets.isError),
	)
	// Stats 计数来自后端 `agg.count`（wire 真源）；兼容扁平 `count`。
  const statsPoints = federationStats.data?.points
  const statsCount = !federationStats.isError && Array.isArray(statsPoints)
    && statsPoints.every((point) => Number.isFinite(point.agg?.count ?? point.count) && Number(point.agg?.count ?? point.count) >= 0)
    ? statsPoints.reduce((sum, point) => sum + Number(point.agg?.count ?? point.count), 0) : undefined
  const statsSummary = describeLogAggregate(federationStats.data)
  const facetsSummary = describeLogAggregate(federationFacets.data)
  const levelDimension = federationFacets.data?.dimensions?.find((dimension) => dimension.dimension === 'level')
	const levelFacets = !federationFacets.isError ? levelDimension?.values ?? [] : []
	const facetsTruncated = facetsSummary.truncated || federationFacets.data?.dimensions?.some((dimension) => dimension.truncated) === true
	// Fields / Streams / stream fields 均只在实例联邦查询的同一 Query View 内启用。
	const aggregateReady = federationActive && !follow && !federationQuery.isPlaceholderData && !federationQuery.isFetching && !!federation?.response.viewId
	const federationFields = useLogsFederationFields(
		{ ...aggregateParams, explore: true },
		aggregateReady && (federationFacets.isSuccess || federationFacets.isError),
	)
  const fieldDiscovery = describeLogAggregate(federationFields.data)
  const fieldDiscoveryUnsupported = (federationFields.error as { response?: { status?: number } } | null)?.response?.status === 501
    || fieldDiscovery.reasons.some((reason) => /unsupported|unimplemented/i.test(reason))
  const fieldDiscoveryStatus = fieldDiscoveryUnsupported ? 'unsupported'
    : federationFields.isError ? 'failed' : federationFields.isSuccess
      ? Array.isArray(federationFields.data.fields) && federationFields.data.coverage?.complete === true
        && !fieldDiscovery.partial && !fieldDiscovery.truncated && !fieldDiscovery.pending ? 'ready' : 'failed'
      : 'loading'
	const federationStreamFieldNames = useLogsFederationStreamFieldNames(
		aggregateParams,
		aggregateReady && (federationStreams.isSuccess || federationStreams.isError),
	)
	const federationStreamFieldValues = useLogsFederationStreamFieldValues(
		aggregateParams,
		streamField,
		aggregateReady,
	)
	// 流清单（源 + 流名 + 条数）。complete=false 时条数不可信。
	const streamItems = !federationStreams.isError ? federationStreams.data?.streams ?? [] : []
	const streamsComplete = federationStreams.data?.complete === true
  const insightsVisible = federationActive && !federationQuery.isPlaceholderData
  /**
   * 列表主数据源。限流中断时回落到 {@link lastGoodFederation} 的最近一份成功列表：
   * 已有行继续可见（不清空），绝不把限流呈成「暂无日志」。
   *
   * 为什么要在这里兜底而不能只靠 react-query：跟随态每 3 秒滚动时间桶 ⇒ queryKey 每次都变，
   * 而 key 变化后的失败会把 data 丢弃（实测：keepPreviousData 只在同 key 重取时保留，
   * 换 key 后失败则 data=undefined）⇒ 列表照样被清空。
   *
   * **只在筛选身份一致时复用**：保留项记着它所属筛选的 filterKey，与当前筛选不一致
   * （说明用户刚切了筛选）时不得复用——否则切筛选后服务端持续 429，页面会拿旧筛选
   * （另一节点/实例）的行冒充新筛选的结果，比空列表更危险。
   */
  const retainedFederation = federationRateLimited && lastGoodFederation?.filterKey === filterKey
    ? lastGoodFederation.result
    : null
  const federationItems = federationActive
    ? mapFederationItems(
      federationRateLimited
        // 限流且拿不到上一份列表时保持 null → 交给 timeline 的 loading/空态语义处理，
        // 由横幅与 emptySuccessAllowed=false 说明「这次没查到」，不伪造 0 条。
        ? retainedFederation?.response.items ?? null
        : federation.response.items,
    )
    : null

  const platformView = !federationEnabled
  const items = liveDisplay ? mapFederationItems(liveState.items) : federationItems ?? []
  const total = items.length
  // 有游标时必须原 view 续页（预算截断空页也可续）；明确结束才移动完整窗口。
  const timeDistribution = useMemo(() => buildLogTimeBuckets(statsPoints ?? [], {
    from: scope.from, to: scope.to, timeBucket: timeBucketForRange(range), exact: statsSummary.exact,
  }), [statsPoints, scope.from, scope.to, range, statsSummary.exact])

  const availableFields = federationFields.data?.fields ?? []
  const availableStreamFields = (federationStreamFieldNames.data?.fields ?? [])
    .filter((field): field is 'stream' | 'log_source_id' => field === 'stream' || field === 'log_source_id')
  const availableStreamValues = federationStreamFieldValues.data?.values ?? []


  /**
   * 把一行日志映射成「具体是谁」的显示名。
   *
   * 直接展示 `inst:2/file` 没人看得懂，而只写「实例」又丢了“到底是哪一个”。
   * 中间形态是真实名称（`logtest-mc`），拉不到名称时退回带 id 的写法（`实例 #2`）。
   * 依赖 nodes/instances 列表；列表未到达时先显示带 id 的写法，到达后自动补齐。
   */
  const instanceNames = useMemo(() => (instances ?? []).map((it) => ({ id: it.id, name: it.name })), [instances])
  const nodeNames = useMemo(() => (nodes ?? []).map((n) => ({ id: n.id, name: n.name })), [nodes])
  const sourceLabelOf = useCallback(
    (log: LogEntry) => {
      const resolved = resolveSourceLabel(log, { instances: instanceNames, nodes: nodeNames })
      if (resolved.name) return resolved.name
      if (resolved.id !== null && resolved.kind !== 'unknown' && resolved.kind !== 'control_plane') {
        return `${t(`logs.source_${resolved.kind}`, resolved.kind)} #${resolved.id}`
      }
      return t(`logs.source_${resolved.kind}`, resolved.kind)
    },
    [instanceNames, nodeNames, t],
  )

  const federationCanContinue = Boolean(federation?.response.nextCursor && federation?.response.viewId)
  const federationCanShift = !federation?.response.nextCursor && federation?.response.exhausted === true
    && items.length > 0 && range !== 'all' && typeof federationParams.from === 'string'
  const federationNextAvailable = federationActive && !follow
    && (federationCanContinue || federationCanShift)
    && !federationQuery.isFetching && !federationQuery.isPlaceholderData

  // 页码＝**已访问页数**（page），不再对"还有下一页"无条件加 1 ✗——CP 在"恰好整页到边"时也会发
  // `next_cursor`（hasMore 含 workerOpen ✓），旧式无条件 `page + 1` 于是把 100 条整页显示成 2/2 ✗。
  // 「下一页」按钮的可点性由查询结果的 nextCursor 直接驱动（不复用本页数值 ✓）。
  //
  // 联邦模式没有服务端总数：分母只能给**保守下界**——确认还能继续（next_cursor 或可移窗）时
  // page+1，否则 page。恒 `Math.max(1, page)` 会让分母恒等分子（「第 N / N 页」）而丢失
  // 「还有下一页」的判读信息；无条件 page+1 又在恰好整页到边时谎报一页。
  const totalPages = federationActive
    ? (federationNextAvailable ? page + 1 : page)
    : Math.max(1, Math.ceil(total / PAGE_SIZE))

  // 改任一筛选都回到第 1 页，避免停留在越界页。
  const resetTo = useCallback(
    <T,>(setter: (v: T) => void) =>
      (v: T) => {
        exportRequest.current?.abort()
        setter(v)
        setInteractionPaused(false)
        setPage(1)
        setFederationCursors({})
      },
    [],
  )

  /** 结构化字段/流筛选只由受控字段名和值生成 LogsQL 片段；不接受用户输入拼接字段名。 */
  const appendStructuredFilter = useCallback((expression: string) => {
    const trimmed = expression.trim()
    if (!trimmed) return
    exportRequest.current?.abort()
    if (activeAdvanced) {
      setAdvancedQuery((current) => {
        const previous = current.trim()
        return previous ? `(${previous}) AND ${trimmed}` : trimmed
      })
    } else {
      setKeyword((current) => {
        const previous = current.trim()
        return previous ? `(${previous}) AND ${trimmed}` : trimmed
      })
    }
    setPage(1)
    setFederationCursors({})
  }, [activeAdvanced])

  /**
   * 切换视图：只重置分页状态，**保留** nodeId / instanceId 等筛选。
   *
   * 保留筛选而非清空：用户选定的范围不该在切视图时被替他放大（曾一度在这里清掉
   * nodeId/instanceId，依据是「平台日志不带这两个维度」——该前提是错的，已回退）。
   *
   * 已无 `legacy` 分支：该视图随 CP `logs` 表删除而下线（2026-10-29）。
   */
  const switchView = useCallback(
    (candidate: (typeof LOG_VIEWS)[number]) => {
      exportRequest.current?.abort()
      setView(candidate)
      setInteractionPaused(false)
      setPage(1)
      setFederationCursors({})
    },
    [],
  )

  // 导出门禁（2026-10-29 变更）：
  //
  // CP `/logs/export` 已随 `logs` 表一并下线，**平台视图没有导出能力**。
  // 要导出就把 grep 结果自己重定向（面板会给出可复制的命令）。
  //
  // 联邦视图仍按覆盖/校验门禁；平台文件尾部不提供导出（见上）。
  const classicExportAllowed = false
  const exportBlockedByFederation =
    !!federation && !classicExportAllowed && !federation.exportAffordance.showDownload
  const exportDisabled = platformView || exporting || total === 0 || exportBlockedByFederation
    || queryDraftDirty || !!queryInputError
    || (federationEnabled ? federationQuery.isFetching || federationQuery.isPlaceholderData || federationQuery.isError
      : true)
	// federated 探测未落地前，items 可能暂时为空——此时不得判为「空结果」，
	// 否则会先闪 empty-state（且 404 降级期间显示非成功空态），把尚未就绪误报成失败。
	// 故把「联邦探测未完成」并入 loading：只有探测结束、数据源确定后才允许判空。
	//
	// 联邦视图下经典查询根本不参与页面态（`classicAuthoritative=false` ⇒ 不发起、无数据）：
	// 页面态只由联邦探测与联邦结果决定，不再存在「降级后由经典表说话」这条路径。
	const federationProbePending = federationEnabled && federationQuery.isLoading
  const isLoading = liveDisplay ? liveState.status === 'connecting' : federationProbePending
  const isError = liveDisplay && liveState.status === 'error' && items.length === 0
	// 空结果语义：失败/partial 不得呈「暂无日志」空成功。
	// 查询服务未启用（降级）亦然：经典路径可能确实没有行，但引擎侧数据从未被查询，
	// 此时宣称「暂无日志」是假成功，必须保留非成功空态。
	// 限流（rate_limited）同样是失败态：emptySuccessAllowed 由 classified 判为 false。
	// 联邦视图下**没有**「降级即空成功」这条例外了：降级只说明查不到，不允许显示成
	// 「暂无日志」——那是把未查询说成不存在。平台视图仍按经典路径判定。
  const emptySuccessAllowed = federationEnabled
		? (federation?.classified.emptySuccessAllowed ?? false)
		: (!federation || federation.classified.emptySuccessAllowed)

  // 文件来源由 TextLogWorkbench 独立分类；此处只解释当前实例 VL 查询。
  const emptyReason = federation?.response.coverage?.pending ? 'pending'
    : !emptySuccessAllowed ? 'not_ready' : 'no_data'

  const emptyHint = t(`logs.emptyHint${emptyReason === 'pending' ? 'Pending' : 'NoData'}`)

  const handleExport = async (exportScope: LogExportScope, bounds?: { from: string; to: string }) => {
    if (exportDisabled || exportRequest.current) return
    if (exportScope === 'range' && !bounds) { setExportRangeOpen(true); return }
    if (exportScope === 'currentPage' && follow) {
      // Tail 没有固定页身份；先暂停并建立 Search 页，不能把另一页伪装成当前页。
      setFollow(false)
      setRangeAnchor(followAnchorMs)
      toast.info(t('logs.exportPauseFollow'))
      return
    }
    const controller = new AbortController()
    exportRequest.current = controller
    setExporting(true)
    const frozenScope = normalizeLogScope({ ...scope, ...bounds })
    const matchingView = logScopeKey(frozenScope) === scopeKey && !follow ? federation?.response.viewId : undefined
    const frozenPage = structuredClone(federation?.response.items ?? [])
    try {
      if (federationActive) {
        await exportFederatedLogs({
          ...frozenScope,
          ...(matchingView ? { viewId: matchingView } : {}),
          ...(exportScope === 'currentPage'
            ? { range: 'page', limit: PAGE_SIZE, ...(federationParams.cursor ? { cursor: federationParams.cursor } : {}) }
            : { range: 'all' }),
        }, { signal: controller.signal, ...(exportScope === 'currentPage' ? { pageItems: frozenPage } : {}) })
      }
      setExportRangeOpen(false)
      toast.success(t('logs.exportStarted'))
    } catch (error) {
      if (controller.signal.aborted || isLogRequestCanceled(error)) return
      if (error instanceof Error && error.message === 'LOG_PAGE_CHANGED') {
        toast.error(t('logs.exportPageChanged'))
        return
      }
      // 导出闸阻断（覆盖/质量/预算未过）与传输失败分开呈现：前者给出后端原因，
      // 让用户知道「为什么没有文件」，而不是以为功能坏了。
      if (error instanceof FederatedExportIncompleteError && error.reasons.length > 0) {
        // 预算类阻断（行数 / 字节上限）优先用专用文案：笼统的「预算超了」
        // 无法指导操作，而「超过单次 N 行」能直接告诉用户缩小范围还是调配置。
        // 有 limits 时把实际数字一并说出（后端本次采用的上限）。
        const preciseKey = exportBlockedKeyForReasons(error.reasons)
        if (preciseKey) {
          const maxRows = error.limits?.maxRows
          toast.error(
            maxRows
              ? `${t(preciseKey)}（本次上限 ${maxRows} 行）`
              : t(preciseKey),
          )
        } else {
          toast.error(t('logs.exportBlockedReasons', { reasons: error.reasons.join(', ') }))
        }
      } else {
        toast.error(t('logs.exportFailed'))
      }
    } finally {
      if (exportRequest.current === controller) exportRequest.current = null
      setExporting(false)
    }
  }

  /**
   * 失败态可操作引导：把 helper 给出的 actionKey 落成真实动作。
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
          setFederationCursors({})
          setPage(1)
          void federationQuery.refetch()
          break
        case LOGS_FEDERATION_KEYS.actionRequestOnline:
          setOnlineOnly(true)
          setPage(1)
          setFederationCursors({})
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

  const sourceControl = (
    <Select disabled={view === 'platform' || !isPlatformAdmin} value={selectedSource} onValueChange={(value: string) => resetTo(setSource)(value)}>
      <SelectTrigger size="sm" className="w-32" data-testid="logs-source-select"><SelectValue /></SelectTrigger>
      <SelectContent>{SOURCES.filter((value) => (isPlatformAdmin || value === 'instance') && (view !== 'node_instance' || value !== 'control_plane')).map((value) => <SelectItem key={value} value={value}>{t(`logs.source_${value}`)}</SelectItem>)}</SelectContent>
    </Select>
  )

  return (
    <PageShell variant="fixed" data-page="logs">
      {/* 页头用布局层 PageHeader：标题 + 帮助浮层 + 右侧操作。
       *
       * 为何换成它而不是继续手写 jm-page-header/jm-page-title：
       * 仓库里 49 个页面已经统一走 PageHeader（标题只出现一次、位于最上、
       * 说明收进问号里）。日志页是少数还留着手写骨架的，导致标题字号、
       * 与操作区的对齐都与别处不同——同一产品里两个页面长得不一样，
       * 用户会以为是不同的东西。
       *
       * 帮助用 help（点击展开浮层）而不是 description（悬停 Tooltip）：
       * 本页要讲清「三种视图的区别」「三类来源」「覆盖完整性」「跟随」
       * 「多选复制」五件事，一句话装不下；Tooltip 在触摸设备上还展不开。 */}
      <PageHeader
        title={t('logs.title')}
        help={(
          <PageHelp
            testId="logs-help"
            title={t('logs.helpTitle')}
            intro={t('logs.helpIntro')}
            sections={[
              {
                title: t('logs.helpViewsTitle'),
                body: t('logs.helpViewsIntro'),
                items: [
                  t('logs.helpViewNodeInstance'),
                  t('logs.helpViewPlatform'),
                  t('logs.helpViewAll'),
                ],
              },
              {
                title: t('logs.helpSourceTitle'),
                body: t('logs.helpSourceIntro'),
                items: [
                  t('logs.helpSourceInstance'),
                  t('logs.helpSourceWorker'),
                  t('logs.helpSourceCp'),
                ],
              },
              {
                title: t('logs.helpCoverageTitle'),
                body: t('logs.helpCoverageBody'),
              },
              {
                title: t('logs.helpFollowTitle'),
                body: t('logs.helpFollowBody'),
              },
              {
                title: t('logs.helpShortcutTitle'),
                body: t('logs.helpShortcutBody'),
              },
              {
                title: t('logs.helpQueryTitle'),
                body: t('logs.helpQueryBody'),
              },
            ]}
          />
        )}
        actions={(
          <>
            {/* 来源做标（原「联邦」「Legacy」徽标）已删。
             *
             * 「联邦」「Legacy」是内部架构词，用户看不懂它们的含义，只知道“头上多了一个徒标”。
             * 真正的信息已经由页面本身体现：查的是实时日志还是历史日志、覆盖完不完整，
             * 都在下方覆盖横幅与空态提示里说清楚了。徒标只增加噪音。 */}
            {(federationDegraded || federationNotReady) && (
              <span
                data-testid="logs-not-ready-hint"
                className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground"
                // 与横幅同源区分：只有引擎未启用才给「去启用」指引；接口不可达时
                // 让用户去开启是无效指令，只说可查范围与后续办法。
                title={
                  federationNotReady
                    ? t('logsFederation.degradedHint')
                    : t('logsFederation.degradedUnavailableHint')
                }
              >
                <Info className="size-3.5 shrink-0" />
                <span className="truncate">
                  {federationNotReady
                    ? t('logsFederation.degradedTitleShort')
                    : t('logsFederation.degradedUnavailableTitleShort')}
                </span>
              </span>
            )}
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
                    onSelect={() => void handleExport(scope)}
                  >
                    {t(`logs.exportScope_${scope}`)}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
            {exporting && (
              <Button size="sm" variant="outline" onClick={() => exportRequest.current?.abort()}>{t('common.cancel')}</Button>
            )}
          </>
        )}
      />
      <LogExportRangeDialog
        key={`${scopeKey}|${exportRangeOpen}`}
        open={exportRangeOpen}
        from={scope.from}
        to={scope.to}
        pending={exporting}
        onOpenChange={(open) => { if (!open) exportRequest.current?.abort(); setExportRangeOpen(open) }}
        onConfirm={(bounds) => void handleExport('range', bounds)}
      />

      <div
        className="jm-toolbar-surface flex flex-wrap gap-1 p-1"
        role="tablist"
        aria-label={t('logs.viewLabel')}
      >
        {LOG_VIEWS.filter(
          // 非平台管理员只能看 node_instance（联邦查询按实例范围收敛）。
          (candidate) => isPlatformAdmin || candidate === 'node_instance',
        ).map(
          (candidate) => (
            <Button
              key={candidate}
              variant={view === candidate ? 'default' : 'ghost'}
              size="sm"
              role="tab"
              aria-selected={view === candidate}
              onClick={() => {
                switchView(candidate)
              }}
            >
              {t(`logs.view_${candidate}`)}
            </Button>
          ),
        )}
      </div>

      {textKind ? <TextLogWorkbench key={textKind} kind={textKind} nodeId={nodeId} nodes={nodes ?? []} onNodeChange={(value) => resetTo(setNodeId)(value)} sourceControl={sourceControl} /> : <>
      {/* 洞察卡片区：表格之外的顶部。
       *
       * 为何抬到这里而不是留在表格工具栏里：原来它们挤在一行与筛选控件争宽，
       * 用户看到的是「本次观测到 930 个事件（非精确总量）· 覆盖不完整 · 质量：EXACT / PARTIAL」
       * 这串读不懂的徒标，连带着把日志面板顶满。拆成卡片后，每块只回答一个问题，
       * 且表格区只剩日志本身。
       *
       * 跟随态不显示聚合内容，但保留固定高度槽位：聚合数字每 3 秒就陈旧，
       * 同时避免跟随切换时把日志窗口整体向上/向下推。 */}
      <div
        data-testid="logs-insights-slot"
        className="grid h-[208px] shrink-0 grid-cols-2 auto-rows-[104px] gap-2 overflow-x-auto overflow-y-hidden xl:h-[104px] xl:grid-cols-4"
      >
        {!follow && insightsVisible ? (
          <div data-testid="logs-federation-insights" className="contents">
            <div className="min-w-0" data-testid="logs-insight-cell-total"><EventsTotalCard
              count={statsCount}
              exact={statsSummary.exact}
              pageRows={federationQuery.isPlaceholderData ? undefined : items.length}
              levelCount={levelFacets.length}
              streamCount={streamItems.length}
              bucketCount={timeDistribution.buckets.length}
              partial={statsSummary.partial}
              pending={statsSummary.pending}
              truncated={statsSummary.truncated}
              failed={federationStats.isError}
              quality={statsSummary.quality}
              summary={
                federationFacets.isError
                  ? t('logs.vizSummaryFacetsFailed')
                  : !facetsSummary.exact
                    ? `${t('logs.aggregateFacets')} · ${t(facetsSummary.partial ? 'logs.aggregatePartial' : facetsSummary.pending ? 'logs.aggregatePending' : 'logs.aggregateUnverified')}`
                    : facetsTruncated
                      ? `${t('logs.aggregateFacets')} · ${t('logs.aggregateTruncated')}`
                      : undefined
              }
            /></div>
            <div className="min-w-0" data-testid="logs-insight-cell-level"><LevelDistributionCard
              levels={levelFacets.map((facet) => ({ level: facet.value, count: facet.count }))}
              uncertain={!facetsSummary.exact || facetsTruncated}
              failed={federationFacets.isError}
            /></div>
            <div className="min-w-0" data-testid="logs-insight-cell-sources"><TopSourcesCard
              items={streamItems}
              lookup={{ instances, nodes }}
              countsReliable={streamsComplete}
              failed={federationStreams.isError}
            /></div>
            <div className="min-w-0" data-testid="logs-insight-cell-time"><TimeDistributionCard
              buckets={timeDistribution.buckets}
              invalid={timeDistribution.invalid}
              failed={federationStats.isError}
              uncertain={!statsSummary.exact}
            /></div>
          </div>
        ) : null}
      </div>

      {/* 查询、来源、字段分别成行，预算与筛选同区，日志面板独立滚动。 */}
      <div className="jm-toolbar-surface flex min-w-0 shrink-0 flex-col gap-2 p-2" data-testid="logs-toolbar">
        <div className="flex min-w-0 items-center gap-2" data-testid="logs-query-row">
        <div className="flex h-9 min-w-0 flex-1 items-center gap-1" data-testid="logs-query-controls">
          {advancedAllowed && (
            <Button
              type="button"
              variant={activeAdvanced ? 'default' : 'outline'}
              size="sm"
              className="w-16 shrink-0"
              data-testid="logs-advanced-toggle"
              aria-pressed={activeAdvanced}
              onClick={toggleAdvancedQuery}
            >
              LogsQL
            </Button>
          )}
          <Input
            data-testid="logs-query-input"
            value={activeAdvanced ? advancedQuery : keyword}
            onChange={(event) => {
              exportRequest.current?.abort()
              if (activeAdvanced) setAdvancedQuery(event.target.value)
              else setKeyword(event.target.value)
            }}
            onKeyDown={(event) => {
              if (activeAdvanced && event.key === 'Enter' && !event.nativeEvent.isComposing) {
                event.preventDefault()
                applyAdvancedQuery()
              }
            }}
            aria-label={t(activeAdvanced ? 'logs.advancedLabel' : 'logs.searchPlaceholder')}
            aria-invalid={!!queryInputError}
            placeholder={t(activeAdvanced ? 'logs.advancedPlaceholder' : 'logs.searchPlaceholder')}
            className={cn('h-9 min-w-0 flex-1', activeAdvanced && 'font-mono')}
          />
          <Button
            type="button"
            variant="outline"
            size="sm"
            className={cn('shrink-0', !activeAdvanced && 'hidden')}
            data-testid="logs-query-apply"
            disabled={!activeAdvanced || !!queryInputError}
            onClick={applyAdvancedQuery}
          >
            {t('logs.queryApply')}
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className={cn('shrink-0', !federationQuery.isFetching && 'hidden')}
            disabled={!federationQuery.isFetching}
            data-testid="logs-query-cancel"
            aria-label={t('logs.queryCancel')}
            onClick={cancelQuery}
          >
            {t('common.cancel')}
          </Button>
        </div>
        <Select
          value={range}
          onValueChange={(v: string) => {
            setRangeAnchor(Date.now())
            setFollowAnchorMs(bucketFloor(Date.now()))
            resetTo(setRange)(v as TimeRangePreset)
          }}
        >
          {/* data-testid 供 e2e 定位：默认窗与选项文案都会漂移（见 e2e/navigation-benchmark.spec.ts），
              按 testid 定位才不会被「最近 15 分钟 / 最近 24 小时」这类文案变化打红。 */}
          <SelectTrigger size="sm" className="w-32 shrink-0" data-testid="logs-range-select">
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
        <div className="flex shrink-0 items-center gap-1">
          <Button
            type="button" size="sm" variant={follow ? 'default' : 'outline'}
            className="shrink-0" data-testid="logs-live-toggle" aria-pressed={follow}
            onClick={() => {
              exportRequest.current?.abort()
              setRangeAnchor(Date.now())
              setFollowAnchorMs(bucketFloor(Date.now()))
              setManualPaused(false)
              setInteractionPaused(false)
              setFollow((value) => !value)
              setPage(1)
              setFederationCursors({})
            }}
            disabled={platformView}
          >
            <Radio className={cn('size-3.5', follow && !manualPaused && !interactionPaused && 'animate-pulse')} />
            {t(follow ? 'logsLive.stop' : 'logs.follow')}
          </Button>
          <Button type="button" size="sm" variant="outline" className="shrink-0" data-testid="logs-live-pause" disabled={!follow} aria-pressed={manualPaused || interactionPaused}
            onClick={(event) => {
              if (!manualPaused && !interactionPaused) { setManualPaused(true); return }
              const viewport = event.currentTarget.closest('[data-page="logs"]')?.querySelector<HTMLElement>('[data-log-viewport]')
              const selection = document.getSelection()
              if (viewport && selection && ((selection.anchorNode && viewport.contains(selection.anchorNode)) || (selection.focusNode && viewport.contains(selection.focusNode)))) {
                selection.removeAllRanges()
              }
              if (viewport) viewport.scrollTop = 0
              setManualPaused(false)
              setInteractionPaused(false)
            }}>
            {t(manualPaused || interactionPaused ? 'logsLive.resume' : 'logsLive.pause')}
          </Button>
        </div>
        </div>
        <div className="flex min-w-0 flex-wrap items-center gap-2" data-testid="logs-scope-row">
        <div className="grid min-w-0 flex-1 grid-cols-3 gap-2">
        <div className="min-w-0 [&>button]:w-full">{sourceControl}</div>
        {/* 节点/实例选择器始终占位（platform 视图下禁用），避免切换视图时工具栏元素增减导致布局位移。 */}
        <Select
            disabled={view === 'platform'}
            value={nodeId === null ? SENTINEL_ALL : String(nodeId)}
            onValueChange={(v: string) =>
              resetTo(setNodeId)(v === SENTINEL_ALL ? null : Number(v))
            }
          >
            <SelectTrigger size="sm" className="min-w-0 w-full" data-testid="logs-node-select">
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
            <SelectTrigger size="sm" className="min-w-0 w-full" data-testid="logs-instance-select">
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
        </div>
        <div className="flex shrink-0 items-center gap-1" data-testid="logs-level-filters">
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
        </div>
        <div className="flex min-h-8 min-w-0 flex-wrap items-center gap-2 border-t border-border/60 pt-2" data-testid="logs-exploration-row">
        {/* VL 字段与流探索：只在实例/联邦查询能力可用时有内容，平台/节点文本模式不展示。 */}
        <div className="flex items-center gap-1" data-testid="logs-vl-exploration">
          <LogFieldExplorer
            params={aggregateParams}
            enabled={aggregateReady && !queryDraftDirty}
            fields={availableFields}
            onFilter={appendStructuredFilter}
            fieldsStatus={fieldDiscoveryStatus}
            onRetryFields={() => { void federationFields.refetch() }}
          />
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="w-28"
                disabled={platformView || availableFields.length === 0}
                data-testid="logs-fields-trigger"
              >
                {t('logs.fields')} {availableFields.length > 0 ? `(${availableFields.length})` : ''}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="max-h-80 w-72 overflow-y-auto">
              {availableFields.filter(isSafeLogsField).slice(0, 100).map((field) => (
                <DropdownMenuItem
                  key={field}
                  data-testid="logs-field-option"
                  onSelect={() => appendStructuredFilter(`${/[.-]/.test(field) ? JSON.stringify(field) : field}:*`)}
                >
                  {field}
                </DropdownMenuItem>
              ))}
              {availableFields.length === 0 && (
                <DropdownMenuItem disabled>{t('logs.fieldsUnavailable')}</DropdownMenuItem>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
          <Select
            disabled={platformView || availableStreamFields.length === 0}
            value={streamField}
            onValueChange={(value: string) => setStreamField(value as 'stream' | 'log_source_id')}
          >
            <SelectTrigger size="sm" className="w-32" data-testid="logs-stream-field-select">
              <SelectValue placeholder={t('logs.streamField')} />
            </SelectTrigger>
            <SelectContent>
              {availableStreamFields.map((field) => (
                <SelectItem key={field} value={field}>{field}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={platformView || availableStreamValues.length === 0}
                data-testid="logs-stream-values-trigger"
              >
                {t('logs.streamValues')}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="max-h-80 w-72 overflow-y-auto">
              {availableStreamValues.slice(0, 100).map((item) => (
                <DropdownMenuItem
                  key={item.value}
                  data-testid="logs-stream-value-option"
                  onSelect={() => appendStructuredFilter(`${streamField}:"${escapeLogsQLString(item.value)}"`)}
                >
                  <span className="truncate">{item.value}</span>
                  <span className="ml-auto tabular-nums text-muted-foreground">{item.count}</span>
                </DropdownMenuItem>
              ))}
              {availableStreamValues.length === 0 && (
                <DropdownMenuItem disabled>{t('logs.streamValuesUnavailable')}</DropdownMenuItem>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
        {queryMetaLabel && (
          <span data-testid="logs-query-meta" className="ml-auto min-w-0 flex-1 text-right text-[11px] leading-4 tabular-nums text-muted-foreground">
            {queryMetaLabel}
          </span>
        )}
        </div>
      </div>


      <div
        data-testid="logs-coverage-slot"
        className="flex h-11 shrink-0 items-center gap-2 overflow-hidden"
      >
        {follow ? (
          <>
            <div data-testid="logs-live-status" data-state={liveState.status} role={liveState.status === 'error' ? 'alert' : 'status'} className="min-w-0 flex-1 truncate text-xs text-muted-foreground"
              title={`${t('logsLive.limitedReplay')}${liveState.message ? ` · ${liveState.message}` : ''}`}>
              {t(`logsLive.${liveState.status}`)} · {t('logsLive.limitedReplay')}{liveState.code ? ` · ${liveState.code}` : ''}
              {liveState.message ? ` · ${liveState.message}` : ''}
            </div>
            {pollingFallback && federation && (
              <div className="max-h-11 min-w-0 flex-1 overflow-y-auto">
                <CoverageBanner federation={federation} onAction={handleFederationAction} qualityGuidanceKey={qualityGuidanceKey} onRetry={() => { void federationQuery.refetch() }} />
              </div>
            )}
          </>
        ) : queryInputError || queryFailure ? (
          <p role="alert" data-testid="logs-query-error" className="min-w-0 truncate text-xs text-destructive" title={queryInputError ?? queryFailure}>
            {queryInputError ?? queryFailure}
          </p>
        ) : queryCancelled ? (
          <p role="status" data-testid="logs-query-cancelled" className="text-xs text-muted-foreground">{t('logs.queryCancelled')}</p>
        ) : federation ? (
          <CoverageBanner
            federation={federation}
            onAction={handleFederationAction}
            qualityGuidanceKey={qualityGuidanceKey}
            onRetry={() => {
              void federationQuery.refetch()
            }}
          />
        ) : null}
      </div>

      <Panel
        className="min-h-0 flex-1"
        bodyClassName="flex min-h-0 flex-col p-0"
      >
        {isError ? (
          <div
            data-testid="logs-panel-error"
            className="flex min-h-0 flex-1 items-center justify-center px-6 text-center text-destructive"
          >
            {t('logs.loadError')}
          </div>
        ) : (
          <LogTimeline
            items={items}
            total={total}
            follow={follow && !manualPaused && !interactionPaused}
            filterKey={filterKey}
            onInteractionPauseChange={setInteractionPaused}
            loading={isLoading || (!follow && federationQuery.isPlaceholderData)}
            emptySuccessAllowed={!follow && emptySuccessAllowed}
            emptyReason={emptyReason}
            emptyHint={follow ? t('logsLive.waiting') : emptyHint}
            highlightKeyword={keyword}
            sourceLabelOf={sourceLabelOf}
          />
        )}
        <LogFooter
          follow={follow}
          platformTail={platformView}
          total={total}
          page={follow ? 1 : page}
          totalPages={totalPages}
          canNext={federationActive ? federationNextAvailable : undefined}
          onPrev={() => setPage((p) => Math.max(1, p - 1))}
          onNext={() => {
            if (federationActive) {
              if (!federationNextAvailable) return
              const next = federationCanContinue
                ? {
                    from: federationParams.from as string | undefined,
                    to: federationParams.to as string | undefined,
                    viewId: federation?.response.viewId,
                    cursor: federation?.response.nextCursor ?? undefined,
                  }
                : windowForAnchor(range, federationParams.from as string)
              setFederationCursors((current) => ({ ...current, [page + 1]: next }))
            }
            setPage((p) => p + 1)
          }}
        />
      </Panel>
      </>}
    </PageShell>
  )
}

/**
 * 覆盖/失败态横幅：消费 foundation 的 classify → toCoverageBannerProps。
 * visible=false 时不渲染；tone 驱动 token 配色；导出被阻断时展示原因。
 */
function CoverageBanner({
  federation,
  onRetry,
  onAction,
  qualityGuidanceKey,
}: {
  federation: LogsFederationResult
  onRetry: () => void
  onAction: (key: string) => void
  /**
   * 质量维度阻断导出时的自救引导 key（身份冲突 / 未对账重复）。
   * 未传表示当前不因质量维度阻断导出，不渲染额外提示。
   */
  qualityGuidanceKey?: string
}) {
  const { t } = useTranslation()
  const props: CoverageBannerProps = federation.banner
  const classified = federation.classified
  const sourceTag = federation.sourceTag
  const degraded = federation.degraded
  const federatedNotReady = federation.federatedNotReady
  // 质量标记与 foundation 同源，不另造一套：两边若漂移，会出现「横幅说有冲突、页面上看不到标记」
  // 的矛盾呈现。**必须分开读**：conflict 与 unresolved 是两个不同状态。
  const duplicateConflict = classified.coverage.duplicateConflict
  const duplicateUnresolved = classified.coverage.duplicateUnresolved
  // 不可达/未就绪始终展示横幅（即使 foundation 因 items 路径判定 visible=false）。
  const visible = props.visible || degraded || federatedNotReady
  if (!visible) return null

  // retry 有独立按钮与处理；其余可操作引导逐条落成按钮，避免计算了却不呈现。
  const guidanceActions = props.actionKeys.filter(
    (key) => key !== LOGS_FEDERATION_KEYS.actionRetry,
  )

  // 降级有两种来源，文案必须区分：
  // - federatedNotReady：联邦接口正常响应但引擎未启用 → 可给出启用指引；
  // - 纯 degraded：联邦接口本身不可达（404/未部署/连接失败）→ 不说「未启用」，
  //   否则会把「接口没部署」误导成「功能没开」，用户去开启也无效。
  const tone = degraded ? 'info' : federatedNotReady ? 'warning' : props.tone
  const title = degraded
    ? federatedNotReady
      ? t('logsFederation.federatedNotReady')
      : t('logsFederation.degradedUnavailable')
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
      // 质量维度独立于覆盖完整性：DOM 上分开暴露，测试/排障可核验「conflict 与
      // unresolved 没有被混成一个布尔」。
      data-quality-conflict={duplicateConflict ? 'true' : 'false'}
      data-quality-unresolved={duplicateUnresolved ? 'true' : 'false'}
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
      {/* 质量维度阻断导出时的自救引导：不能把用户留在「导出灰着、既不知道原因也没有出路」
          的禁用态。 */}
      {qualityGuidanceKey && (
        <p data-testid="logs-quality-guidance" className="text-xs opacity-90">
          {t(qualityGuidanceKey)}
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

/** 时间线底部条：跟随态显实时指示；否则显分页。 */
function LogFooter({
  follow,
  total,
  page,
  totalPages,
  canNext,
  onPrev,
  onNext,
  platformTail,
}: {
  follow: boolean
  total: number
  page: number
  totalPages: number
  /** 联邦 tail-first 模式的显式可翻性（服务端 next_cursor 驱动）；省略时回退页码比较。 */
  canNext?: boolean
  onPrev: () => void
  onNext: () => void
  /**
   * 平台视图：数据源是**日志文件尾部**（最近 N 行），没有服务端分页。
   *
   * 此时**不得**渲染上一页/下一页与“第 X / Y 页”——那些能力在文件上不存在。
   * 显示一个永远禁用的分页条并不是“降级”，而是让用户以为“再翻一页可能有更多”，
   * 与“诚实告知只有尾部”相矛盾。改为显式提示去看更早的记录该怎么做。
   */
  platformTail?: boolean
}) {
  const { t } = useTranslation()
  if (follow) {
    return (
      <div data-testid="logs-footer" className="flex h-10 shrink-0 items-center gap-2 border-t px-3 text-[11px] text-muted-foreground">
        <span className="size-1.5 rounded-full bg-muted-foreground" />
        {t('logsLive.window')}
        <span className="ml-auto">{t('logsLive.windowCount', { count: total })}</span>
      </div>
    )
  }
  if (platformTail) {
    return (
      <div data-testid="logs-footer" className="flex h-10 shrink-0 items-center gap-2 border-t px-3 text-[11px] text-muted-foreground">
        <span>{t('logs.platformTailHint', { count: total })}</span>
      </div>
    )
  }
  return (
    <div data-testid="logs-footer" className="flex h-10 shrink-0 items-center justify-between border-t px-3 text-sm text-muted-foreground">
      <span>{t('logs.totalCount', { count: total })}</span>
      <div className="flex items-center gap-2">
        <Button variant="outline" size="sm" disabled={page <= 1} onClick={onPrev}>
          {t('logs.prevPage')}
        </Button>
        <span>{t('logs.pageInfo', { page, totalPages })}</span>
        <Button
          variant="outline"
          size="sm"
          disabled={canNext === undefined ? page >= totalPages : !canNext}
          onClick={onNext}
        >
          {t('logs.nextPage')}
        </Button>
      </div>
    </div>
  )
}
