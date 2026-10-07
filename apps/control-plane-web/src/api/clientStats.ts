import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 客户端分发观测契约（FR-217 / FR-428）已归包，双侧共用（ADR-097）；对外保留原导出名，调用点无需改动。
 */
export type {
  ClientDistObservabilitySummary,
  ClientDistObservabilityCompare,
} from '@jianmanager/ui/lib/client-dist-observability-contracts'
import type {
  ClientDistObservabilitySummary,
  ClientDistObservabilityCompare,
} from '@jianmanager/ui/lib/client-dist-observability-contracts'

/** 下载量按日点（FR-095）。 */
export interface StatsDayPoint {
  day: string
  requests: number
  bytes: number
}

/** 版本分布项。 */
export interface StatsVersion {
  version: number
  requests: number
}

/** 请求结果分布项（success|failure，来自 client_dist_events）。 */
export interface StatsResult {
  result: string
  count: number
}

/** 来源 IP 分布项。 */
export interface StatsIP {
  ip: string
  count: number
}

/** 分发统计复合视图（FR-095，来自 FR-093/094/092 聚合）。 */
export interface ClientDistStats {
  channelId: string
  days: number
  downloads: StatsDayPoint[]
  versions: StatsVersion[]
  results: StatsResult[]
  successRate: number
  failureRate: number
  rollbackRate: number
  activeMachines: number
  topIps: StatsIP[]
}

/** 频道分发统计（按频道 + 天数窗口）。 */
export function useClientStats(channelId: string | null | undefined, days: number, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['client-dist-stats', channelId, days],
    queryFn: async () => {
      const { data } = await api.get<ClientDistStats>('/client-dist/stats', {
        params: { channelId, days },
      })
      return data
    },
    enabled: (options?.enabled ?? true) && channelId !== null,
  })
}

// === 客户端分发观测（FR-217，消费方含 FR-220 平台统计页） ===

/** 版本/平台/滞后分布项（区间内跨桶合并）。 */
export interface ClientDistDistItem {
  version?: number
  os?: string
  lag?: number
  count: number
}

/** 小时桶时序点（series[]，按 ts 升序；跨频道时同小时合并；缺数小时无点）。FR-218 分发监控页画时序趋势消费。 */
export interface ClientDistSeriesPoint {
  ts: string
  manifestPulls: number
  artifactPulls: number
  downloadBytes: number
  activeMachines: number
  updateTotal: number
  updateSuccess: number
  updateFailStatic: number
  updateRolledBack: number
  updateError: number
}

/**
 * 客户端分发观测复合视图（FR-217，见 ADR-049）。
 * 平台统计页（FR-220）只取 summary + 分布；分发监控页（FR-218）额外画 series 时序趋势。
 */
export interface ClientDistObservability {
  channelId: string
  from: string
  to: string
  series: ClientDistSeriesPoint[]
  summary: ClientDistObservabilitySummary
  /** FR-428：前一等长窗口同环比基数（缺省=后端未升级，前端不渲染同环比）。 */
  compare?: ClientDistObservabilityCompare
  versionDist: ClientDistDistItem[]
  platformDist: ClientDistDistItem[]
  lagDist: ClientDistDistItem[]
}

/**
 * 客户端分发观测（FR-217）：省略 channelId=跨频道总。
 * FR-425/428：窗口支持预设 range 或任意 from/to（RFC3339，后端 parseObsRange 已支持）；summary.compare=前一等长窗口同环比。
 * **平台管理员**端点：非管理员返 403 → 调用方据 query error 局部降级（retry:false 让 403 快速失败、不重试）。
 */
export type ClientDistWindow = { range: string } | { from: string; to: string }

export function useClientDistObservability(params: { channelId?: string; window?: ClientDistWindow; range?: string; enabled?: boolean }) {
  const { channelId, window, range = '7d', enabled = true } = params
  const from = window && 'from' in window ? window.from : undefined
  const to = window && 'to' in window ? window.to : undefined
  const effRange = window && 'range' in window ? window.range : range
  return useQuery({
    queryKey: ['client-dist-observability', channelId ?? 'all', effRange, from ?? '', to ?? ''],
    queryFn: async () => {
      const { data } = await api.get<ClientDistObservability>('/client-dist/observability', {
        params: { ...(channelId ? { channelId } : {}), ...(from && to ? { from, to } : { range: effRange }) },
      })
      return data
    },
    enabled,
    retry: false,
  })
}
