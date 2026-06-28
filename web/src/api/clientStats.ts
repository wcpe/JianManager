import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

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

/** 更新结果分布项（success|fail-static|rolled-back|error）。 */
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
  rollbackRate: number
  activeMachines: number
  topIps: StatsIP[]
}

/** 频道分发统计（按频道 + 天数窗口）。 */
export function useClientStats(channelId: string | null, days: number) {
  return useQuery({
    queryKey: ['client-dist-stats', channelId, days],
    queryFn: async () => {
      const { data } = await api.get<ClientDistStats>('/client-dist/stats', {
        params: { channelId, days },
      })
      return data
    },
    enabled: !!channelId,
  })
}

// === 客户端分发观测（FR-217，消费方含 FR-220 平台统计页） ===

/** 观测汇总标量（区间内跨频道/单频道合并；率为 0~1 小数）。 */
export interface ClientDistObservabilitySummary {
  manifestPulls: number
  artifactPulls: number
  downloadBytes: number
  casHit: number
  casMiss: number
  updateTotal: number
  updateSuccess: number
  updateFailStatic: number
  updateRolledBack: number
  updateError: number
  successRate: number
  failStaticRate: number
  rollbackRate: number
  casHitRate: number
  activeMachines: number
  /** 区间在明细保留窗(14d)内=精确去重独立数 true；窗外=各桶人次求和近似 false（ADR-049）。 */
  activeMachinesExact: boolean
}

/** 版本/平台/滞后分布项（区间内跨桶合并）。 */
export interface ClientDistDistItem {
  version?: number
  os?: string
  lag?: number
  count: number
}

/** 客户端分发观测复合视图（FR-217，见 ADR-049）。平台统计页只取 summary + 分布，不画 series 时序（那归 FR-218）。 */
export interface ClientDistObservability {
  channelId: string
  from: string
  to: string
  summary: ClientDistObservabilitySummary
  versionDist: ClientDistDistItem[]
  platformDist: ClientDistDistItem[]
  lagDist: ClientDistDistItem[]
}

/**
 * 客户端分发观测（FR-217）：省略 channelId=跨频道总。
 * **平台管理员**端点：非管理员返 403 → 调用方据 query error 局部降级（retry:false 让 403 快速失败、不重试）。
 */
export function useClientDistObservability(params: { channelId?: string; range: string; enabled?: boolean }) {
  const { channelId, range, enabled = true } = params
  return useQuery({
    queryKey: ['client-dist-observability', channelId ?? 'all', range],
    queryFn: async () => {
      const { data } = await api.get<ClientDistObservability>('/client-dist/observability', {
        params: { ...(channelId ? { channelId } : {}), range },
      })
      return data
    },
    enabled,
    retry: false,
  })
}
