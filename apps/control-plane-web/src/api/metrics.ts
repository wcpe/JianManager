import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'
import { INSTANCE_QUERY_GC_TIME_MS } from '@/api/instances'
import type { MetricRange } from '@jianmanager/ui'

export interface NodeMetricsData {
  cpuUsage: number
  memoryUsage: number
  diskUsage: number
  memoryUsedMb: number
  memoryTotalMb: number
  diskUsedMb: number
  diskTotalMb: number
}

export interface WorldMetric {
  name: string
  loadedChunks: number
  entities: number
  tileEntities: number
}

export interface InstanceMetricsData {
  tps: number
  onlinePlayers: number
  memoryMb: number
  msptMillis: number
  threads: number
  cpuPercent: number
  heapMaxMb: number
  uptimeSeconds: number
  worlds: WorldMetric[] | null
  probeAvailable: boolean
  // === MC 直探（SLP / Query，FR-446 / FR-447）补充字段与显式可用性位 ===
  // 采集优先级链 `探针 → SLP → Query → 不可用`：`*Available=false` 即「不可用」，
  // 前端据此渲染「不可用」而非 `0` / `-1` / `--` 伪值（消除「0 人在线」回归）。
  playersAvailable: boolean
  motd: string
  motdAvailable: boolean
  version: string
  versionAvailable: boolean
  /**
   * 服务端图标（`data:image/png;base64,...`，可达 ~50KB）。**预留字段**：后端 SLP → Worker →
   * `MetricsData.favicon` 已端到端回传，前端当前仅声明未渲染；若要展示应在实例详情页 MOTD
   * 卡片按需渲染，避免把大 base64 注入卡片列表（FR-446 审计项 7）。
   */
  favicon: string
  maxPlayers: number
  maxPlayersAvailable: boolean
  playerNames: string[] | null
  playerNamesAvailable: boolean
  /** true=名单取自 SLP sample（弱信息，可能不完整，非实名）。 */
  playerNamesPartial: boolean
  plugins: string[] | null
  pluginsAvailable: boolean
  map: string
  mapAvailable: boolean
  slpAvailable: boolean
  queryAvailable: boolean
  /** 本拍命中来源位：1=探针 2=SLP 4=Query（与后端 `metrics.SourceMask` 对齐）。 */
  sourceMask: number
}

export function useNodeMetrics(nodeId: number) {
  return useQuery({
    queryKey: ['nodeMetrics', nodeId],
    queryFn: async () => {
      const { data } = await api.get<NodeMetricsData>(`/nodes/${nodeId}/metrics`)
      return data
    },
    enabled: !!nodeId,
    refetchInterval: 30_000,
  })
}

export function useInstanceMetrics(instanceId: number, enabled = true) {
  return useQuery({
    queryKey: ['instanceMetrics', instanceId],
    queryFn: async () => {
      const { data } = await api.get<InstanceMetricsData>(`/instances/${instanceId}/metrics`)
      return data
    },
    enabled: !!instanceId && enabled,
    refetchInterval: enabled ? 10_000 : false,
    // FR-297：跨服回切先呈现缓存指标后台刷新，避免 KPI 区闪空。
    gcTime: INSTANCE_QUERY_GC_TIME_MS,
    placeholderData: keepPreviousData,
  })
}

// === 时序历史指标（FR-060：/metrics/series、/metrics/overview） ===

/** 曲线上一点：raw 档 avg=min=max；缺测为 null（断点）。 */
export interface SeriesPoint {
  ts: string
  avg: number | null
  min: number | null
  max: number | null
}

/** 一条历史序列。scope=instance 含分世界时 world 非空。 */
export interface MetricSeries {
  metricKey: string
  unit: string
  world: string
  points: SeriesPoint[]
}

export interface MetricSeriesResponse {
  resolution: string
  from: string
  to: string
  series: MetricSeries[]
}

/** FR-401 节点关联的共享 Bot Worker 运行时历史响应。 */
export interface BotRuntimeMetricResponse {
  resolution: string
  from: string
  to: string
  sharedRuntime: boolean
  notice: string
  nodes: { nodeId: number; nodeName: string; series: MetricSeries[] }[]
  unavailable: { nodeId: number; reason: string }[]
}

/**
 * 聚合粒度档位（FR-221，ADR-013 三档降采样）：
 * auto=按区间自动选档；raw=原始 30s；5m/1h=对应降采样卷积档。
 */
export type MetricResolution = 'auto' | 'raw' | '5m' | '1h'

