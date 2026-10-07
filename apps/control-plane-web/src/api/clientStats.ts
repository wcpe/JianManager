import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 客户端分发观测契约（FR-217 / FR-428）已归包，双侧共用（ADR-097）；对外保留原导出名，调用点无需改动。
 */
export type {
  ClientDistObservabilitySummary,
  ClientDistObservabilityCompare,
} from '@jianmanager/ui/lib/client-dist-observability-contracts'

/**
 * 客户端分发统计契约（FR-095）已归包，双侧共用（ADR-097）；对外保留原导出名，调用点无需改动。
 */
export type {
  StatsDayPoint,
  StatsVersion,
  StatsResult,
  StatsIP,
  ClientDistStats,
  ClientDistDistItem,
  ClientDistSeriesPoint,
  ClientDistObservability,
  ClientDistWindow,
} from '@jianmanager/ui/lib/client-dist-stats-contracts'
import type {
  ClientDistStats,
  ClientDistWindow,
  ClientDistObservability,
} from '@jianmanager/ui/lib/client-dist-stats-contracts'


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
