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
  useLogsFederation,
  useLogsFederationFacets,
  useLogsFederationStats,
} from '@/api/logFederation'
import { useAuthStore } from '@/stores/auth'
import { useNodes } from '@/api/nodes'
import { InstancePicker } from '@/components/InstancePicker'
import { LOGS_FEDERATION_KEYS } from '@/lib/logs-federation'
import {
  LogsPageView,
  type LogsFilterState,
  type LogsInsights,
  type LogsView,
} from '@/components/views/logs/LogsPageView'
import {
  buildExportParams,
  timeRangeToParams,
  type LogExportScope,
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
      range: 'all',
    }
  })
  const [page, setPage] = useState(1)
  const [federationCursors, setFederationCursors] = useState<Record<number, string>>({ 1: '' })
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
  }, [filter.range, follow])

  // 跟随态钉在第 1 页（最新）；锚点 now 在每次构建参数时取，配合轮询滚动时间窗。
  const timeParams = timeRangeToParams(
    filter.range,
    follow ? new Date() : new Date(rangeAnchor),
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
  const { data, isLoading: classicLoading, isError: classicError, refetch: refetchLogs } = useLogs(params, {
    refetchInterval: follow ? FOLLOW_INTERVAL : false,
    enabled: !legacyMode,
  })
  const legacyQuery = useLegacyLogs(params, legacyMode)

  // FR-482：可选联邦覆盖探测。与 useLogs 同筛选参数；API 404 → degraded legacy。
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
  const aggregateParams: Record<string, unknown> = {
    ...timeParams,
    ...(view === 'all' && filter.source ? { source: filter.source } : {}),
    ...(filter.level ? { level: filter.level } : {}),
    ...(filter.nodeId !== null ? { nodeId: filter.nodeId } : {}),
    ...(filter.instanceId !== null ? { instanceId: filter.instanceId } : {}),
    ...(filter.keyword.trim() ? { keyword: filter.keyword.trim() } : {}),
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
  // 洞察栏数据：只在联邦活跃时注入（视图据此判定是否挂 testid；非联邦态只留占位高度）。
  const insights: LogsInsights | undefined = federationActive
    ? { eventsCount: statsCount, levelFacets }
    : undefined
  const federationItems = federationActive
    ? mapFederationItems(federation.response.items)
    : null

  const items = legacyMode ? (legacyQuery.data?.items ?? []) : federationActive ? (federationItems ?? []) : (data?.items ?? [])
  const total = legacyMode ? (legacyQuery.data?.total ?? 0) : federationActive ? items.length : (data?.total ?? 0)
  const federationHasNext = federationActive && !!federation.response.nextCursor
  const totalPages = federationActive
    ? Math.max(1, page + (federationHasNext ? 1 : 0))
    : Math.max(1, Math.ceil(total / PAGE_SIZE))

  /**
   * 筛选变更：写回筛选态并**始终**回到第 1 页、清空联邦游标。
   * 三件事必须同处发生——只改其一会落在越界页或旧游标上（联邦游标与 page 一一对应）。
   */
  const applyFilter = useCallback((patch: Partial<LogsFilterState>) => {
    setFilter((prev) => ({ ...prev, ...patch }))
    setPage(1)
    setFederationCursors({ 1: '' })
  }, [])

  /** 主视图切换：与筛选同款重置；Legacy 是只读存量视图，切过去须先关掉实时跟随。 */
  const changeView = useCallback((next: LogsView) => {
    setView(next)
    setPage(1)
    setFederationCursors({ 1: '' })
    if (next === 'legacy') setFollow(false)
  }, [])

  /** 实时跟随开关：跟随态钉在第 1 页，故开关本身也要重置分页与游标。 */
  const toggleFollow = useCallback(() => {
    setFollow((f) => !f)
    setPage(1)
    setFederationCursors({ 1: '' })
  }, [])

  // 导出门禁：联邦可用且覆盖/校验未过 → 禁用；404 降级保留经典导出路径。
  const classicExportAllowed = !federation || federation.degraded
  const exportBlockedByFederation =
    !!federation && !classicExportAllowed && !federation.exportAffordance.showDownload
  const exportDisabled = legacyMode || exporting || total === 0 || exportBlockedByFederation
  // federated 探测（FR-482）未落地前，items 可能暂时为空——此时不得判为「空结果」，
  // 否则会先闪 empty-state（且 404 降级期间显示非成功空态），把尚未就绪误报成失败。
  // 故把「联邦探测未完成」并入 loading：只有探测结束、数据源确定后才允许判空。
  const federationProbePending = !legacyMode && federationQuery.isLoading
  const isLoading = legacyMode
    ? legacyQuery.isLoading
    : classicLoading || federationProbePending
  const isError = legacyMode ? legacyQuery.isError : classicError
  const hasData = legacyMode ? !!legacyQuery.data : !!data

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

  /** 覆盖态横幅重试：降级态只重取经典表（联邦未部署，再打也是失败）；否则两侧一起重取。 */
  const handleRetry = () => {
    if (legacyMode) void legacyQuery.refetch()
    else {
      void federationQuery.refetch()
      void refetchLogs()
    }
  }

  const goPrevPage = () => setPage((p) => Math.max(1, p - 1))
  /** 下一页：联邦态先把当前页返回的 nextCursor 存成下一页游标，再推页码。 */
  const goNextPage = () => {
    if (federationActive) {
      const cursor = federation.response.nextCursor
      if (!cursor) return
      setFederationCursors((current) => ({ ...current, [page + 1]: cursor }))
    }
    setPage((p) => Math.min(totalPages, p + 1))
  }

  return (
    <LogsPageView
      items={items}
      total={total}
      isLoading={isLoading}
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
  )
}