/** 节点/实例历史曲线（FR-060；FR-221 增自定义粒度）。按区间自动选档（或显式 resolution），30s 轮询。 */
export function useMetricSeries(params: {
  scope: 'node' | 'instance'
  targetId: string
  range: MetricRange
  /** 指标键过滤（逗号合并下发）；不传返回该目标全部序列。 */
  metrics?: string[]
  /** 聚合粒度档位（FR-221）；auto/留空=按区间自动选档。 */
  resolution?: MetricResolution
  enabled?: boolean
}) {
  const { scope, targetId, range, metrics, resolution, enabled = true } = params
  return useQuery({
    queryKey: ['metricSeries', scope, targetId, range, metrics?.join(',') ?? '', resolution ?? 'auto'],
    queryFn: async () => {
      const q = new URLSearchParams({ scope, targetId, range })
      if (metrics?.length) q.set('metrics', metrics.join(','))
      if (resolution && resolution !== 'auto') q.set('resolution', resolution)
      const { data } = await api.get<MetricSeriesResponse>(`/metrics/series?${q.toString()}`)
      return data
    },
    enabled: enabled && !!targetId,
    refetchInterval: 30_000,
  })
}

/** 节点监控页的共享 Bot Worker 状态；30 秒轮询以与历史采样节奏对齐。 */
export function useBotRuntimeMetrics(params: {
  nodeId: number | undefined
  range: MetricRange
  resolution?: MetricResolution
  enabled?: boolean
}) {
  const { nodeId, range, resolution, enabled = true } = params
  return useQuery({
    queryKey: ['botRuntimeMetrics', nodeId ?? 0, range, resolution ?? 'auto'],
    queryFn: async () => {
      const q = new URLSearchParams({ nodeId: String(nodeId), range })
      if (resolution && resolution !== 'auto') q.set('resolution', resolution)
      const { data } = await api.get<BotRuntimeMetricResponse>(`/metrics/bot-runtime?${q.toString()}`)
      return data
    },
    enabled: enabled && !!nodeId,
    refetchInterval: enabled && nodeId ? 30_000 : false,
  })
}

/** 被剔除的目标（FR-340）：无实例访问权（forbidden）或 UUID 不存在（not_found）。 */
export interface SkippedTarget {
  targetId: string
  reason: 'forbidden' | 'not_found'
}

/**
 * 批量序列响应（FR-340）：整批共用 resolution/from/to；series 按 targetId(UUID) 分组，
 * 值与单目标端点 series 数组同构；被剔除的目标入 skipped（前端可区分「无数据」空数组与「被剔除」）。
 */
export interface MetricSeriesBatchResponse {
  resolution: string
  from: string
  to: string
  series: Record<string, MetricSeries[]>
  skipped: SkippedTarget[]
}

/**
 * 批量多实例历史曲线（FR-340：消 NodeInstanceCompare 的 N+1）。一次请求返回各目标序列，
 * 30s 轮询。targetIds 为空时不请求。v1 仅 scope=instance（对比场景只有实例维度有 N+1）。
 */
export function useMetricSeriesBatch(params: {
  scope: 'instance'
  targetIds: string[]
  range: MetricRange
  /** 指标键过滤（逗号合并下发）；不传返回各目标全部序列。 */
  metrics?: string[]
  /** 聚合粒度档位（FR-221）；auto/留空=按区间自动选档。 */
  resolution?: MetricResolution
  enabled?: boolean
}) {
  const { scope, targetIds, range, metrics, resolution, enabled = true } = params
  return useQuery({
    queryKey: ['metricSeriesBatch', scope, targetIds.join(','), range, metrics?.join(',') ?? '', resolution ?? 'auto'],
    queryFn: async () => {
      const { data } = await api.post<MetricSeriesBatchResponse>('/metrics/series/batch', {
        scope,
        targetIds,
        ...(metrics?.length ? { metrics } : {}),
        range,
        ...(resolution && resolution !== 'auto' ? { resolution } : {}),
      })
      return data
    },
    enabled: enabled && targetIds.length > 0,
    refetchInterval: 30_000,
  })
}

export interface OverviewTotals {
  nodeCount: number
  onlineNodeCount: number
  runningInstances: number
  cpuPct: number
  /** 在线节点负载利用率均值（load1/核数*100，FR-062）。 */
  loadAvg: number
  memUsedBytes: number
  memTotalBytes: number
  onlinePlayers: number
}

