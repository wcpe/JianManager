import { useQuery } from '@tanstack/react-query'
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
  limit?: number
}

/**
 * 查询审计日志（FR-015）。
 * 筛选条件下沉到后端 DB（user/action/targetType/时间范围/limit），变更即重查。
 * keepPreviousData 让改筛选时旧结果保留，避免表格闪烁。
 */
export function useAuditLogs(params?: AuditQueryParams) {
  return useQuery({
    queryKey: ['audit', params],
    queryFn: async () => {
      const { data } = await api.get<AuditLogInfo[]>('/audit', { params })
      return data
    },
    placeholderData: (prev) => prev,
  })
}
