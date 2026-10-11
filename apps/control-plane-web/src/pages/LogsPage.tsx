import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { AlertTriangle, Download, Info, MoreHorizontal, Radio } from 'lucide-react'
import { useLegacyLogs, useLogs, exportLogs, type LogQueryParams } from '@/api/logs'
import {
	buildFederationResult,
	exportFederatedLogs,
	FederatedExportIncompleteError,
	isRateLimitError,
	rateLimitedEnvelope,
	useLogsFederation,
	useLogsFederationFacets,
	useLogsFederationStats,
	type LogsFederationResult,
} from '@/api/logFederation'
import { useAuthStore } from '@/stores/auth'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { ClearConflictsDialog } from '@/components/ClearConflictsDialog'
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
  buildExportParams,
  computeVirtualWindow,
  windowForAnchor,
  TIME_RANGE_PRESETS,
  LOG_VIEWS,
  type TimeRangePreset,
  type LogExportScope,
} from './logs-filters'

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
    return {
      id: typeof o.id === 'number' ? o.id : -(index + 1),
      source: normalizeSource(
        typeof o.source === 'string'
          ? o.source
          : typeof o.log_source_id === 'string'
            ? o.log_source_id
            : 'instance',
      ),
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
 * 把时刻向下取整到最近一个 FOLLOW_INTERVAL 桶（纯函数，便于单测）。
 *
 * 跟随态的时间窗锚点必须落在桶边界上：若直接取挂钟毫秒，`from` 每次渲染都变，
 * queryKey 随之变化，react-query 视为新查询 → 响应永远落不到地。
 */
function bucketFloor(ms: number): number {
  return Math.floor(ms / FOLLOW_INTERVAL) * FOLLOW_INTERVAL
}

/**
 * 日志中心（FR-049 查看 / FR-050 检索 / FR-150 增强 + FR-482 覆盖/失败态接线）。
 * 套「流水检索」范式：强筛选（级别 pill + 来源/节点/实例/时间范围 + 关键字）→ 时间线行（虚拟滚动）；
 * 「实时跟随」开关锁定首页并按间隔轮询（tail）；导出可选范围（当前页/全部匹配/时间段）。
 * 级别用 StatusBadge 着色，token 驱动，与告警页语义统一。
 *
 * FR-482 接线（最小改动）：
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
    // 默认 24h（2026-09-30 实测依据）：默认「全部时间」会让每个目标的全部分区参与查询——
    // 默认窗＝**15 分钟**（w21 快首屏 ✗）：搜索页首屏走 CP 快路径（Query.Fast ⇒ resolveForQueryFast，
    // 省每 Worker 一次 OpenView 往返 ＋ 未校验标记 Coverage.Pending ✓），窗口越小首屏越快；
    // 「24h/全部」等更大范围仍从时间范围筛选器显式选择（走慢路径与完整校验 ✓，绝不给假完整 ✗）。
    const [range, setRange] = useState<TimeRangePreset>('15m')
  const [page, setPage] = useState(1)
	// 页历史保存原窗口和服务端复合游标；只有窗口枚举结束后才允许向更早窗口移动。
	const [federationCursors, setFederationCursors] = useState<Record<number,
		Pick<LogQueryParams, 'from' | 'to'> & { viewId?: string; cursor?: string }
	>>({})
  // 「仅查在线节点」：显式开启后联邦扇出收缩到在线目标（FR-480 online_only）。
  const [onlineOnly, setOnlineOnly] = useState(false)
  const [follow, setFollow] = useState(false)
  const [exporting, setExporting] = useState(false)
  /**
   * 时间窗锚点：非跟随态下冻结，避免每次渲染 `new Date()` 使 useLogs /
   * useLogsFederation 的 queryKey 每毫秒变化导致请求风暴（FR-482 接线后暴露）。
   * 跟随态在渲染时取当前时间，配合 refetchInterval 滚动时间窗。
   */
  const [rangeAnchor, setRangeAnchor] = useState(() => Date.now())
  useEffect(() => {
    // 时间范围/跟随态变化时重置锚点：读取挂钟的「同步外部时间」语义，非渲染期纯计算。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setRangeAnchor(Date.now())
  }, [range, follow])

  // 跟随态时间桶：**由 interval 驱动的 state**，不再在渲染期取挂钟分桶。
  // 旧实现的渲染期分桶有双重触发：跨桶时 queryKey 变化触发一次请求，同时
  // refetchInterval 到点又触发一次 ⇒ 同一份数据成对双发（生产 cp.out 里同参数成对请求
  // 仅相差约 6ms 正是此）。改为「桶滚动＝唯一触发源」：跟随态下主查询
  // refetchInterval=false（见下），每个 tick 恰好一次请求。
  // 分桶仍然必要：`from` 逐毫秒变化会让 queryKey 每次渲染都变、响应永远落不到地。
  // 跟随态不下发 `to`（见 timeParamsFor）：时钟偏移下的未来时间戳事件仍可见。
  const [followAnchorMs, setFollowAnchorMs] = useState(() => bucketFloor(Date.now()))
  useEffect(() => {
    if (!follow) return
    const timer = window.setInterval(() => {
      // 只在真正跨桶时改变 state：同一桶内返回原值 → React 跳过重渲染 → 不发请求。
      setFollowAnchorMs((prev) => {
        const next = bucketFloor(Date.now())
        return next === prev ? prev : next
      })
    }, FOLLOW_INTERVAL)
    return () => window.clearInterval(timer)
  }, [follow])

  const timeParams = timeParamsFor(
    range,
    follow ? new Date(followAnchorMs) : new Date(rangeAnchor),
    follow,
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
  // 平台视图的权威来源是 CP 经典日志接口；联邦数据只覆盖节点/实例日志。
  const federationEnabled = !legacyMode && view !== 'platform'
	const legacyQuery = useLegacyLogs(params, legacyMode)
  // FR-482：可选联邦覆盖探测。与 useLogs 同筛选参数；API 404 → degraded legacy。
	const federationParams: Record<string, unknown> = {
		...params,
		limit: PAGE_SIZE,
		...(follow ? { tailMode: 'FOLLOW_LIVE' } : {}),
		...(onlineOnly ? { onlineOnly: true } : {}),
		// 续页不解析游标、不从日志时间重建窗口，保留纳秒同戳组和原视图身份。
		...(!follow && page > 1 ? federationCursors[page] : {}),
	}
  const federationQuery = useLogsFederation(federationParams, {
	enabled: federationEnabled,
	// 跟随态：时间桶 state（上）是唯一触发源，此处必须为 false，否则同拍双发。
	// `all` 预设没有 `from`（窗口无边界 ⇒ 分桶不改变 queryKey），此时无桶可滚，
	// 才退回 interval 作为那一次触发；两种情况都只有**一个**触发源。
	refetchInterval: follow && !timeParams.from ? FOLLOW_INTERVAL : false,
	})
  /**
   * 最近一份「联邦可用且带 items」的成功结果，用于限流期间继续渲染已有行。
   *
   * 用 state 而非 ref：渲染期要**读**它来决定列表内容，ref 读值不触发重渲染且违反
   * 「渲染期不得访问 ref」；state 才能让保留生效。
   * 只记非降级结果——降级（legacy 表格）的行来自经典接口，记进来会让限流回退时
   * 把经典表的行冒充成联邦结果。
   * 更新时机用「渲染期按需调整 state」而非 effect（React 官方推荐的派生 state 写法，
   * 也避免先渲染一帧空列表再把数据补回来的闪烁）。
   *
   * **必须携带筛选身份**：保留项若不含筛选键，切到新筛选后新请求持续 429（重试耗尽）时，
   * 列表会继续渲染**上一个筛选**的行（可能是另一个节点/实例）——筛选控件已显示新条件，
   * 内容却属于旧条件，运维会把 A 实例的日志读成 B 实例的。复用前必须比对 filterKey。
   */
  const [lastGoodFederation, setLastGoodFederation] = useState<{ filterKey: string; result: LogsFederationResult } | null>(null)
  // 「确认并清空身份冲突」对话框开关（质量标记的运维入口，见 ClearConflictsDialog）。
  const [clearConflictsOpen, setClearConflictsOpen] = useState(false)
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
  // 筛选身份键（view/实例/节点/级别/关键字/范围；**不含 page** ✗——翻页需保留滚动位置）。
  // 既驱动滚动复位（LogTimeline 的 filterKey effect），也是限流保留项的归属判据：
  // 保留项记的是这个键，筛选一变，旧保留项即判失效、不得冒充新筛选的结果。
  const filterKey = `${view}|${instanceId ?? ''}|${nodeId ?? ''}|${level}|${keyword.trim()}|${range}`
  const latestFederation = federationQuery.data
  /**
   * 「这一轮请求真的拿到结果了」——用于清除闩锁。
   *
   * 不含 degraded 条件：queryFn 只对 429 抛出，其它失败（404/连接失败）都会被转成信封、
   * 在 react-query 层面算成功。若限定「非降级才算成功」，那么「先 429、随后接口变成 404」
   * 时闩锁永远清不掉，限流横幅会盖住本该发生的 legacy 降级。
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
    && latestFederation
    && !latestFederation.degraded
    && Array.isArray(latestFederation.response.items)
    && lastGoodFederation?.result !== latestFederation
  ) {
    // 记录当时的筛选身份：切筛选后旧保留项即失效（见 retainedFederation 的复用条件）。
    setLastGoodFederation({ filterKey, result: latestFederation })
  }
  const federationRateLimited = rateLimited
  const federation = legacyMode && legacyQuery.data
    ? buildFederationResult({ sourceTag: 'legacy', items: legacyQuery.data.items, coverage: legacyQuery.data.coverage })
    : federationEnabled
      ? (federationRateLimited
        ? buildFederationResult(rateLimitedEnvelope(
          federationQuery.error instanceof Error ? federationQuery.error.message : undefined,
        ))
        : federationQuery.data)
      : undefined

  // F-003：联邦可用（非降级）时列表以 federation.items 为主数据源；
  // 404 降级才回退 useLogs 的 Legacy 表。禁止「联邦横幅 + Legacy 内容」错配。
  const federationActive = !!federation && !federation.degraded && federation.available
  // 供标题区状态提示使用（与横幅同源，避免两处判定漂移）。
  const federationDegraded = federation?.degraded === true
  const federationNotReady = federation?.federatedNotReady === true
  /**
   * 「确认并清空身份冲突」入口可用性。
   *
   * 三个条件缺一不可：
   * - 确有冲突（`duplicateUnresolved`，与横幅同源）—— 否则这个按钮没有任何可清的东西；
   * - 选中了单一节点 —— 清空是**按节点**下发的（Worker 持有该节点的源），
   *   未选节点时无法确定目标，此时不给入口，而不是默认拿第一个节点去清；
   * - 联邦不是降级态 —— 降级时质量标记来自 legacy 路径，本入口对其无效。
   */
  const canClearConflicts =
    federationActive &&
    nodeId !== null &&
    federation?.classified.coverage.duplicateUnresolved === true

  /**
   * 经典表格查询（联邦视图下作降级回退源）。
   *
   * 两个门控都指向同一目标：**联邦视图下不要为了「可能用得上」而重复请求**。
   * ① `refetchInterval` 不含联邦视图：node_instance/all 视图的主数据源是 federation，
   *    再让 useLogs 跟着每 3 秒打一条数据面请求，只会让每次轮询变成 2 条（生产 429 的
   *    主因之一），而它拉回的行在联邦活跃时根本不参与渲染。
   * ② `enabled` 只在「经典接口确实是权威来源」时才开：平台视图（federationEnabled=false）
   *    或联邦已降级（404/不可达）。仅关掉 ① 还不够——跟随态的时间桶每 3 秒滚动一次，
   *    `params` 里的 from/to 随之变化 ⇒ queryKey 变化本身就触发一次新请求（实测：即使
   *    refetchInterval=false，classic 仍每 tick 发 1 条）。关掉 ② 才真正消除这次重复。
   *    降级后才启用也意味着回退表按需加载，不会在联邦正常时白白预热一份没人看的表格。
   */
  const classicAuthoritative = !federationEnabled || federation?.degraded === true
  const { data, isLoading: classicLoading, isError: classicError, refetch: refetchLogs } = useLogs(params, {
    refetchInterval: follow && !federationEnabled ? FOLLOW_INTERVAL : false,
    enabled: !legacyMode && classicAuthoritative,
  })
	const aggregateParams: Record<string, unknown> = {
		...timeParams,
		...(view === 'all' && source ? { source } : {}),
		...(level ? { level } : {}),
		...(nodeId !== null ? { nodeId } : {}),
		...(instanceId !== null ? { instanceId } : {}),
		...(keyword.trim() ? { keyword: keyword.trim() } : {}),
		// 注意：**不得**把搜索的 viewId 带入聚合参数 ✗——stats/facets 是各自固定条件的独立查询，
		// 复用搜索视图会被 CP 按 F-006 语义拒（409 VIEW_STALE；实测 stats 与 facets 两呼皆 409 ✓）。
		// 去除后二者各自重建视图，服务端校验一字不动 ✗。
	}
	// Tail无永久位置字段：复用既有Search每30秒刷新证据，固定窗口避免每个3秒轮询桶重查。
	const followEvidenceQuery = useLogsFederation({
		...aggregateParams,
		...timeParamsFor(range, new Date(rangeAnchor), true),
		limit: 1,
		...(onlineOnly ? { onlineOnly: true } : {}),
	}, { enabled: federationEnabled && follow, refetchInterval: follow ? 30_000 : false })
	const ingestPositionGaps = follow
		? followEvidenceQuery.data?.response.ingestPositionGaps ?? []
		: federation?.response.ingestPositionGaps ?? []
	// 编排（§1 延后串行）：① 主列表先出——stats/facets 均已 gate 在「主查询有数据且拿到 viewId」之后 ✓；
	// ② stats → facets 再串行——facets 额外等 stats 落定（成功或失败），
	// 任一时刻最多一个聚合请求在飞 ⇒ 不再与主列表/彼此并发挤 VL 的 maxConcurrentRequests=2 两个槽 ✗。
	const federationStats = useLogsFederationStats(
		{ ...aggregateParams, groupBy: 'level' },
		federationActive && !follow && !!federation?.response.viewId,
	)
	const federationFacets = useLogsFederationFacets(
		{ ...aggregateParams, dimensions: 'level,stream', dimensionLimit: 8 },
		federationActive && !follow && !!federation?.response.viewId
			&& (federationStats.isSuccess || federationStats.isError),
	)
	// Stats 计数来自后端 `agg.count`（wire 真源）；兼容扁平 `count`。
	const statsCount = federationStats.data?.points?.reduce(
		(sum, point) => sum + Number(point.agg?.count ?? point.count ?? 0),
		0,
	)
	const levelFacets = federationFacets.data?.dimensions?.find((dimension) => dimension.dimension === 'level')?.values ?? []
	// 洞察栏是否有真实内容（决定是否挂 testid；占位容器本身始终渲染以稳定高度）。
	const insightsVisible = federationActive && (statsCount !== undefined || levelFacets.length > 0)
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

  const items = legacyMode ? (legacyQuery.data?.items ?? []) : federationActive ? (federationItems ?? []) : (data?.items ?? [])
  const total = legacyMode ? (legacyQuery.data?.total ?? 0) : federationActive ? items.length : (data?.total ?? 0)
  // 有游标时必须原 view 续页（预算截断空页也可续）；明确结束才移动完整窗口。
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
        setter(v)
        setPage(1)
        setFederationCursors({})
      },
    [],
  )

  // 导出门禁：联邦可用且覆盖/校验未过 → 禁用；404 降级保留经典导出路径。
  const classicExportAllowed = !federation || federation.degraded
  const exportBlockedByFederation =
    !!federation && !classicExportAllowed && !federation.exportAffordance.showDownload
  const exportDisabled = legacyMode || exporting || total === 0 || exportBlockedByFederation
	// federated 探测（FR-482）未落地前，items 可能暂时为空——此时不得判为「空结果」，
	// 否则会先闪 empty-state（且 404 降级期间显示非成功空态），把尚未就绪误报成失败。
	// 故把「联邦探测未完成」并入 loading：只有探测结束、数据源确定后才允许判空。
	//
	// 联邦视图下经典查询只是回退源，它的加载/失败**不得**参与页面态：联邦已拿到数据时
	// 白屏/报错都无意义地盖住列表；只有联邦降级（真正在用经典表）时才由它说话。
	const federationProbePending = federationEnabled && federationQuery.isLoading
	const isLoading = legacyMode
		? legacyQuery.isLoading
		: federationProbePending || (classicAuthoritative && classicLoading)
	const isError = legacyMode
		? legacyQuery.isError
		: classicAuthoritative && classicError
	const hasData = legacyMode
		? !!legacyQuery.data
		: classicAuthoritative
			? !!data
			// 联邦视图下「有数据」= 有联邦结果（含限流时保留的上一份行）。
			: !!federation
	// 空结果语义：失败/partial 不得呈「暂无日志」空成功（FR-482）。
	// 引擎未就绪的降级是例外中的例外：经典路径可能确实没有行，但引擎侧数据从未被查询，
	// 此时宣称「暂无日志」是假成功，必须保留非成功空态。
	// 限流（rate_limited）同样是失败态：emptySuccessAllowed 由 classified 判为 false。
	const emptySuccessAllowed =
		!federation ||
		(federation.degraded && !federation.federatedNotReady) ||
		federation.classified.emptySuccessAllowed

  const handleExport = async (scope: LogExportScope) => {
    if (exportDisabled) return
    setExporting(true)
    try {
		if (federationActive) {
			// 导出同样**不得复用过期视图** ✗：仅当当前响应确实带 viewId 时携带（视图来自最近一次查询 ⇒
			// 与当前条件同源 ✓）；否则省去 viewId，由 CP 按当前条件重建视图。
			await exportFederatedLogs({
				...buildExportParams(params, scope),
				...(federation.response.viewId ? { viewId: federation.response.viewId } : {}),
			})
		} else {
			await exportLogs(buildExportParams(params, scope))
		}
      toast.success(t('logs.exportStarted'))
    } catch (error) {
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
      setExporting(false)
    }
  }

  /**
   * 失败态可操作引导（FR-482 §3.1）：把 helper 给出的 actionKey 落成真实动作。
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

  return (
    <div data-page="logs" className="jm-page-stack flex h-full min-h-0 flex-col gap-4">
      <div className="jm-page-header">
        <div className="flex min-w-0 items-center gap-2">
          <h1 className="jm-page-title">{t('logs.title')}</h1>
          {/* 状态与提示固定在标题区：不占用列表空间，也不因状态变化推动下方布局。 */}
          {/* 徽标仅在确有来源标记时渲染：标题行是 flex，空占位只会让测试与读屏
              多拿到一个空节点，而它本身不承载高度，去掉不影响布局稳定。 */}
          {federation?.sourceTag && (
            <span data-testid="logs-source-tag" className="inline-flex shrink-0">
              <StatusBadge
                level={federation.sourceTag === 'legacy' ? 'warning' : 'info'}
                label={t(`logsFederation.sourceTag.${federation.sourceTag}`)}
              />
            </span>
          )}
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
				setFederationCursors({})
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
                  setFederationCursors({})
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
          {/* data-testid 供 e2e 定位：默认窗与选项文案都会漂移（见 e2e/navigation-benchmark.spec.ts），
              按 testid 定位才不会被「最近 15 分钟 / 最近 24 小时」这类文案变化打红。 */}
          <SelectTrigger size="sm" className="w-36" data-testid="logs-range-select">
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
            <SelectTrigger size="sm" className="w-32" data-testid="logs-source-select">
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
            <SelectTrigger size="sm" className="w-36" data-testid="logs-node-select">
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
			setFederationCursors({})
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
	      仅占位不显示任何文字，列表位置保持稳定。
	      占位容器不挂 testid —— 否则「等洞察栏出现」会立刻命中空容器，读到的
	      是尚未填充的空文本；挂 testid 的仍是有内容的那一份。 */}
	  {!follow && (
		<div
		  {...(insightsVisible ? { 'data-testid': 'logs-federation-insights' } : {})}
		  className="flex min-h-8 flex-wrap items-center gap-2 border-y px-1 py-1 text-xs text-muted-foreground"
		>
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

      {/* FR-482 覆盖/失败态横幅：partial/offline/failed 不得当空成功。 */}
      {federation && (
        <CoverageBanner
          federation={federation}
          onAction={handleFederationAction}
          onClearConflicts={canClearConflicts ? () => setClearConflictsOpen(true) : undefined}
          onRetry={() => {
			if (legacyMode) void legacyQuery.refetch()
			else { void federationQuery.refetch(); void refetchLogs() }
          }}
        />
      )}

      {/* 身份冲突人工清空（运维入口）：只在有冲突、且当前选中单一节点时可用。
          未选中单一节点时无法确定要清哪个节点，故不给入口（而不是默认清第一个）。 */}
      {canClearConflicts && (
        <ClearConflictsDialog
          nodeId={nodeId}
          open={clearConflictsOpen}
          onOpenChange={setClearConflictsOpen}
          onCleared={() => {
            if (legacyMode) void legacyQuery.refetch()
            else { void federationQuery.refetch(); void refetchLogs() }
          }}
        />
      )}

      {/* 永久位置凭据独立于时间覆盖；空items也必须可见，来源未知时不推测归属。 */}
      {federationActive && ingestPositionGaps.length > 0 && (
        <section data-testid="logs-ingest-position-gaps" role="status"
          className="max-h-48 shrink-0 overflow-auto rounded-md border border-status-warning/30 bg-status-warning/10 px-3 py-2 text-sm">
          <h2 className="flex items-center gap-1.5 font-medium">
            <AlertTriangle className="size-4 shrink-0" />
            {t('logsFederation.positionGaps.title', { count: ingestPositionGaps.length })}
          </h2>
          <p className="text-xs text-muted-foreground">{t('logsFederation.positionGaps.description')}</p>
          <ul className="mt-1.5 space-y-1.5">
            {ingestPositionGaps.map((gap, index) => (
              <li key={index} data-testid="logs-ingest-position-gap" className="break-all text-xs">
                <p>{t('logsFederation.positionGaps.identity', {
                  worker: gap.workerId, source: gap.logSourceId ?? t('logsFederation.positionGaps.unknown'),
                  generation: gap.sourceGeneration ?? t('logsFederation.positionGaps.unknown'),
                })}</p>
                <p>{t('logsFederation.positionGaps.evidence', {
                  namespace: gap.storageNamespace, from: gap.from, to: gap.to, reason: gap.reasonCode,
                  operator: gap.operator, at: gap.atUTC,
                })}</p>
              </li>
            ))}
          </ul>
        </section>
      )}

      {isLoading && !hasData ? (
        <div className="flex flex-col gap-1">
          <p className="text-muted-foreground">{t('common.loading')}</p>
          <SlowQueryHint />
        </div>
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
            filterKey={filterKey}
            // 切窗口/筛选期间展示的是上一份数据（placeholderData）：此时 isLoading 为 false
            // 但新结果尚未到达，须维持 loading 语义（timeline 据此不把旧数据误判为空态）；
            // follow 轮询每 3 秒换桶，不置位以免闪烁。
            loading={isLoading || (!follow && federationQuery.isPlaceholderData)}
            emptySuccessAllowed={emptySuccessAllowed}
          />
          <LogFooter
            follow={follow}
            total={total}
            page={follow ? 1 : page}
            totalPages={totalPages}
            canNext={federationActive ? federationNextAvailable : undefined}
            onPrev={() => setPage((p) => Math.max(1, p - 1))}
			onNext={() => {
				if (federationActive) {
					if (!federationNextAvailable) return
					const next = federationCanContinue
						? { from: federationParams.from as string | undefined,
							to: federationParams.to as string | undefined,
							viewId: federation?.response.viewId, cursor: federation?.response.nextCursor ?? undefined }
						: windowForAnchor(range, federationParams.from as string)
					setFederationCursors((current) => ({ ...current, [page + 1]: next }))
				}
				setPage((p) => p + 1)
			}}
          />
        </Panel>
      )}
    </div>
  )
}

