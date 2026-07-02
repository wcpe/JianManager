import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 客户端分发拉取/下载明细事件（FR-093 追踪 + FR-249/FR-265 观测）。
 * 机器码/IP 客户端可伪造、不可信，仅追踪统计。`errCode` 成功事件为空、失败事件填语义错误码。
 */
export interface ClientDistEvent {
  id: number
  channelId: string
  machineId: string
  ip: string
  /** manifest | artifact。 */
  kind: string
  version: number
  artifactSha: string
  bytes: number
  /** HTTP 状态码（200/206/304/401/404/503…）。 */
  status: number
  /** 语义错误码（FR-249）：成功为空；失败如 INVALID_CLIENT_KEY/NO_LATEST_VERSION/ARTIFACT_NOT_FOUND/SIGN_KEY_NOT_CONFIGURED。 */
  errCode: string
  /** 可读错误原因（FR-265）。 */
  errReason?: string
  /** 请求方法与脱敏路径（不含 query）。 */
  method?: string
  path?: string
  /** 响应 ETag 快捷列。 */
  etag?: string
  durationMs: number
  createdAt: string
}

/** 分发事件检索过滤（FR-249）：空/undefined 字段不约束。 */
export interface ClientDistEventFilter {
  channelId?: string
  machineId?: string
  ip?: string
  /** manifest | artifact。 */
  kind?: string
  /** 成功/失败维度：success（0<status<400）| failure（status>=400）| 空（全部）。 */
  outcome?: 'success' | 'failure' | ''
  errCode?: string
  version?: number
  limit?: number
  /** 平台管理员端点门控；非管理员不发起请求。 */
  enabled?: boolean
}

export interface ClientDistEventSearchFilter extends ClientDistEventFilter {
  artifactSha?: string
  runtimeVersion?: number
  coreVersion?: string
  platform?: string
  lag?: number
  page?: number
  pageSize?: number
}

export interface ClientDistEventPage {
  items: ClientDistEvent[]
  page: number
  pageSize: number
  total: number
}

export interface ClientDistEventDetail extends ClientDistEvent {
  requestHeaders: Record<string, string>
  responseHeaders: Record<string, string>
}

export interface ClientDistRealtimeSummary {
  manifestPulls: number
  artifactPulls: number
  errorRequests: number
  activeMachines: number
}

export interface ClientDistRatePoint {
  ts: string
  manifest: number
  artifact: number
  error: number
}

export interface ClientDistRecentError {
  id: number
  time: string
  channelId: string
  kind: string
  target: string
  ip: string
  status: number
  errCode: string
}

export interface StatsIP {
  ip: string
  count: number
}

export interface ClientDistRealtime {
  summary1h: ClientDistRealtimeSummary
  requestRate24h: ClientDistRatePoint[]
  recentErrors: ClientDistRecentError[]
  topIps1h: StatsIP[]
}

/**
 * 客户端分发明细事件检索（FR-093/249 兼容旧端点）：**平台管理员**端点。
 * 非管理员经 `enabled=false` 不发起请求；403 快速失败不重试。
 */
export function useClientDistEvents(filter: ClientDistEventFilter) {
  const { channelId, machineId, ip, kind, outcome, errCode, version, limit, enabled = true } = filter
  return useQuery({
    queryKey: ['client-dist-events', channelId ?? 'all', machineId ?? '', ip ?? '', kind ?? 'all', outcome ?? 'all', errCode ?? '', version ?? '', limit ?? 200],
    queryFn: async () => {
      const { data } = await api.get<ClientDistEvent[]>('/client-dist/events', {
        params: compactParams({ channelId, machineId, ip, kind, outcome, errCode, version, limit }),
      })
      return data
    },
    enabled,
    retry: false,
  })
}

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