export interface OverviewTrend {
  metricKey: string
  unit: string
  points: SeriesPoint[]
}

export interface MetricOverviewResponse {
  totals: OverviewTotals
  resolution: string
  trends: OverviewTrend[]
}

/** 总览页跨节点聚合：当前总量 + 聚合曲线（FR-060；FR-221 增自定义粒度）。可见页每 10 秒轮询。 */
export function useMetricOverview(range: MetricRange, resolution?: MetricResolution) {
  return useQuery({
    queryKey: ['metricOverview', range, resolution ?? 'auto'],
    queryFn: async () => {
      const q = new URLSearchParams({ range })
      if (resolution && resolution !== 'auto') q.set('resolution', resolution)
      const { data } = await api.get<MetricOverviewResponse>(`/metrics/overview?${q.toString()}`)
      return data
    },
    refetchInterval: 10_000,
    refetchIntervalInBackground: false,
  })
}

export type ResourceFreshness = 'fresh' | 'stale' | 'offline' | 'unavailable'

/** FR-402 平台管理员首页的有界全景观测读模型。 */
export interface PlatformObservabilityOverviewResponse {
  sampledAt: string | null
  health: {
    nodeCount: number
    onlineNodeCount: number
    staleNodeCount: number
    offlineNodeCount: number
    runningInstanceCount: number
    crashedInstanceCount: number
    stoppedInstanceCount: number
  }
  resources: {
    cpuPct: number | null
    loadPct: number | null
    memoryUsedBytes: number | null
    memoryTotalBytes: number | null
    freshness: ResourceFreshness
  }
  bots: {
    sharedRuntime: true
    notice: string
    nodeCount: number
    botWorkerRssBytes: number | null
    botWorkerCpuPct: number | null
    workerProcessRssBytes: number | null
    workerProcessCpuPct: number | null
    activeCount: number | null
    connectingCount: number | null
    eventLoopP95Ms: number | null
    unavailable: { nodeId: number; reason: string }[]
  }
  alerts: { id: number; severity: string; title: string; createdAt: string }[]
  tasks: { id: number; state: string; title: string; updatedAt: string }[]
  exceptions: { kind: string; nodeId?: number; instanceId?: number; title: string; href: string }[]
}

/** 平台管理员可见时每 10 秒刷新；TanStack Query 默认隐藏页暂停，这里显式固定该契约。 */
export function usePlatformObservabilityOverview(enabled: boolean) {
  return useQuery({
    queryKey: ['platformObservabilityOverview'],
    queryFn: async () => {
      const { data } = await api.get<PlatformObservabilityOverviewResponse>('/observability/overview')
      return data
    },
    enabled,
    staleTime: 0,
    refetchInterval: enabled ? 10_000 : false,
    refetchIntervalInBackground: false,
  })
}

/** FR-461 健康墙单格分级。 */
export type HealthLevel = 'offline' | 'stale' | 'degraded' | 'healthy'

/** FR-461 健康墙单格：一台节点的当前只读快照。 */
export interface HealthWallNode {
  nodeId: number
  nodeUuid: string
  name: string
  zone?: string
  freshness: 'fresh' | 'stale' | 'offline'
  cpuPct: number | null
  memPct: number | null
  diskPct: number | null
  running: number
  crashed: number
  stopped: number
  /**
   * FR-459/FR-461：活着但已不可服务的实例数（状态仍 RUNNING，巡检已写入 status_reason，
   * 如假死 / 崩溃熔断）。假死进程不会退出、状态也不转 CRASHED，只按 status 分级会漏掉，
   * 故服务端单列计数并据此把节点降级。老 CP 不返回本字段。
   */
  degraded?: number
  activeAlerts: number
  botActive: number | null
  botConnecting: number | null
  level: HealthLevel
  /** 一键定位下钻地址：/monitoring?node=<uuid>。 */
  href: string
}

/** FR-461 健康墙读模型。 */
export interface HealthWallResponse {
  nodes: HealthWallNode[]
  /** 节点数超过服务端上限、响应已按 severity 截断时为 true。 */
  truncated?: boolean
}

/** 健康墙服务端排序键。 */
export type HealthWallSort = 'level' | 'cpu' | 'mem' | 'disk' | 'instances'

