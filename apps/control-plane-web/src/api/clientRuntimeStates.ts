import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 客户端运行态契约（FR-265）已归包，双侧共用（ADR-097）；对外保留原导出名，调用点无需改动。
 */
export type {
  ClientRuntimeState,
  ClientRuntimeSummary,
  RuntimeVersionCount,
  RuntimeStringCount,
  RuntimeLagCount,
  RuntimeUpdateSeriesPoint,
  ClientRuntimeOverview,
} from '@jianmanager/ui/lib/client-runtime-contracts'
import type { ClientRuntimeOverview } from '@jianmanager/ui/lib/client-runtime-contracts'

/** 查询客户端运行态聚合：省略 channelId=跨频道总。 */
export function useClientRuntimeOverview(params: { channelId?: string; range: string; enabled?: boolean }) {
  const { channelId, range, enabled = true } = params
  return useQuery({
    queryKey: ['client-runtime-overview', channelId ?? 'all', range],
    queryFn: async () => {
      const { data } = await api.get<ClientRuntimeOverview>('/client-dist/clients', {
        params: { ...(channelId ? { channelId } : {}), range },
      })
      return data
    },
    enabled,
    retry: false,
  })
}
