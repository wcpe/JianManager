import { useInfiniteQuery } from '@tanstack/react-query'
import api from '@/api/client'

export interface AuditLogInfo {
  id: number
  uuid: string
  userId: number
  action: string
  targetType: string
  targetId: string
  detail: string
  ip: string
  /** 操作是否失败（FR-321：失败操作也留痕；历史行零值=未失败）。 */
  failed: boolean
  /** 失败时的错误内容（响应 error body 截断，FR-321）。 */
  error: string
  createdAt: string
  user?: { id: number; username: string }
}

/**
 * 审计日志筛选参数（FR-015）：任意组合，留空表示该维度不过滤。
 * 全部透传为 `GET /audit` 的 query；后端按 RFC3339 解析 from/to。
 */
export interface AuditQueryParams {
  userId?: number
  action?: string
  targetType?: string
  /** 起始时间（RFC3339，含时区，如 2026-06-22T10:30:00Z）。 */
  from?: string
  /** 结束时间（RFC3339，含时区）。 */
  to?: string
}

export interface AuditLogPage {
  items: AuditLogInfo[]
  total: number
  page: number
  pageSize: number
}

const AUDIT_PAGE_SIZE = 100

/**
 * 无限分页查询审计日志（FR-015 / FR-172）。
 * 业务筛选进入 queryKey，筛选变化时由 TanStack Query 自然切换到新的分页缓存。
 */
export function useAuditLogs(params?: AuditQueryParams) {
  return useInfiniteQuery({
    queryKey: ['audit', params],
    initialPageParam: 1,
    queryFn: async ({ pageParam }) => {
      const { data } = await api.get<AuditLogPage | AuditLogInfo[]>('/audit', {
        params: { ...params, page: pageParam, pageSize: AUDIT_PAGE_SIZE },
      })
      if (Array.isArray(data)) {
        return { items: data, total: data.length, page: pageParam, pageSize: AUDIT_PAGE_SIZE }
      }
      return data
    },
    getNextPageParam: (lastPage) =>
      lastPage.page * lastPage.pageSize < lastPage.total ? lastPage.page + 1 : undefined,
  })
}

export async function exportAuditLogs(params?: AuditQueryParams): Promise<Blob> {
  const { data } = await api.get<Blob>('/audit/export', { params, responseType: 'blob' })
  return data
}
