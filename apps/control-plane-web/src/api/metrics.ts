import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
// 序列与实时指标契约已回迁应用侧（受控视图与业务页面共用，ADR-103）；此处原样再导出，调用点无需改动。
// 本地绑定供本文件的查询泛型使用。
import type { InstanceMetricsData } from '@/lib/instances/instance-metrics'
import type { PlayerTrendResult } from '@/lib/players/player-trend'
import type { SLOResult } from '@/lib/metrics/slo'
import type { RankingMetric, RankingResult } from '@/lib/instances/ranking'
import type { MetricSeries, SeriesPoint } from '@/lib/metrics/metric-series'
export type { InstanceMetricsData, WorldMetric } from '@/lib/instances/instance-metrics'
export type { MetricSeries, SeriesPoint } from '@/lib/metrics/metric-series'
export type { PlayerTrendResult } from '@/lib/players/player-trend'
export type { SLOResult } from '@/lib/metrics/slo'
export type { RankingItem, RankingMetric, RankingResult } from '@/lib/instances/ranking'

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
    // 跨服回切先呈现缓存指标后台刷新，避免 KPI 区闪空。
    gcTime: INSTANCE_QUERY_GC_TIME_MS,
    placeholderData: keepPreviousData,
  })
}

// === 时序历史指标（：/metrics/series、/metrics/overview） ===

/** 曲线上一点：raw 档 avg=min=max；缺测为 null（断点）。 */

/** 一条历史序列。scope=instance 含分世界时 world 非空。 */

export interface MetricSeriesResponse {
  resolution: string
  from: string
  to: string
  series: MetricSeries[]
}

/** 节点关联的共享 Bot Worker 运行时历史响应。 */
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
 * 聚合粒度档位（ADR-013 三档降采样）：
 * auto=按区间自动选档；raw=原始 30s；5m/1h=对应降采样卷积档。
 */
export type MetricResolution = 'auto' | 'raw' | '5m' | '1h'

