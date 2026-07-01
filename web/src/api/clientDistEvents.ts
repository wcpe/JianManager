import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 客户端分发拉取/下载明细事件（FR-093 追踪 + FR-249 错误追踪）。
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
  durationMs: number
  createdAt: string
}

/** 分发事件检索过滤（FR-249）：空/undefined 字段不约束。 */
export interface ClientDistEventFilter {
  channelId?: string
  /** manifest | artifact。 */
  kind?: string
  /** 成功/失败维度：success（0<status<400）| failure（status>=400）| 空（全部）。 */
  outcome?: 'success' | 'failure' | ''
  errCode?: string
  limit?: number
  /** 平台管理员端点门控；非管理员不发起请求。 */
  enabled?: boolean
}

/**
 * 客户端分发明细事件检索（FR-093/249）：**平台管理员**端点。
 * 非管理员经 `enabled=false` 不发起请求；403 快速失败不重试（同 useClientDistObservability）。
 */
export function useClientDistEvents(filter: ClientDistEventFilter) {
  const { channelId, kind, outcome, errCode, limit, enabled = true } = filter
  return useQuery({
    queryKey: ['client-dist-events', channelId ?? 'all', kind ?? 'all', outcome ?? 'all', errCode ?? '', limit ?? 200],
    queryFn: async () => {
      const { data } = await api.get<ClientDistEvent[]>('/client-dist/events', {
        params: {
          ...(channelId ? { channelId } : {}),
          ...(kind ? { kind } : {}),
          ...(outcome ? { outcome } : {}),
          ...(errCode ? { errCode } : {}),
          ...(limit ? { limit } : {}),
        },
      })
      return data
    },
    enabled,
    retry: false,
  })
}
