import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/** 客户端运行态行（FR-265，来自 client_runtime_states）。 */
export interface ClientRuntimeState {
  id: number
  channelId: string
  machineId: string
  ip: string
  platform: string
  javaVersion: string
  launcher: string
  coreVersion: string
  localVersion: number
  firstSeenAt: string
  lastHeartbeatAt: string
  createdAt?: string
  updatedAt?: string
}

/** 客户端 Tab KPI：启动心跳 + 更新结果遥测。 */
export interface ClientRuntimeSummary {
  recentStarted: number
  todayStarted: number
  recentStarts: number
  todayStarts: number
  updateSuccessRate: number
  updateFailureRate: number
}

export interface RuntimeVersionCount {
  version: number
  count: number
}

export interface RuntimeStringCount {
  value: string
  count: number
}

export interface RuntimeLagCount {
  lag: number
  count: number
}

export interface RuntimeUpdateSeriesPoint {
  ts: string
  success: number
  failStatic: number
  rolledBack: number
  error: number
}

/** 客户端运行态聚合响应（FR-265）。 */
export interface ClientRuntimeOverview {
  channelId: string
  from: string
  to: string
  summary: ClientRuntimeSummary
  items: ClientRuntimeState[]
  runtimeVersionDist: RuntimeVersionCount[]
  coreVersionDist: RuntimeStringCount[]
  platformDist: RuntimeStringCount[]
  launcherDist: RuntimeStringCount[]
  lagDist: RuntimeLagCount[]
  updateResultSeries: RuntimeUpdateSeriesPoint[]
}

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
