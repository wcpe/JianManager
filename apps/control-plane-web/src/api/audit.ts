import { useInfiniteQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 审计契约（FR-015 / FR-172 / FR-321）已归包，双侧共用（ADR-097）；对外保留原导出名，调用点无需改动。
 */
export type {
  AuditLogInfo,
  AuditQueryParams,
  AuditLogPage,
} from '@jianmanager/ui/lib/audit-contracts'
import type {
  AuditLogInfo,
  AuditQueryParams,
  AuditLogPage,
} from '@jianmanager/ui/lib/audit-contracts'

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
