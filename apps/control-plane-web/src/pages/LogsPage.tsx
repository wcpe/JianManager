import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { AlertTriangle, Download, Info, Radio } from 'lucide-react'
import { type LogQueryParams } from '@/api/logs'
import {
	buildFederationResult,
  describeLogAggregate,
  isLogRequestCanceled,
	exportFederatedLogs,
	FederatedExportIncompleteError,
	isRateLimitError,
	rateLimitedEnvelope,
	useLogsFederation,
	useLogsFederationFacets,
	useLogsFederationStats,
	useLogsFederationStreams,
	type LogsFederationResult,
} from '@/api/logFederation'
import { useAuthStore } from '@/stores/auth'
import { usePlatformDiagnosticsLog } from '@/api/nodeDiagnostics'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { LogExportRangeDialog } from '@/components/logs/LogExportRangeDialog'
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
  computeVirtualWindow,
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

/**
 * 从一行平台日志文本里推断级别。
 *
 * 为什么要在前端推断：平台日志现在是**纯文本文件**，不是结构化行。
 * slog 两种格式都在行内带级别：
 *   - text：`time=... level=WARN msg=...`
 *   - json：`{"time":"...","level":"WARN","msg":"..."}`
 * 两者都没命中时归 info（与后端 log_level.go 的推断口径一致）。
 */
function inferLineLevel(line: string): string {
  const m = /level[=":]+\s*"?(DEBUG|INFO|WARN|WARNING|ERROR)"?/i.exec(line)
  const v = m?.[1]?.toUpperCase()
  if (!v) return 'info'
  if (v === 'WARNING') return 'warn'
  return v.toLowerCase()
}

/**
 * 从一行平台日志文本里取时间（用于排序与展示）。
 *
 * 取不到就返回空串——不伪造当前时间：那会让排序看起来正常、实际是错的。
 */
function inferLineTime(line: string): string {
  // json 格式："time":"2026-10-29T15:04:32+08:00"
  const json = /"time"\s*:\s*"([^"]+)"/.exec(line)?.[1]
  if (json) return json
  // text 格式：time=2026-10-29T15:04:32.000+08:00
  const text = /\btime=(\S+)/.exec(line)?.[1]
  if (text) return text
  return ''
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
 * 日志中心（查看 / 检索 / 增强 + 覆盖/失败态接线）。
 * 套「流水检索」范式：强筛选（级别 pill + 来源/节点/实例/时间范围 + 关键字）→ 时间线行（虚拟滚动）；
 * 「实时跟随」开关锁定首页并按间隔轮询（tail）；导出可选范围（当前页/全部匹配/时间段）。
 * 级别用 StatusBadge 着色，token 驱动，与告警页语义统一。
 *
 * 联邦覆盖接线（最小改动）：
 * - 可选联邦 API（`/logs/federation`）经 `useLogsFederation` 探测；不可用时**不回退**经典表
 *   （用户裁定：「没有就是没有」，以免把平台/历史日志读成该视图的日志），只出横幅指明去看
 *   Legacy 只读入口。详见下方 `classicAuthoritative` 一段。
 * - 覆盖/失败态一律经 `classifyLogViewState` + `toCoverageBannerProps`，失败/partial 禁止空成功。
 * - `sourceTag=legacy` 打 Legacy 徽标；ErrFederatedNotReady 显示联邦未就绪说明。
 * - 导出在 `resolveExportDownloadAffordance` 阻断时禁用；经典 `/logs/export` 仅在平台视图放行。
 */
