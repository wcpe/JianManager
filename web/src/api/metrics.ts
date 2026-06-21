import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'
import type { MetricRange } from '@/components/charts/RangePicker'

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

/** 节点/实例历史曲线（FR-060）。按区间自动选档，30s 轮询。 */
export function useMetricSeries(params: {
  scope: 'node' | 'instance'
  targetId: string
  range: MetricRange
  /** 指标键过滤（逗号合并下发）；不传返回该目标全部序列。 */
  metrics?: string[]
  enabled?: boolean
}) {
  const { scope, targetId, range, metrics, enabled = true } = params
  return useQuery({
    queryKey: ['metricSeries', scope, targetId, range, metrics?.join(',') ?? ''],
    queryFn: async () => {
      const q = new URLSearchParams({ scope, targetId, range })
      if (metrics?.length) q.set('metrics', metrics.join(','))
      const { data } = await api.get<MetricSeriesResponse>(`/metrics/series?${q.toString()}`)
      return data
    },
    enabled: enabled && !!targetId,
    refetchInterval: 30_000,
  })
}

export interface OverviewTotals {
  nodeCount: number
  onlineNodeCount: number
  runningInstances: number
  cpuPct: number
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

/** 总览页跨节点聚合：当前总量 + 聚合曲线（FR-060）。30s 轮询。 */
export function useMetricOverview(range: MetricRange) {
  return useQuery({
    queryKey: ['metricOverview', range],
    queryFn: async () => {
      const { data } = await api.get<MetricOverviewResponse>(`/metrics/overview?range=${range}`)
      return data
    },
    refetchInterval: 30_000,
  })
}
