import { keepPreviousData, useQuery } from '@tanstack/react-query'
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

/** 被剔除的目标（FR-334）：无实例访问权（forbidden）或 UUID 不存在（not_found）。 */
export interface SkippedTarget {
  targetId: string
  reason: 'forbidden' | 'not_found'
}

/**
 * 批量序列响应（FR-334）：整批共用 resolution/from/to；series 按 targetId(UUID) 分组，
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
 * 批量多实例历史曲线（FR-334：消 NodeInstanceCompare 的 N+1）。一次请求返回各目标序列，
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

/** 总览页跨节点聚合：当前总量 + 聚合曲线（FR-060；FR-221 增自定义粒度）。30s 轮询。 */
export function useMetricOverview(range: MetricRange, resolution?: MetricResolution) {
  return useQuery({
    queryKey: ['metricOverview', range, resolution ?? 'auto'],
    queryFn: async () => {
      const q = new URLSearchParams({ range })
      if (resolution && resolution !== 'auto') q.set('resolution', resolution)
      const { data } = await api.get<MetricOverviewResponse>(`/metrics/overview?${q.toString()}`)
      return data
    },
    refetchInterval: 30_000,
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
