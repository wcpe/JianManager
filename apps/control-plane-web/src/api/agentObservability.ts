import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 某 Agent Token 在统计窗口内的 MCP 活动聚合（FR-391 管理端）。
 * 端点已去会话化（ADR-096），视图不再有「会话」实体，只能按 Token 聚合。
 */
export interface McpActivityItem {
  tokenId: number
  tokenName: string
  tokenPrefix: string
  lastActivityAt: string
  lastAction: string
  callCount: number
  failureCount: number
  clientIPs: string[]
  /** 客户端标识 → 该窗口内调用次数。 */
  clients: Record<string, number>
}

export interface McpActivityResponse {
  window: string
  generatedAt: string
  items: McpActivityItem[]
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

/**
 * 按 Token 聚合列出 MCP 活动（平台管理员）。
 * 轮询间隔默认 10s：活动视图无「连接/断开」事件可订阅，只能靠轮询逼近实时。
 * @param windowValue Go duration 字符串（`1h`~`168h`），越界由后端回 400。
 */
export function useMcpActivity(
  windowValue: string,
  options?: { enabled?: boolean; refetchInterval?: number },
) {
  return useQuery({
    queryKey: ['mcpActivity', windowValue],
    queryFn: async () => {
      const { data } = await api.get<McpActivityResponse>('/agent/mcp/activity', {
        params: { window: windowValue },
      })
      return {
        window: data?.window ?? windowValue,
        generatedAt: data?.generatedAt ?? '',
        items: data?.items ?? [],
      } satisfies McpActivityResponse
    },
    enabled: options?.enabled ?? true,
    refetchInterval: options?.refetchInterval ?? 10_000,
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