export default function LogsPage() {
  const { t } = useTranslation()
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
  const [source, setSource] = useState('')
  const [level, setLevel] = useState('')
  const [nodeId, setNodeId] = useState<number | null>(null)
  // 深链预筛选：从 URL ?instanceId= 初始化实例筛选（如从终端「查看完整历史」进入）。
  const [instanceId, setInstanceId] = useState<number | null>(() => {
    const q = searchParams.get('instanceId')
    const n = q ? Number(q) : NaN
    return Number.isFinite(n) && n > 0 ? n : null
  })
  const [keyword, setKeyword] = useState('')
  const debouncedKeyword = useDebounced(keyword.trim(), 250)
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
  const scope = normalizeLogScope({
    view,
    ...(view === 'all' && source ? { source } : {}),
    level,
    ...(nodeId !== null ? { nodeId } : {}),
    ...(instanceId !== null ? { instanceId } : {}),
    keyword: debouncedKeyword,
    ...(view !== 'platform' && onlineOnly ? { onlineOnly: true } : {}),
    ...timeParams,
    ...(!follow && page > 1 ? { from: federationCursors[page]?.from, to: federationCursors[page]?.to } : {}),
  })
  const params: LogQueryParams = { ...scope, page: follow ? 1 : page, pageSize: PAGE_SIZE }
  const scopeKey = logScopeKey(scope)

  // 平台视图读进程日志文件尾部；联邦数据覆盖节点/实例日志。
  const federationEnabled = view !== 'platform'
  // 可选联邦覆盖探测。与 useLogs 同筛选参数；API 404 → degraded legacy。
	const federationParams: Record<string, unknown> = {
		...params,
		limit: PAGE_SIZE,
		...(follow ? { tailMode: 'FOLLOW_LIVE' } : {}),
		...(onlineOnly ? { onlineOnly: true } : {}),
		// 续页不解析游标、不从日志时间重建窗口，保留纳秒同戳组和原视图身份。
		...(!follow && page > 1 ? federationCursors[page] : {}),
	}
  const federationQuery = useLogsFederation(federationParams, { enabled: federationEnabled })
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


  /**
   * 经典表格查询（**仅**平台视图的权威来源）。
   *
   * 两个门控都指向同一目标：**联邦视图下不要为了「可能用得上」而重复请求**。
   * ① `refetchInterval` 不含联邦视图：node_instance/all 视图的主数据源是 federation，
   *    再让 useLogs 跟着每 3 秒打一条数据面请求，只会让每次轮询变成 2 条（生产 429 的
   *    主因之一），而它拉回的行在联邦活跃时根本不参与渲染。
   * ② `enabled` 只在「经典接口确实是权威来源」时才开：**只有平台视图**
   *    （federationEnabled=false）符合。仅关掉 ① 还不够——跟随态的时间桶每 3 秒滚动一次，
   *    `params` 里的 from/to 随之变化 ⇒ queryKey 变化本身就触发一次新请求（实测：即使
   *    refetchInterval=false，classic 仍每 tick 发 1 条）。关掉 ② 才真正消除这次重复。
   *
   * **联邦视图不再因降级而回退经典表**（用户裁定）：查询服务不可用时「没有就是没有」，
   * 把平台/历史日志填进节点/实例视图，会让「查不到」被读成「这些就是该视图的日志」，
   * 属于静默换源式的误导。Legacy 有专门入口（工具栏右侧「更多」），要看存量日志的用户
   * 自己切过去看，而不是由本视图替他决定。
   * 由此 `data` 在联邦视图下恒为 undefined，items/total/isLoading/isError 会连锁落到
   * 「联邦不可用 ⇒ 空且非成功」的语义上，无需各处重复判断。
   */
  const classicAuthoritative = !federationEnabled
  // 平台视图（view=platform）的权威来源已改为**进程日志文件尾部诊断**（2026-10-29）。
  //
  // 为什么改：CP `logs` 表已废弃——它把日志正文与业务数据混在同一个库，
  // 关键词检索是 LIKE 全表扫，保留期靠“归档后 DELETE”维持。
  // 新形态是诚实的降级：平台日志只提供“最近 N 行 + 时间/级别/关键字可见性”，
  // **不提供**分页、总条数、任意时间窗——那些能力在文件上实现不了，也不该假装能。
  // 要看完整历史请直接在该机器上 grep（面板会给出可复制的命令）。
  const platformLogQuery = usePlatformDiagnosticsLog(view === 'platform', 500)
  const platformLogLines = useMemo<LogEntry[]>(() => {
    const content = platformLogQuery.data?.content
    if (!content) return []
    return content
      .split('\n')
      .filter((line) => line.length > 0)
      // 尾部原文是「旧→新」，页面按时间倒序渲染，故反转。
      .reverse()
      .map((line, index) => ({
        id: -(index + 1),
        source: 'control_plane',
        // 级别由行内 level= 推断（slog text/json 两种格式都覆盖），失败则归 info。
        level: inferLineLevel(line),
        instanceId: 0,
        instanceUuid: '',
        nodeId: 0,
        message: line,
        time: inferLineTime(line),
      }) satisfies LogEntry)
  }, [platformLogQuery.data])
  // CP 经典日志接口（/logs、/logs/export）已随 `logs` 表下线，不再发起查询。
  // 保留同名占位量是为了让下游渲染分支（loading/error/data）保持单一形态，
  // 而不必在三处删条件；它们的值恒为“未加载/无错/空”。
  type ClassicLogPage = { items?: LogEntry[]; total?: number }
  const data = undefined as ClassicLogPage | undefined
  const classicLoading = false
  const classicError = false
  const refetchLogs = () => undefined
  // 同条件的聚合可复用搜索视图；实际时间窗与在线目标必须逐字段同源。
  const aggregateParams: Record<string, unknown> = {
    ...scope,
    ...(federation?.response.viewId ? { viewId: federation.response.viewId } : {}),
  }
  const mainFetching = federationEnabled ? federationQuery.isFetching : classicLoading
  const refetchMain = federationEnabled ? federationQuery.refetch : refetchLogs
  useEffect(() => {
    if (!follow || mainFetching || exporting) return
    // 上一轮真正落定后再计时，慢请求不会被新桶打断，也不会形成重叠轮询。
    const timer = window.setTimeout(() => {
      if (timeParams.from) setFollowAnchorMs(bucketFloor(Date.now()))
      else void refetchMain()
    }, FOLLOW_INTERVAL)
    return () => window.clearTimeout(timer)
  }, [follow, mainFetching, exporting, timeParams.from, refetchMain])

	// 编排（§1 延后串行）：① 主列表先出——stats/facets 均已 gate 在「主查询有数据且拿到 viewId」之后 ✓；
	// ② stats → facets 再串行——facets 额外等 stats 落定（成功或失败），
	// 任一时刻最多一个聚合请求在飞 ⇒ 不再与主列表/彼此并发挤 VL 的 maxConcurrentRequests=2 两个槽 ✗。
	const federationStats = useLogsFederationStats(
		{ ...aggregateParams, groupBy: 'level' },
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
	// 流清单（源 + 流名 + 条数）。complete=false 时条数不可信（VL 截断会把 hits 全归零）。
	const streamItems = !federationStreams.isError ? federationStreams.data?.streams ?? [] : []
	const streamsComplete = federationStreams.data?.complete === true
	const insightsVisible = federationActive && !federationQuery.isPlaceholderData && (
		federationStats.isSuccess || federationStats.isError || federationFacets.isSuccess || federationFacets.isError
		|| federationStreams.isSuccess || federationStreams.isError
	)
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

  // 两源：联邦（节点与实例）/ 平台进程日志尾部。
  // 平台视图不再有服务端分页与总数：total 就是拿到的行数（尾部条数），
  // 这不是“偷懒”，而是文件末尾的真相——不伪造一个算不出来的总数。
  const platformView = view === 'platform'
  const items = platformView
    ? platformLogLines
    : federationActive
      ? (federationItems ?? [])
      : (data?.items ?? [])
  const total = platformView
    ? platformLogLines.length
    : federationActive
      ? items.length
      : (data?.total ?? 0)
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
        exportRequest.current?.abort()
        setter(v)
        setPage(1)
        setFederationCursors({})
      },
    [],
  )

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
    || keyword.trim() !== debouncedKeyword
    || (federationEnabled ? federationQuery.isFetching || federationQuery.isPlaceholderData || federationQuery.isError
      : true)
	// federated 探测未落地前，items 可能暂时为空——此时不得判为「空结果」，
	// 否则会先闪 empty-state（且 404 降级期间显示非成功空态），把尚未就绪误报成失败。
	// 故把「联邦探测未完成」并入 loading：只有探测结束、数据源确定后才允许判空。
	//
	// 联邦视图下经典查询根本不参与页面态（`classicAuthoritative=false` ⇒ 不发起、无数据）：
	// 页面态只由联邦探测与联邦结果决定，不再存在「降级后由经典表说话」这条路径。
	const federationProbePending = federationEnabled && federationQuery.isLoading
	const isLoading = federationProbePending || (classicAuthoritative && classicLoading)
	const isError = classicAuthoritative && classicError
	const hasData = classicAuthoritative
		? !!data
		// 联邦视图下「有数据」= 有联邦结果（含限流时保留的上一份行）。
		: !!federation
	// 空结果语义：失败/partial 不得呈「暂无日志」空成功。
	// 查询服务未启用（降级）亦然：经典路径可能确实没有行，但引擎侧数据从未被查询，
	// 此时宣称「暂无日志」是假成功，必须保留非成功空态。
	// 限流（rate_limited）同样是失败态：emptySuccessAllowed 由 classified 判为 false。
	// 联邦视图下**没有**「降级即空成功」这条例外了：降级只说明查不到，不允许显示成
	// 「暂无日志」——那是把未查询说成不存在。平台视图仍按经典路径判定。
	const emptySuccessAllowed = federationEnabled
		? (federation?.classified.emptySuccessAllowed ?? false)
		: (!federation || federation.classified.emptySuccessAllowed)

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
      </div>
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
          onChange={(e) => { exportRequest.current?.abort(); setKeyword(e.target.value) }}
          placeholder={t('logs.searchPlaceholder')}
          className="h-9 w-52"
        />
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
      exportRequest.current?.abort()
      setRangeAnchor(Date.now())
      setFollowAnchorMs(bucketFloor(Date.now()))
			setFollow((f) => !f)
			setPage(1)
			setFederationCursors({})
		  }}
		  disabled={platformView}
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
      {insightsVisible && (
        <>
          <span data-testid="logs-stats-summary" role={federationStats.isError ? 'alert' : 'status'} title={statsSummary.reasons.join(', ')}>
            {federationStats.isError ? t('logs.aggregateFailed', { kind: t('logs.aggregateStats') })
              : statsCount !== undefined ? t(statsSummary.exact ? 'logs.statsEvents' : 'logs.statsObservedEvents', { count: statsCount })
                : t('logs.aggregateUnknown', { kind: t('logs.aggregateStats') })}
            {!federationStats.isError && !statsSummary.exact && ` · ${t(statsSummary.partial ? 'logs.aggregatePartial' : statsSummary.pending ? 'logs.aggregatePending' : 'logs.aggregateUnverified')}`}
            {!federationStats.isError && !statsSummary.qualityExact && ` · ${t('logs.aggregateQuality', { quality: statsSummary.quality })}`}
            {statsSummary.truncated && ` · ${t('logs.aggregateTruncated')}`}
          </span>
          <span data-testid="logs-facets-summary" role={federationFacets.isError ? 'alert' : 'status'} title={facetsSummary.reasons.join(', ')}>
            {federationFacets.isError ? t('logs.aggregateFailed', { kind: t('logs.aggregateFacets') })
              : t('logs.aggregateFacets')}
            {!federationFacets.isError && !facetsSummary.exact && ` · ${t(facetsSummary.partial ? 'logs.aggregatePartial' : facetsSummary.pending ? 'logs.aggregatePending' : 'logs.aggregateUnverified')}`}
            {!federationFacets.isError && !facetsSummary.qualityExact && ` · ${t('logs.aggregateQuality', { quality: facetsSummary.quality })}`}
            {facetsTruncated && ` · ${t('logs.aggregateTruncated')}`}
          </span>
          {levelFacets.map((facet) => (
            <StatusBadge key={facet.value} level={logLevelStatus(facet.value.toLowerCase())}
              label={`${facet.value} ${facet.count}${facetsSummary.exact && !facetsTruncated ? '' : ` (${t('logs.aggregateObserved')})`}`} />
          ))}
          {/* 流清单：窗内有哪些源在刷日志、各多少条。
              这是「哪个实例在刷屏」的直接答案，比 facets 的 stream 维度更全
              （后者只是 stdout/stderr 两个值，不含源）。
              条数只能在 complete=true 时当断言：VL 在截断时会把所有 hits 归零。 */}
          {streamItems.length > 0 && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  type="button"
                  data-testid="logs-streams-trigger"
                  className="inline-flex items-center gap-1.5 rounded-full bg-muted px-2.5 py-1 text-xs text-muted-foreground transition-colors duration-200 ease-ios hover:bg-accent"
                >
                  {t('logs.streamsCount', { count: streamItems.length })}
                  {!streamsComplete && ` · ${t('logs.streamsCountsUnreliable')}`}
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="max-h-80 w-80 overflow-y-auto">
                {streamItems.slice(0, 50).map((item) => (
                  <DropdownMenuItem
                    key={`${item.log_source_id}\u0000${item.stream}`}
                    // 只展示不筛选：`source` 参数是**来源类别前缀**（instance/worker），
                    // 不是具体源 id，拿 log_source_id 去砸它会把查询砸成 0 条。
                    // 真要做「点一下就筛这个源」得先给后端加一个按 log_source_id 的
                    // 精确筛选参数，那是另一件事。
                    disabled
                    title={item.raw ?? `${item.log_source_id} ${item.stream}`}
                    className="flex items-center justify-between gap-3 text-xs"
                  >
                    <span className="truncate">{item.log_source_id || t('logs.streamsUnknownSource')}
                      {item.stream ? <span className="ml-1 text-muted-foreground">{item.stream}</span> : null}
                    </span>
                    <span className="shrink-0 tabular-nums text-muted-foreground">
                      {streamsComplete ? item.hits : '?'}
                    </span>
                  </DropdownMenuItem>
                ))}
                {streamItems.length > 50 && (
                  <div className="px-2 py-1.5 text-[11px] text-muted-foreground">
                    {t('logs.streamsMore', { count: streamItems.length - 50 })}
                  </div>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </>
      )}
		</div>
	  )}

      {/* 覆盖/失败态横幅：partial/offline/failed 不得当空成功。 */}
      {federation && (
        <CoverageBanner
          federation={federation}
          onAction={handleFederationAction}
          qualityGuidanceKey={qualityGuidanceKey}
          onRetry={() => {
			void federationQuery.refetch(); void refetchLogs()
          }}
        />
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
   * 筛选身份键（view/来源/在线过滤/实例/节点/级别/关键字/范围；**不含 page** ✗——翻页需保留滚动位置）。
   * 变化即重置滚动：切实例后新列表更短时，旧 scrollTop 会落在空白区，visible 为空 ⇒
   * 表现为「点什么都不动/刷不出来」✗✓。
   * 维度须与参与查询的筛选条件一致（详见调用处 filterKey 的说明），否则会出现
   * 「筛选已变、滚动与保留项仍按旧身份判定」的不一致。
   */
  filterKey: string
  /** 查询尚未完成：此时不判空态，避免把「未就绪」误报为空结果。 */
  loading?: boolean
  /** false 时禁止把空列表呈成「无日志」成功空态。 */
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
      <div className="flex shrink-0 items-center gap-2 border-t px-3 py-2 text-[11px] text-status-success">
        <span className="size-1.5 animate-pulse rounded-full bg-status-success" />
        {t('logs.followingHint')}
        <span className="ml-auto text-muted-foreground">{t('logs.totalCount', { count: total })}</span>
      </div>
    )
  }
  if (platformTail) {
    return (
      <div className="flex shrink-0 items-center gap-2 border-t px-3 py-2 text-[11px] text-muted-foreground">
        <span>{t('logs.platformTailHint', { count: total })}</span>
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
