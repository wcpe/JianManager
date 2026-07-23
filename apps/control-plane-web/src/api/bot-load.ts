/**
 * FR-372 压测运行观测 API hooks。
 * 后端 SSE/报告 HTTP 若未就绪时由 devmock 仿真；真实 404/503 优雅降级。
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api, { ensureFreshToken } from '@/api/client'
import type {
  BotLoadFailure,
  BotLoadMetricPoint,
  BotLoadRetryResult,
  BotLoadRunBot,
  BotLoadRunEventPage,
  BotLoadRunV2,
  Page,
} from '@/lib/bot-load/types'
import { downloadBlob, reportFilename } from '@/lib/bot-load/report'

const ROOT = '/bots/stress-sessions'

export function botLoadQueryKeys(runId: number | string) {
  return {
    detail: ['bots', 'stress-sessions', runId, 'v2'] as const,
    metrics: (params?: Record<string, unknown>) =>
      ['bots', 'stress-sessions', runId, 'metrics', params] as const,
    bots: (params?: Record<string, unknown>) =>
      ['bots', 'stress-sessions', runId, 'bots', params] as const,
    failures: (params?: Record<string, unknown>) =>
      ['bots', 'stress-sessions', runId, 'failures', params] as const,
    events: (params?: Record<string, unknown>) =>
      ['bots', 'stress-sessions', runId, 'events', params] as const,
  }
}

/** 运行详情（V2 优先；schemaVersion=1 由页面降级提示）。 */
export function useBotLoadRun(id: number | string | null) {
  return useQuery({
    queryKey: botLoadQueryKeys(id ?? 0).detail,
    queryFn: async () => {
      const { data } = await api.get<BotLoadRunV2>(`${ROOT}/${id}`)
      return data
    },
    enabled: id != null && id !== '',
  })
}

export function useBotLoadMetrics(
  id: number | string | null,
  params?: { from?: string; to?: string; resolution?: string },
) {
  return useQuery({
    queryKey: botLoadQueryKeys(id ?? 0).metrics(params),
    queryFn: async () => {
      const { data } = await api.get<{
        items: BotLoadMetricPoint[]
        from: string
        to: string
        resolution: string
      }>(`${ROOT}/${id}/metrics`, { params })
      return data
    },
    enabled: id != null && id !== '',
  })
}

export function useBotLoadRunBots(
  id: number | string | null,
  params?: {
    page?: number
    pageSize?: number
    q?: string
    status?: string
    executorNodeId?: number | string
    stepId?: string
    errorCode?: string
  },
) {
  return useQuery({
    queryKey: botLoadQueryKeys(id ?? 0).bots(params),
    queryFn: async () => {
      const { data } = await api.get<Page<BotLoadRunBot>>(`${ROOT}/${id}/bots`, { params })
      return data
    },
    enabled: id != null && id !== '',
  })
}

export function useBotLoadFailures(
  id: number | string | null,
  params?: {
    page?: number
    pageSize?: number
    category?: string
    errorCode?: string
    botUuid?: string
    executorNodeId?: number | string
    stepId?: string
    from?: string
    to?: string
  },
) {
  return useQuery({
    queryKey: botLoadQueryKeys(id ?? 0).failures(params),
    queryFn: async () => {
      const { data } = await api.get<Page<BotLoadFailure>>(`${ROOT}/${id}/failures`, { params })
      return data
    },
    enabled: id != null && id !== '',
  })
}

export function useBotLoadEvents(
  id: number | string | null,
  params?: {
    page?: number
    pageSize?: number
    type?: string
    eventId?: string
    actionRunId?: string
    botUuid?: string
    executorNodeId?: number | string
    stepId?: string
    from?: string
    to?: string
    snapshotEventId?: string
  },
) {
  return useQuery({
    queryKey: botLoadQueryKeys(id ?? 0).events(params),
    queryFn: async () => {
      const { data } = await api.get<BotLoadRunEventPage>(`${ROOT}/${id}/events`, { params })
      return data
    },
    enabled: id != null && id !== '',
  })
}

export function useStopBotLoadRun() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, reason }: { id: number | string; reason?: string }) => {
      const { data } = await api.post<BotLoadRunV2>(`${ROOT}/${id}/stop`, { reason })
      return data
    },
    onSuccess: (_d, { id }) => {
      qc.invalidateQueries({ queryKey: ['bots', 'stress-sessions'] })
      qc.invalidateQueries({ queryKey: botLoadQueryKeys(id).detail })
    },
  })
}

export function useCancelBotLoadRun() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, reason }: { id: number | string; reason?: string }) => {
      const { data } = await api.post<BotLoadRunV2>(`${ROOT}/${id}/cancel`, { reason })
      return data
    },
    onSuccess: (_d, { id }) => {
      qc.invalidateQueries({ queryKey: ['bots', 'stress-sessions'] })
      qc.invalidateQueries({ queryKey: botLoadQueryKeys(id).detail })
    },
  })
}

export function useRetryBotLoadFailed() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({
      id,
      requestId,
      botUuids,
      errorCodes,
      fromStepId,
    }: {
      id: number | string
      requestId: string
      botUuids?: string[]
      errorCodes?: string[]
      fromStepId?: string
    }) => {
      const { data } = await api.post<BotLoadRetryResult>(`${ROOT}/${id}/retry-failed`, {
        requestId,
        botUuids,
        errorCodes,
        fromStepId,
      })
      return data
    },
    onSuccess: (_d, { id }) => {
      qc.invalidateQueries({ queryKey: botLoadQueryKeys(id).bots() })
      qc.invalidateQueries({ queryKey: botLoadQueryKeys(id).failures() })
      qc.invalidateQueries({ queryKey: botLoadQueryKeys(id).detail })
    },
  })
}

/** 下载 JSON/CSV 报告（blob）。后端未挂端点时会 reject，UI 展示错误。 */
export function useDownloadBotLoadReport() {
  return useMutation({
    mutationFn: async ({
      id,
      runUuid,
      format,
    }: {
      id: number | string
      runUuid: string
      format: 'json' | 'csv'
    }) => {
      const resp = await api.get(`${ROOT}/${id}/report`, {
        params: { format },
        responseType: 'blob',
      })
      const blob = resp.data as Blob
      downloadBlob(blob, reportFilename(runUuid, format))
      return { format, size: blob.size }
    },
  })
}

/** 构造会话 SSE URL（供 EventClient 使用）。 */
export function botLoadStreamUrl(runId: number | string): string {
  const base = api.defaults.baseURL || ''
  return `${base}${ROOT}/${runId}/stream`
}

export { ensureFreshToken }
