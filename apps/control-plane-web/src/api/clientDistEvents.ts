import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/** 
 * 客户端分发拉取/下载明细事件（追踪观测）。
 * 机器码/IP 客户端可伪造、不可信，仅追踪统计。`errCode` 成功事件为空、失败事件填语义错误码。
 */

 /** 
  * 客户端分发事件契约已回迁应用侧，双侧共用；对外保留原导出名，调用点无需改动。
  */
 export type { ClientDistEvent, ClientDistEventFilter, ClientDistEventSearchFilter, ClientDistEventPage, ClientDistEventDetail, ClientDistRealtimeSummary, ClientDistRatePoint, ClientDistRecentError, ClientDistRealtime, ClientDistErrorCount, ClientDistFailureSample, ClientDistErrorSummary } from '@/lib/client-dist/client-dist-events-contracts'
// StatsIP 由统计契约定义（此前本文件有一份重复定义，已去重）；对外仍从本模块可用。
export type { StatsIP } from '@/lib/client-dist/client-dist-stats-contracts'
import type { ClientDistEventSearchFilter, ClientDistEventPage, ClientDistEventDetail, ClientDistRealtime, ClientDistErrorSummary } from '@/lib/client-dist/client-dist-events-contracts'
/** 分页检索分发事件，支持运行态维度联动过滤。 */
export function useClientDistEventSearch(filter: ClientDistEventSearchFilter) {
  const {
    channelId,
    machineId,
    ip,
    range,
    from,
    to,
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
      range ?? '',
      from ?? '',
      to ?? '',
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
          range,
          from,
          to,
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

/**
 * 近实时面板的轮询间隔（R35）。
 *
 * 实时监控/错误摘要此前只有 `staleTime` 到期判定，不是定时请求：
 * 用户把这一页挂在前台不动，数字就永远停在打开那一刻——而页面上写的是「近 1h」「近 24h」。
 * 30s 的取值与告警列表（alerts.ts）同一量级：足够看出趋势，又不会把 CP 当成采样点。
 */
export const CLIENT_DIST_REALTIME_REFETCH_MS = 30_000

/** 查询错误码 TopN 与最近失败样例。 */
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
    // 标签页在后台时不轮询：切回来由 focus 触发一次刷新即可。
    refetchInterval: enabled ? CLIENT_DIST_REALTIME_REFETCH_MS : false,
    refetchIntervalInBackground: false,
    retry: false,
  })
}

/** 查询近实时分发请求聚合（R35：带 30s 轮询，页面前台停留时数字会自己走）。 */
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
    refetchInterval: enabled ? CLIENT_DIST_REALTIME_REFETCH_MS : false,
    refetchIntervalInBackground: false,
    retry: false,
  })
}

function compactParams(input: Record<string, unknown>) {
  return Object.fromEntries(Object.entries(input).filter(([, v]) => v !== undefined && v !== null && v !== ''))
}
