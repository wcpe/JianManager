import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** MCP 会话快照（FR-389 管理端）。 */
export interface McpSessionInfo {
  sessionId: string
  tokenId: number
  tokenName: string
  tokenPrefix: string
  clientIP: string
  transport: string
  connectedAt: string
  lastActivityAt: string
  lastTool?: string
  idleTimeout?: string
  absoluteTimeout?: string
}

export interface McpSessionsResponse {
  sessions: McpSessionInfo[]
  config?: {
    idleTimeout?: string
    absoluteTimeout?: string
    maxGlobalSessions?: number
    maxSessionsPerToken?: number
  }
}

/** Agent 调用流水行（FR-390）。 */
export interface AgentCallLogInfo {
  id: number
  tokenId: number
  tokenName: string
  action: string
  client: string
  transport?: string
  targetType?: string
  targetId?: string
  success: boolean
  error?: string
  latencyMs?: number
  ip?: string
  createdAt: string
}

export interface AgentCallLogPage {
  items: AgentCallLogInfo[]
  total: number
  page: number
  pageSize: number
}

export interface AgentCallLogFilter {
  tokenId?: number
  action?: string
  client?: string
  success?: boolean | ''
  from?: string
  to?: string
  page?: number
  pageSize?: number
}

/** 列出 MCP 会话（平台管理员）。 */
export function useMcpSessions(options?: { enabled?: boolean; refetchInterval?: number }) {
  return useQuery({
    queryKey: ['mcpSessions'],
    queryFn: async () => {
      const { data } = await api.get<McpSessionsResponse>('/agent/mcp/sessions')
      return {
        sessions: data?.sessions ?? [],
        config: data?.config,
      }
    },
    enabled: options?.enabled ?? true,
    refetchInterval: options?.refetchInterval ?? 10_000,
  })
}

/** 踢线 MCP 会话。 */
export function useKickMcpSession() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (sessionId: string) => api.delete(`/agent/mcp/sessions/${encodeURIComponent(sessionId)}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['mcpSessions'] }),
  })
}

/** 分页查询 Agent 调用流水。 */
export function useAgentCallLogs(filter: AgentCallLogFilter, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['agentCallLogs', filter],
    queryFn: async () => {
      const params: Record<string, string | number | boolean> = {
        page: filter.page ?? 1,
        pageSize: filter.pageSize ?? 50,
      }
      if (filter.tokenId != null && filter.tokenId > 0) params.tokenId = filter.tokenId
      if (filter.action?.trim()) params.action = filter.action.trim()
      if (filter.client?.trim()) params.client = filter.client.trim()
      if (filter.success === true || filter.success === false) params.success = filter.success
      if (filter.from?.trim()) params.from = filter.from.trim()
      if (filter.to?.trim()) params.to = filter.to.trim()
      const { data } = await api.get<AgentCallLogPage>('/agent/call-logs', { params })
      return {
        items: data?.items ?? [],
        total: data?.total ?? 0,
        page: data?.page ?? 1,
        pageSize: data?.pageSize ?? 50,
      } satisfies AgentCallLogPage
    },
    enabled: options?.enabled ?? true,
  })
}

/** 面板可用的 MCP 基址（同源 /api/v1/mcp）。 */
export function mcpBaseUrl(): string {
  if (typeof window === 'undefined') return '/api/v1/mcp'
  return `${window.location.origin}/api/v1/mcp`
}