/**
 * 覆盖/失败态横幅（FR-482）：消费 foundation 的 classify → toCoverageBannerProps。
 * visible=false 时不渲染；tone 驱动 token 配色；导出被阻断时展示原因。
 */
function CoverageBanner({
  federation,
  onRetry,
  onAction,
  onClearConflicts,
}: {
  federation: LogsFederationResult
  onRetry: () => void
  onAction: (key: string) => void
  /** 存在"身份冲突待确认"时提供运维入口；未传则该入口不渲染。 */
  onClearConflicts?: () => void
}) {
  const { t } = useTranslation()
  const props: CoverageBannerProps = federation.banner
  const classified = federation.classified
  const sourceTag = federation.sourceTag
  const degraded = federation.degraded
  const federatedNotReady = federation.federatedNotReady
  // 「有重复日志待确认」的判据与 foundation 同源（coverage.duplicateUnresolved），
  // 不另造一套：两边若漂移，会出现「横幅说有冲突、按钮却不见」或反之的矛盾呈现。
  const duplicateUnresolved = classified.coverage.duplicateUnresolved
  // 404 降级始终展示横幅（即使 foundation 因 items 路径判定 visible=false）。
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
      {(props.showRetry || guidanceActions.length > 0 || (duplicateUnresolved && onClearConflicts)) && !degraded && (
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
          {/* 身份冲突人工清空：只在确有冲突时出现（否则这个按钮没有任何可清的东西）。
              它改的是查询面语义（标记消失），故走二次确认 + 必填理由（见 ClearConflictsDialog）。 */}
          {duplicateUnresolved && onClearConflicts && (
            <Button
              size="sm"
              variant="destructive"
              data-testid="logs-clear-conflicts-open"
              onClick={onClearConflicts}
            >
              {t('logsFederation.clearConflicts.button')}
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

/** 级别快速筛选 pill：选中态主色淡染，非选中态弱色；带级别时前导状态色点。 */
/** 慢查询提示出现的等待阈值（秒）：3 秒内的加载属正常往返，不必打扰用户。 */
const SLOW_QUERY_HINT_SECONDS = 3

/**
 * 慢查询提示：长耗时查询不能只留一个转圈——让用户知道「还在跑、等了多久」，
 * 并提示缩小时间范围（无界范围是最慢路径）。纯前端计时，不改任何查询语义。
 *
 * 计时的归零交给组件生命周期：本组件只在「加载中且尚无数据」时挂载，加载结束即卸载，
 * 秒数随之丢弃。不在 effect 体内同步 setState(0) —— 那会触发级联渲染
 * （react-hooks/set-state-in-effect），且在严格模式下被双调用放大。
 */
function SlowQueryHint() {
  const { t } = useTranslation()
  const [waitedSeconds, setWaitedSeconds] = useState(0)
  useEffect(() => {
    const startedAt = Date.now()
    const timer = window.setInterval(() => {
      setWaitedSeconds(Math.floor((Date.now() - startedAt) / 1000))
    }, 1000)
    return () => window.clearInterval(timer)
  }, [])
  if (waitedSeconds < SLOW_QUERY_HINT_SECONDS) return null
  return (
    <p className="text-xs text-muted-foreground" data-testid="logs-slow-query-hint">
      {t('logs.slowQueryHint', { seconds: waitedSeconds })}
    </p>
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

/**
 * 日志时间线（虚拟滚动）：固定行高，仅渲染视口附近的窗口，千行级日志不一次性入 DOM。
 * 跟随态下每次数据更新自动滚到顶部（最新在前）。
 */
function LogTimeline({
  items,
  total,
  follow,
  filterKey,
  loading = false,
  emptySuccessAllowed = true,
}: {
  items: import('@/api/logs').LogEntry[]
  total: number
  follow: boolean
  /**
   * 筛选身份键（view/实例/节点/级别/关键字/范围；**不含 page** ✗——翻页需保留滚动位置）。
   * 变化即重置滚动：切实例后新列表更短时，旧 scrollTop 会落在空白区，visible 为空 ⇒
   * 表现为「点什么都不动/刷不出来」✗✓。
   */
  filterKey: string
  /** 查询尚未完成：此时不判空态，避免把「未就绪」误报为空结果。 */
  loading?: boolean
  /** FR-482：false 时禁止把空列表呈成「无日志」成功空态。 */
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

  // 筛选变化（非翻页）⇒ 无条件重置滚动到顶部：避免旧偏移落在新列表的空白区 ✗✓。
  useEffect(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = 0
    }
    // 筛选变化需同步把虚拟窗口起点复位（与上面 DOM 的 scrollTop 同一次提交内完成，
    // 否则窗口仍停在旧 startIndex、首帧渲染错位）——这是「外部系统同步」而非派生 state。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setScrollTop(0)
  }, [filterKey])

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
  canNext,
  onPrev,
  onNext,
}: {
  follow: boolean
  total: number
  page: number
  totalPages: number
  /** 联邦 tail-first 模式的显式可翻性（服务端 next_cursor 驱动）；省略时回退页码比较（legacy）。 */
  canNext?: boolean
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