/** 集群健康墙（FR-461）：只读快照一次查询给全，不触发 Worker RPC。 */
export function useHealthWall(enabled: boolean, sort: HealthWallSort = 'level') {
  return useQuery({
    queryKey: ['healthWall', sort],
    queryFn: async () => {
      const { data } = await api.get<HealthWallResponse>('/observability/health-wall', { params: { sort } })
      return data
    },
    enabled,
    staleTime: 0,
    refetchInterval: enabled ? 15_000 : false,
    refetchIntervalInBackground: false,
  })
}

export interface ResourceAttributionBotWorker {
  rssBytes: number | null
  cpuPct: number | null
  activeCount: number | null
  connectingCount: number | null
  eventLoopP95Ms: number | null
  available: boolean
  reason: string
}

export interface ResourceAttributionNode {
  nodeId: number
  nodeUuid: string
  name: string
  status: ResourceFreshness
  observedAt: string | null
  cpuPct: number | null
  loadPct: number | null
  memoryUsedBytes: number | null
  memoryTotalBytes: number | null
  workerProcessRssBytes: number | null
  workerProcessCpuPct: number | null
  botWorker: ResourceAttributionBotWorker
}

export interface ResourceAttributionInstance {
  instanceId: number
  instanceUuid: string
  instanceName: string
  nodeId: number
  cpuPct: number
  rssBytes: number
  sampledAt: string
}

export interface ResourceAttributionProcess {
  instanceId: number
  instanceUuid: string
  instanceName: string
  nodeId: number
  pid: number
  name: string
  cpuPercent: number
  rssBytes: number
  sampledAt: string
}

export interface ResourceAttributionResponse {
  sampledAt: string | null
  freshness: ResourceFreshness
  nodes: ResourceAttributionNode[]
  topInstances: ResourceAttributionInstance[]
  topProcesses: ResourceAttributionProcess[]
}

/** 首页受管资源归因；仅在管理员打开仪表 Tooltip 后轮询，隐藏标签页默认暂停。 */
export function useResourceAttribution(enabled: boolean, sort: 'cpu' | 'memory') {
  return useQuery({
    queryKey: ['resourceAttribution', sort],
    queryFn: async () => {
      const { data } = await api.get<ResourceAttributionResponse>(`/metrics/resource-attribution?sort=${sort}&limit=5`)
      return data
    },
    enabled,
    refetchInterval: enabled ? 10_000 : false,
    refetchIntervalInBackground: false,
  })
}

export interface ProcessTopItem {
  instanceId: number
  instanceUuid: string
  nodeUuid: string
  pid: number
  name: string
  cpuPercent: number
  rssBytes: number
  readBytesPerSec: number
  writeBytesPerSec: number
  user: string
  commandSummary: string
  sampledAt: string
}

export interface ManagedProcessInfo {
  pid: number
  parentPid: number
  name: string
  isRoot: boolean
  cpuPercent: number
  rssBytes: number
  readBytesPerSec: number
  writeBytesPerSec: number
  user: string
  commandSummary: string
  uptimeSeconds: number
  threadCount: number
  sampledAt: string
  unavailableReason: string
}

export interface ManagedProcessDiagnostic {
  code: string
  severity: 'info' | 'warning' | 'danger' | string
  title: string
  evidence: string
  suggestion: string
}

export interface ManagedProcessDetail {
  instance: { id: number; uuid: string; name: string; nodeId: number; nodeUuid: string; nodeName: string }
  rootPid: number
  target: ManagedProcessInfo
  ancestors: ManagedProcessInfo[]
  children: ManagedProcessInfo[]
  diagnostics: ManagedProcessDiagnostic[]
  history: {
    windowSeconds: number
    sampleCount: number
    latestSampledAt: string
    rssDeltaBytes: number
    avgCpuPercent: number
    avgWriteBytesPerSec: number
  }
}

export interface ManagedProcessActionResult {
  success: boolean
  action: 'terminate' | 'kill_tree' | string
  pid: number
  affectedPids: number[]
  message: string
}

export type ManagedProcessAction = 'terminate' | 'kill_tree'

export function useProcessTop(params: {
  instanceId?: number
  nodeId?: string
  sort?: 'cpu' | 'memory' | 'io'
  limit?: number
  enabled?: boolean
}) {
  const { instanceId, nodeId, sort = 'cpu', limit = 10, enabled = true } = params
  return useQuery({
    queryKey: ['processTop', instanceId ?? '', nodeId ?? '', sort, limit],
    queryFn: async () => {
      const q = new URLSearchParams({ sort, limit: String(limit) })
      if (instanceId) q.set('instanceId', String(instanceId))
      if (nodeId) q.set('nodeId', nodeId)
      const { data } = await api.get<ProcessTopItem[]>(`/metrics/processes/top?${q.toString()}`)
      return data
    },
    enabled,
    refetchInterval: enabled ? 30_000 : false,
  })
}