/** 节点/实例历史曲线（增自定义粒度）。按区间自动选档（或显式 resolution），30s 轮询。 */
export function useMetricSeries(params: {
  scope: 'node' | 'instance'
  targetId: string
  range: MetricRange
  /** 
   * 自定义时间窗（RFC3339）。**同时提供时优先于 `range`**——
   * 后端 `parseMetricRange` 早就在支持 `from`/`to`，只是前端此前只发预设。
   * 用户要「自己选时间范围」时传这两个，`range` 仍必填（作为缺省档位与 queryKey 的一部分）。
   */
  from?: string
  to?: string
  /** 指标键过滤（逗号合并下发）；不传返回该目标全部序列。 */
  metrics?: string[]
  /** 聚合粒度档位；auto/留空=按区间自动选档。 */
  resolution?: MetricResolution
  enabled?: boolean
}) {
  const { scope, targetId, range, from, to, metrics, resolution, enabled = true } = params
  const custom = !!from && !!to
  return useQuery({
    // 自定义窗口进 queryKey：否则切换时间范围会命中同一份缓存、图表不刷新。
    queryKey: [
      'metricSeries',
      scope,
      targetId,
      custom ? `${from}~${to}` : range,
      metrics?.join(',') ?? '',
      resolution ?? 'auto',
    ],
    queryFn: async () => {
      const q = new URLSearchParams({ scope, targetId })
      // 有自定义窗口就只发 from/to：两者同时下发时后端以 from/to 优先，这里显式二选一更清晰。
      if (custom) {
        q.set('from', from!)
        q.set('to', to!)
      } else {
        q.set('range', range)
      }
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

/** 被剔除的目标：无实例访问权（forbidden）或 UUID 不存在（not_found）。 */
export interface SkippedTarget {
  targetId: string
  reason: 'forbidden' | 'not_found'
}

/** 
 * 批量序列响应：整批共用 resolution/from/to；series 按 targetId(UUID) 分组，
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
 * 批量多实例历史曲线（：消 NodeInstanceCompare 的 N+1）。一次请求返回各目标序列，
 * 30s 轮询。targetIds 为空时不请求。v1 仅 scope=instance（对比场景只有实例维度有 N+1）。
 */
export function useMetricSeriesBatch(params: {
  scope: 'instance'
  targetIds: string[]
  range: MetricRange
  /** 指标键过滤（逗号合并下发）；不传返回各目标全部序列。 */
  metrics?: string[]
  /** 聚合粒度档位；auto/留空=按区间自动选档。 */
  resolution?: MetricResolution
  enabled?: boolean
  /** 
   * 刷新令牌：参与查询键，值变化即视为一次新请求。
   *
   * 【为什么需要】调用方按节拍刷新同一批目标时，`targetIds` 不变 → 查询键不变 → React Query
   * 直接命中缓存、连 `refetchInterval` 都不会另起一次（键相同就是同一条查询）。有了令牌，
   * 「同一目标集合重新拉一遍」才有明确的表达方式；不传时恒为 0，既有调用点行为不变。
   */
  refreshToken?: number
}) {
  const {
    scope,
    targetIds,
    range,
    metrics,
    resolution,
    enabled = true,
    refreshToken = 0,
  } = params
  const targetsKey = targetIds.join(',')
  return useQuery({
    queryKey: [
      'metricSeriesBatch',
      scope,
      targetsKey,
      range,
      metrics?.join(',') ?? '',
      resolution ?? 'auto',
      refreshToken,
    ],
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
    // 目标集合变化（如 /instances 可见窗口滚动）会改查询键：保留上一批数据避免整表指标闪空。
    placeholderData: keepPreviousData,
  })
}

export interface OverviewTotals {
  nodeCount: number
  onlineNodeCount: number
  runningInstances: number
  cpuPct: number
  /** 在线节点负载利用率均值（load1/核数*100）。 */
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

/** 总览页跨节点聚合：当前总量 + 聚合曲线（增自定义粒度）。可见页每 10 秒轮询。 */
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

/** 平台管理员首页的有界全景观测读模型。 */
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

/** 健康墙单格分级。 */
// 健康墙类型已回迁应用侧（迁包已撤销，见 ADR-103）；此处转出，调用点零改动。
import type { HealthWallResponse, HealthWallSort } from '@/lib/metrics/health-wall-types'
export type { HealthLevel, HealthWallNode, HealthWallResponse, HealthWallSort } from '@/lib/metrics/health-wall-types'

/** 集群健康墙：只读快照一次查询给全，不触发 Worker RPC。 */
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

/** CPU 时间构成占比（0..100）。后端已按百分比下发，前端直接当百分比用。 */
export interface ResourceAttributionCPUBreakdown {
  user: number
  nice: number
  system: number
  idle: number
  iowait: number
  irq: number
  softirq: number
  steal: number
}

/** 一个挂载点的磁盘用量；后端已按使用率降序。 */
export interface ResourceAttributionMount {
  mountpoint: string
  totalBytes: number
  usedBytes: number
  usedPct: number
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
  /* 
主机级观测（后端 `ManagedResourceAttribution` 同步下发）。
    全部可选且可 null，且 null 与 0 语义不同：
    - 缺失 / null = 老 Worker 未上报 → 前端渲染 `--`
    - 0 = 真实读数就是 0
    把「不知道」画成 0 会让运维误判机器空闲。
  */
  /** 已按核数归一的负载（与 `loadPct` 同口径，便于同图对比）。 */
  load5Pct?: number | null
  load15Pct?: number | null
  /** 活动进程/线程数；总数。 */
  procsRunning?: number | null
  procsTotal?: number | null
  cpuBreakdown?: ResourceAttributionCPUBreakdown | null
  mounts?: ResourceAttributionMount[]
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
  /* 
整机 top-N 进程。与 `topProcesses` 是两个口径，**不可混为一谈**：
    - `topProcesses`：只覆盖受管实例的进程树（我们知道它是谁）
    - `hostTopProcesses`：整机按 CPU 排序前几名（含系统进程、别人的进程）
    运维判断「这台机器为什么慢」要看后者。

    隐私边界：无 `cmdline`（与后端 `model.HostProcessSnapshot` 一致），只给进程名。
  */
  hostTopProcesses?: ResourceAttributionHostProcess[]
}

/** 整机 top-N 进程的一项。 */
export interface ResourceAttributionHostProcess {
  nodeId: number
  nodeName: string
  pid: number
  comm: string
  cpuPct: number
  rssBytes: number
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
  /** 运行用户——粗粒度的「位置」线索（不同实例常用不同系统用户跑）。 */
  user: string
  /** 命令行摘要：含 jar 路径 / 工作目录线索，是最接近「位置」的字段。 */
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

/** 
 * 进程 TOPN。
 *
 * `at`（RFC3339，可选）取「该时刻之前最近一拍」的快照——用于在历史曲线上悬浮时
 * 回答「那一刻是谁在吃 CPU/内存/磁盘」。不传则取最新（原有行为）。
 *
 * 【数据边界】只覆盖**受管实例进程**（游戏服本体及其包装器），不是任意系统进程；
 * 快照与原始指标同档保留 48h，更早的时刻查不到。
 */
export function useProcessTop(params: {
  instanceId?: number
  nodeId?: string
  sort?: 'cpu' | 'memory' | 'io'
  limit?: number
  enabled?: boolean
  /** 指定时刻（RFC3339）；省略=最新。 */
  at?: string
}) {
  const { instanceId, nodeId, sort = 'cpu', limit = 10, enabled = true, at } = params
  return useQuery({
    // at 进 queryKey：不同时刻是不同结果，不进 key 会命中同一份缓存。
    queryKey: ['processTop', instanceId ?? '', nodeId ?? '', sort, limit, at ?? 'latest'],
    queryFn: async () => {
      const q = new URLSearchParams({ sort, limit: String(limit) })
      if (instanceId) q.set('instanceId', String(instanceId))
      if (nodeId) q.set('nodeId', nodeId)
      if (at) q.set('at', at)
      const { data } = await api.get<ProcessTopItem[]>(`/metrics/processes/top?${q.toString()}`)
      return data
    },
    enabled,
    // 悬浮场景要快：有 at 时不轮询，避免每次悬浮都重新请求。
    refetchInterval: enabled && !at ? 30_000 : false,
    ...(at ? { staleTime: 15_000 } : {}),
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

// ===464/465/469：SLO 可用性 / 容量预测 / 性能归因 / 跨实例排行与玩家趋势 ===

/** 归因因子：与目标的 Pearson 相关、归一化贡献权重。 */
export interface AttributionFactor {
  metricKey: string
  label: string
  correlation: number
  weight: number
  note?: string
}

/** 性能归因结果。status=insufficient 时 factors 为空，不伪造排序。 */
export interface AttributionResult {
  target: string
  window: { from: string; to: string }
  status: 'ok' | 'insufficient'
  tldr: string
  factors: AttributionFactor[]
  samples: number
}

/** 
 * 实例性能归因：按需触发（enabled=false 时不请求），不做常驻轮询——
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
/** 排行一项。 */

/** 排行结果。scoped=true 表示非管理员受限视图。 */

/** 跨实例全局排行。30s 轮询与采样节奏对齐。 */
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

/** 玩家在线趋势结果：全网合计曲线 + 24 时段分布 + 峰值/日均。 */

/** 
 * 玩家在线趋势与时段分析。tz 缺省时后端按服务器本地时区分桶，
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

/** 可用性/SLO 结果。MTTR/MTBF 为 null 表示无已恢复故障/无故障（不是 Infinity）。 */

/** 
 * 平台/实例/节点可用性与 SLO 聚合。platform 为非管理员的可见实例汇总；
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

/** 单指标容量外推结果。Exhaust* 为 null 表示无增长/样本不足，不伪造预测。 */
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

/** 容量预测响应。 */
export interface CapacityForecastResponse {
  forecasts: ForecastResult[]
}

/** 实例/节点容量耗尽预测。60s 轮询。 */
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

/** 进程 TOPN 单项：PID / 进程名 / CPU / 内存 / 磁盘读写 / 归属用户 / 命令行。 */


