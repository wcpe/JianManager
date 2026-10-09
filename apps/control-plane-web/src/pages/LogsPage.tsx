/**
 * 日志中心页容器（ADR-097 a 范式）：三路取数（经典 / Legacy / 联邦）、联邦降级编排、导出门禁与下载、
 * 路由深链与实时跟随轮询都在这里决定；筛选条、时间线与覆盖横幅交共享视图。
 *
 * 受控状态归属：`view` 与 `filter` 都是查询键，任一项变化都要回到第 1 页并清空联邦游标，
 * 故与 `page`、`follow`、`federationCursors` 一并留在这里；视图只上报意图。
 * 时间线的滚动位置等纯展示状态留在视图内。
 * 保留原路径默认导出，路由表（`ROUTE_CHUNKS['/logs']`）与既有用例无需改动。
 */
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { exportLogs, useLegacyLogs, useLogs, type LogEntry, type LogQueryParams } from '@/api/logs'
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
import { ClearConflictsDialog } from '@/components/ClearConflictsDialog'
import { InstancePicker } from '@/components/InstancePicker'
import { LOGS_FEDERATION_KEYS, exportBlockedKeyForReasons } from '@/lib/logs-federation'
import {
  LogsPageView,
  type LogsFilterState,
  type LogsInsights,
  type LogsView,
} from '@/components/views/logs/LogsPageView'
import {
  buildExportParams,
  timeRangeToParams,
  windowForAnchor,
  type LogExportScope,
  type TimeRangePreset,
} from './logs-filters'

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

const PAGE_SIZE = 100
const ROLE_PLATFORM_ADMIN = 10
// 实时跟随轮询间隔（ms）。
const FOLLOW_INTERVAL = 3000

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
 * 把时刻向下取整到最近一个 FOLLOW_INTERVAL 桶（纯函数，便于单测）。
 *
 * 跟随态的时间窗锚点必须落在桶边界上：若直接取挂钟毫秒，`from` 每次渲染都变，
 * queryKey 随之变化，react-query 视为新查询 → 响应永远落不到地。
 */
function bucketFloor(ms: number): number {
  return Math.floor(ms / FOLLOW_INTERVAL) * FOLLOW_INTERVAL
}