export function useManagedProcessDetail(instanceId: number | undefined, pid: number | undefined, enabled = true) {
  return useQuery({
    queryKey: ['managedProcessDetail', instanceId ?? 0, pid ?? 0],
    queryFn: async () => {
      const { data } = await api.get<ManagedProcessDetail>(`/instances/${instanceId}/processes/${pid}`)
      return data
    },
    enabled: enabled && !!instanceId && !!pid,
    refetchInterval: enabled && instanceId && pid ? 10_000 : false,
  })
}

export function useManagedProcessAction() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (params: { instanceId: number; pid: number; action: ManagedProcessAction }) => {
      const { data } = await api.post<ManagedProcessActionResult>(
        `/instances/${params.instanceId}/processes/${params.pid}/actions?confirm=true`,
        { action: params.action, confirm: true },
      )
      return data
    },
    onSuccess: (result, params) => {
      toast.success(result.message || '进程处置已提交')
      qc.invalidateQueries({ queryKey: ['processTop'] })
      qc.invalidateQueries({ queryKey: ['managedProcessDetail', params.instanceId, params.pid] })
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['instance'] })
      qc.invalidateQueries({ queryKey: ['instanceMetrics'] })
      qc.invalidateQueries({ queryKey: ['metricOverview'] })
      qc.invalidateQueries({ queryKey: ['metricSeries'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '进程处置失败')
    },
  })
}

// === FR-463/464/465/469：SLO 可用性 / 容量预测 / 性能归因 / 跨实例排行与玩家趋势 ===

/** 归因因子（FR-465）：与目标的 Pearson 相关、归一化贡献权重。 */
export interface AttributionFactor {
  metricKey: string
  label: string
  correlation: number
  weight: number
  note?: string
}

/** 性能归因结果（FR-465）。status=insufficient 时 factors 为空，不伪造排序。 */
export interface AttributionResult {
  target: string
  window: { from: string; to: string }
  status: 'ok' | 'insufficient'
  tldr: string
  factors: AttributionFactor[]
  samples: number
}

/**
 * 实例性能归因（FR-465）：按需触发（enabled=false 时不请求），不做常驻轮询——
 * 归因基于窗口相关分析，随每 30s 心跳变动的意义不大。
 */
export function usePerformanceAttribution(params: {
  targetId: string
  range: MetricRange
  metric?: string
  enabled?: boolean
}) {
  const { targetId, range, metric, enabled = false } = params
  return useQuery({
    queryKey: ['performanceAttribution', targetId, range, metric ?? 'inst_tps'],
    queryFn: async () => {
      const q = new URLSearchParams({ scope: 'instance', targetId, range })
      if (metric) q.set('metric', metric)
      const { data } = await api.get<AttributionResult>(`/metrics/performance/attribution?${q.toString()}`)
      return data
    },
    enabled: enabled && !!targetId,
  })
}

/** 排行支持的指标键（与后端 rankingSupportedMetrics 对齐）。 */
export type RankingMetric = 'inst_tps' | 'inst_mspt' | 'inst_cpu_pct' | 'inst_heap_used' | 'inst_players_online'

/** 排行一项（FR-469）。 */
export interface RankingItem {
  instanceId: number
  instanceUuid: string
  name: string
  nodeUuid: string
  value: number
  rank: number
  sampledAt: string
}

/** 排行结果（FR-469）。scoped=true 表示非管理员受限视图。 */
export interface RankingResult {
  metricKey: string
  order: 'asc' | 'desc'
  windowSeconds: number
  scoped: boolean
  skippedNoData: number
  items: RankingItem[]
}

/** 跨实例全局排行（FR-469）。30s 轮询与采样节奏对齐。 */
export function useInstanceRanking(params: {
  metric: RankingMetric
  window?: string
  limit?: number
  nodeId?: string
  order?: 'asc' | 'desc'
  enabled?: boolean
}) {
  const { metric, window = '5m', limit = 20, nodeId, order, enabled = true } = params
  return useQuery({
    queryKey: ['instanceRanking', metric, window, limit, nodeId ?? '', order ?? ''],
    queryFn: async () => {
      const q = new URLSearchParams({ metric, window, limit: String(limit) })
      if (nodeId) q.set('nodeId', nodeId)
      if (order) q.set('order', order)
      const { data } = await api.get<RankingResult>(`/metrics/instances/ranking?${q.toString()}`)
      return data
    },
    enabled,
    refetchInterval: 30_000,
  })
}

