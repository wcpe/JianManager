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

export function useAuditLogs(params?: { userId?: number; action?: string; limit?: number }) {
  return useQuery({
    queryKey: ['audit', params],
    queryFn: async () => {
      const { data } = await api.get<AuditLogInfo[]>('/audit', { params })
      return data
    },
  })
}
