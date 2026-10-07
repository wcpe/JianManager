import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 客户端分发拉取/下载明细事件（FR-093 追踪 + FR-249/FR-265 观测）。
 * 机器码/IP 客户端可伪造、不可信，仅追踪统计。`errCode` 成功事件为空、失败事件填语义错误码。
 */

 /**
  * 客户端分发事件契约已归包，双侧共用（ADR-097）；对外保留原导出名，调用点无需改动。
  */
 export type {
   ClientDistEvent,
   ClientDistEventFilter,
   ClientDistEventSearchFilter,
   ClientDistEventPage,
   ClientDistEventDetail,
   ClientDistRealtimeSummary,
   ClientDistRatePoint,
  ClientDistRecentError,
  ClientDistRealtime,
   ClientDistErrorCount,
   ClientDistFailureSample,
   ClientDistErrorSummary,
} from '@jianmanager/ui/lib/client-dist-events-contracts'
// StatsIP 由统计契约定义（此前本文件有一份重复定义，已去重）；对外仍从本模块可用。
export type { StatsIP } from '@jianmanager/ui/lib/client-dist-stats-contracts'
import type {
   ClientDistEventSearchFilter,
   ClientDistEventPage,
   ClientDistEventDetail,
   ClientDistRealtime,
   ClientDistErrorSummary,
 } from '@jianmanager/ui/lib/client-dist-events-contracts'
/** 分页检索分发事件（FR-265），支持运行态维度联动过滤。 */
export function useClientDistEventSearch(filter: ClientDistEventSearchFilter) {
  const {
    channelId,
    machineId,
    ip,
    kind,
    outcome,
    errCode,
    version,
    artifactSha,
    runtimeVersion,
    coreVersion,
    platform,
    lag,
    page = 1,
    pageSize = 100,
    enabled = true,
  } = filter
  return useQuery({
    queryKey: [
      'client-dist-events-search',
      channelId ?? 'all',
      machineId ?? '',
      ip ?? '',
      kind ?? 'all',
      outcome ?? 'all',
      errCode ?? '',
      version ?? '',
      artifactSha ?? '',
      runtimeVersion ?? '',
      coreVersion ?? '',
      platform ?? '',
      lag ?? '',
      page,
      pageSize,
    ],
    queryFn: async () => {
      const { data } = await api.get<ClientDistEventPage>('/client-dist/events/search', {
        params: compactParams({
          channelId,
          machineId,
          ip,
          kind,
          outcome,
          errCode,
          version,
          artifactSha,
          runtimeVersion,
          coreVersion,
          platform,
          lag,
          page,
          pageSize,
        }),
      })
      return data
    },
    enabled,
    retry: false,
  })
}

/** 查询单条分发请求脱敏详情。 */
export function useClientDistEventDetail(id: number | null, enabled = true) {
  return useQuery({
    queryKey: ['client-dist-event-detail', id],
    queryFn: async () => {
      const { data } = await api.get<ClientDistEventDetail>(`/client-dist/events/${id}`)
      return data
    },
    enabled: enabled && !!id,
    retry: false,
  })
}

/** 查询错误码 TopN 与最近失败样例（FR-357）。 */
export function useClientDistErrorSummary(params: { channelId?: string; range: string; enabled?: boolean }) {
  const { channelId, range, enabled = true } = params
  return useQuery({
    queryKey: ['client-dist-error-summary', channelId ?? 'all', range],
    queryFn: async () => {
      const { data } = await api.get<ClientDistErrorSummary>('/client-dist/error-summary', {
        params: compactParams({ channelId, range }),
      })
      return data
    },
    enabled,
    retry: false,
  })
}

/** 查询近实时分发请求聚合（FR-265）。 */
export function useClientDistRealtime(params: { channelId?: string; enabled?: boolean }) {
  const { channelId, enabled = true } = params
  return useQuery({
    queryKey: ['client-dist-realtime', channelId ?? 'all'],
    queryFn: async () => {
      const { data } = await api.get<ClientDistRealtime>('/client-dist/realtime', {
        params: compactParams({ channelId }),
      })
      return data
    },
    enabled,
    retry: false,
  })
}

function compactParams(input: Record<string, unknown>) {
  return Object.fromEntries(Object.entries(input).filter(([, v]) => v !== undefined && v !== null && v !== ''))
}