/** 玩家在线趋势结果（FR-469）：全网合计曲线 + 24 时段分布 + 峰值/日均。 */
export interface PlayerTrendResult {
  resolution: string
  timezone: string
  trend: SeriesPoint[]
  hourlyDist: number[]
  peakValue: number
  peakAt: string | null
  dailyAvg: number
}

/**
 * 玩家在线趋势与时段分析（FR-469）。tz 缺省时后端按服务器本地时区分桶，
 * 前端用浏览器时区下发以便时段口径与运维所在时区一致。
 */
export function usePlayerTrend(params: { range: MetricRange; resolution?: MetricResolution; tz?: string; enabled?: boolean }) {
  const { range, resolution, tz, enabled = true } = params
  return useQuery({
    queryKey: ['playerTrend', range, resolution ?? 'auto', tz ?? ''],
    queryFn: async () => {
      const q = new URLSearchParams({ range })
      if (resolution && resolution !== 'auto') q.set('resolution', resolution)
      if (tz) q.set('tz', tz)
      const { data } = await api.get<PlayerTrendResult>(`/metrics/players/trend?${q.toString()}`)
      return data
    },
    enabled,
    refetchInterval: 60_000,
  })
}

/** 可用性/SLO 结果（FR-463）。MTTR/MTBF 为 null 表示无已恢复故障/无故障（不是 Infinity）。 */
export interface SLOResult {
  scope: 'platform' | 'node' | 'instance'
  availability: number
  totalSamples: number
  upSamples: number
  incidents: number
  activeIncidents: number
  mttrSeconds: number | null
  mtbfSeconds: number | null
  budgetAllowedSec: number
  budgetBurnedSec: number
  target: number
  approximatedBuckets: boolean
  /** false=窗口内无可用证据（分母为 0）：可用率与误差预算均不适用，前端显示「不适用」。 */
  applicable: boolean
}

/**
 * 平台/实例/节点可用性与 SLO 聚合（FR-463）。platform 为非管理员的可见实例汇总；
 * node/instance 维度需 targetId，实例无权时后端 403。
 */
export function useSLO(params: {
  scope: 'platform' | 'node' | 'instance'
  targetId?: string
  range: MetricRange
  target?: number
  enabled?: boolean
}) {
  const { scope, targetId, range, target, enabled = true } = params
  return useQuery({
    queryKey: ['slo', scope, targetId ?? '', range, target ?? 0],
    queryFn: async () => {
      const q = new URLSearchParams({ scope, range })
      if (targetId) q.set('targetId', targetId)
      if (target) q.set('target', String(target))
      const { data } = await api.get<SLOResult>(`/metrics/slo?${q.toString()}`)
      return data
    },
    enabled: enabled && (scope === 'platform' || !!targetId),
    refetchInterval: 60_000,
  })
}

/** 单指标容量外推结果（FR-464）。Exhaust* 为 null 表示无增长/样本不足，不伪造预测。 */
export interface ForecastResult {
  targetId: string
  metricKey: string
  nowValue: number
  limitValue: number
  slopePerSec: number
  exhaustAt: string | null
  exhaustLowDays: number | null
  exhaustHighDays: number | null
  confidence: 'high' | 'low' | 'insufficient'
  samples: number
  note?: string
}

/** 容量预测响应（FR-464）。 */
export interface CapacityForecastResponse {
  forecasts: ForecastResult[]
}

/** 实例/节点容量耗尽预测（FR-464）。60s 轮询。 */
export function useCapacityForecast(params: {
  scope: 'node' | 'instance'
  targetId: string
  range: MetricRange
  metrics?: string[]
  enabled?: boolean
}) {
  const { scope, targetId, range, metrics, enabled = true } = params
  return useQuery({
    queryKey: ['capacityForecast', scope, targetId, range, metrics?.join(',') ?? ''],
    queryFn: async () => {
      const q = new URLSearchParams({ scope, targetId, range })
      if (metrics?.length) q.set('metrics', metrics.join(','))
      const { data } = await api.get<CapacityForecastResponse>(`/metrics/capacity/forecast?${q.toString()}`)
      return data
    },
    enabled: enabled && !!targetId,
    refetchInterval: 60_000,
  })
}