export default function LogsPage() {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const isPlatformAdmin = useAuthStore((state) => state.role === ROLE_PLATFORM_ADMIN)

  const [view, setView] = useState<LogsView>('node_instance')
  const [filter, setFilter] = useState<LogsFilterState>(() => {
    // 深链预筛选（FR-345）：从 URL ?instanceId= 初始化实例筛选（如从终端「查看完整历史」进入）。
    const q = searchParams.get('instanceId')
    const n = q ? Number(q) : NaN
    return {
      source: '',
      level: '',
      nodeId: null,
      instanceId: Number.isFinite(n) && n > 0 ? n : null,
      keyword: '',
      // 默认窗＝**15 分钟**（w21 快首屏 ✗ 的修正）：搜索页首屏走 CP 快路径（Query.Fast ⇒
      // resolveForQueryFast，省每 Worker 一次 OpenView 往返 ＋ 未校验标记 Coverage.Pending ✓），
      // 窗口越小首屏越快；默认「全部时间」会让每个目标的全部分区参与查询。
      // 「24h/全部」等更大范围仍从时间范围筛选器显式选择（走慢路径与完整校验 ✓，绝不给假完整 ✗）。
      range: '15m',
    }
  })
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
   * 跟随态由时间桶 state 驱动（见下），配合轮询滚动时间窗。
   */
  const [rangeAnchor, setRangeAnchor] = useState(() => Date.now())
  useEffect(() => {
    // 时间范围/跟随态变化时重置锚点：读取挂钟的「同步外部时间」语义，非渲染期纯计算。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setRangeAnchor(Date.now())
  }, [filter.range, follow])

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

  // 跟随态钉在第 1 页（最新）；锚点在每次构建参数时取，配合轮询滚动时间窗。
  const timeParams = timeParamsFor(
    filter.range,
    follow ? new Date(followAnchorMs) : new Date(rangeAnchor),
    follow,
  )
  const params: LogQueryParams = {
    view,
    page: follow ? 1 : page,
    pageSize: PAGE_SIZE,
    ...(view === 'all' && filter.source ? { source: filter.source } : {}),
    ...(filter.level ? { level: filter.level } : {}),
    ...(filter.nodeId !== null ? { nodeId: filter.nodeId } : {}),
    ...(filter.instanceId !== null ? { instanceId: filter.instanceId } : {}),
    ...(filter.keyword.trim() ? { keyword: filter.keyword.trim() } : {}),
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
  // 筛选身份键（view/来源/在线过滤/实例/节点/级别/关键字/范围；**不含 page** ✗——翻页需保留滚动位置）。
  // 既驱动滚动复位（LogTimeline 的 filterKey effect），也是限流保留项的归属判据：
  // 保留项记的是这个键，筛选一变，旧保留项即判失效、不得冒充新筛选的结果。
  //
  // **必须与真正参与查询的筛选维度逐一对应**：`source`（仅 all 视图）与 `onlineOnly`
  // 都进 federationParams ⇒ 都是独立 queryKey。漏掉它们时，在该视图下切换这两项会「改筛选
  // 不改身份」：键不变 ⇒ 保留项继续被复用，而新查询若 429（生产该端点 23.8%），
  // 页面就把**上一个来源/未过滤**的行渲染成新筛选的结果（例如只看 worker 却显示 control_plane 的行）。
  // `onlineOnly` 尤其容易漏判成「只是排序/展示开关」——它实际是服务端过滤条件。
  const filterKey = `${view}|${filter.source}|${onlineOnly}|${filter.instanceId ?? ''}|${filter.nodeId ?? ''}|${filter.level}|${filter.keyword.trim()}|${filter.range}`
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
    filter.nodeId !== null &&
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
    ...(view === 'all' && filter.source ? { source: filter.source } : {}),
    ...(filter.level ? { level: filter.level } : {}),
    ...(filter.nodeId !== null ? { nodeId: filter.nodeId } : {}),
    ...(filter.instanceId !== null ? { instanceId: filter.instanceId } : {}),
    ...(filter.keyword.trim() ? { keyword: filter.keyword.trim() } : {}),
    // 注意：**不得**把搜索的 viewId 带入聚合参数 ✗——stats/facets 是各自固定条件的独立查询，
    // 复用搜索视图会被 CP 按 F-006 语义拒（409 VIEW_STALE；实测 stats 与 facets 两呼皆 409 ✓）。
    // 去除后二者各自重建视图，服务端校验一字不动 ✗。
  }
  // Tail 无永久位置字段：复用既有 Search 每 30 秒刷新证据，固定窗口避免每个 3 秒轮询桶重查。
  const followEvidenceQuery = useLogsFederation({
    ...aggregateParams,
    ...timeParamsFor(filter.range, new Date(rangeAnchor), true),
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
  // 洞察栏数据：只在联邦活跃时注入（视图据此判定是否挂 testid；非联邦态只留占位高度）。
  const insights: LogsInsights | undefined = federationActive
    ? { eventsCount: statsCount, levelFacets }
    : undefined
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
    && items.length > 0 && filter.range !== 'all' && typeof federationParams.from === 'string'
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

  /**
   * 筛选变更：写回筛选态并**始终**回到第 1 页、清空联邦游标。
   * 三件事必须同处发生——只改其一会落在越界页或旧游标上（联邦游标与 page 一一对应）。
   */
  const applyFilter = useCallback((patch: Partial<LogsFilterState>) => {
    setFilter((prev) => ({ ...prev, ...patch }))
    setPage(1)
    setFederationCursors({})
  }, [])

  /** 主视图切换：与筛选同款重置；Legacy 是只读存量视图，切过去须先关掉实时跟随。 */
  const changeView = useCallback((next: LogsView) => {
    setView(next)
    setPage(1)
    setFederationCursors({})
    if (next === 'legacy') setFollow(false)
  }, [])

  /** 实时跟随开关：跟随态钉在第 1 页，故开关本身也要重置分页与游标。 */
  const toggleFollow = useCallback(() => {
    setFollow((f) => !f)
    setPage(1)
    setFederationCursors({})
  }, [])

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

  /** 覆盖态横幅重试：降级态只重取经典表（联邦未部署，再打也是失败）；否则两侧一起重取。 */
  const handleRetry = () => {
    if (legacyMode) void legacyQuery.refetch()
    else {
      void federationQuery.refetch()
      void refetchLogs()
    }
  }

  const goPrevPage = () => setPage((p) => Math.max(1, p - 1))
  /** 下一页：联邦态先把「续页游标或完整窗口」存成下一页历史，再推页码。 */
  const goNextPage = () => {
    if (federationActive) {
      if (!federationNextAvailable) return
      const next = federationCanContinue
        ? {
          from: federationParams.from as string | undefined,
          to: federationParams.to as string | undefined,
          viewId: federation?.response.viewId,
          cursor: federation?.response.nextCursor ?? undefined,
        }
        : windowForAnchor(filter.range, federationParams.from as string)
      setFederationCursors((current) => ({ ...current, [page + 1]: next }))
    }
    setPage((p) => p + 1)
  }

  return (
    <>
      <LogsPageView
        items={items}
        total={total}
        // 切窗口/筛选期间展示的是上一份数据（placeholderData）：此时 isLoading 为 false
        // 但新结果尚未到达，须维持 loading 语义（视图据此不把旧数据误判为空态）；
        // follow 轮询每 3 秒换桶，不置位以免闪烁。
        isLoading={isLoading || (!follow && federationQuery.isPlaceholderData)}
        hasData={hasData}
        isError={isError}
        emptySuccessAllowed={emptySuccessAllowed}
        page={page}
        totalPages={totalPages}
        view={view}
        filter={filter}
        follow={follow}
        nodes={nodes}
        isPlatformAdmin={isPlatformAdmin}
        // 联邦结果对象含视图未声明的字段（response/available），结构兼容即可直接透传。
        federation={federation}
        insights={insights}
        exporting={exporting}
        exportDisabled={exportDisabled}
        exportBlocked={exportBlockedByFederation}
        // 筛选身份键：视图据此复位时间线滚动，且与限流保留项同源判归属。
        filterKey={filterKey}
        ingestPositionGaps={ingestPositionGaps}
        onClearConflicts={canClearConflicts ? () => setClearConflictsOpen(true) : undefined}
        // 联邦态的可翻性由服务端 next_cursor / 可移窗驱动；非联邦回退页码比较。
        canNext={federationActive ? federationNextAvailable : undefined}
        onChangeFilter={applyFilter}
        onChangeView={changeView}
        onToggleFollow={toggleFollow}
        onPrevPage={goPrevPage}
        onNextPage={goNextPage}
        onExport={(scope) => void handleExport(scope)}
        onFederationAction={handleFederationAction}
        onRetry={handleRetry}
        renderInstancePicker={({ value, onChange, disabled }) => (
          // 实例候选走服务端搜索（防抖与请求属本层策略，视图只提供落点）。
          <InstancePicker
            value={value}
            onChange={(id) => onChange(id)}
            allowAll
            allLabel={t('logs.allInstances')}
            placeholder={t('logs.allInstances')}
            disabled={disabled}
            className="w-44"
          />
        )}
      />
      {/* 身份冲突人工清空（运维入口）：只在有冲突、且当前选中单一节点时可用。
          未选中单一节点时无法确定要清哪个节点，故不给入口（而不是默认清第一个）。 */}
      {canClearConflicts && filter.nodeId !== null && (
        <ClearConflictsDialog
          nodeId={filter.nodeId}
          open={clearConflictsOpen}
          onOpenChange={setClearConflictsOpen}
          onCleared={() => {
            if (legacyMode) void legacyQuery.refetch()
            else { void federationQuery.refetch(); void refetchLogs() }
          }}
        />
      )}
    </>
  )
}
