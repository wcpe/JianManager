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
