import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface AlertRuleInfo {
  id: number
  uuid: string
  name: string
  targetType: string
  targetId: number | null
  metric: string
  operator: string
  threshold: number
  durationSec: number
  notifyType: string
  notifyTarget: string
  enabled: boolean
  createdAt: string
}

export interface AlertEventInfo {
  id: number
  ruleId: number
  ruleName?: string
  targetId: string
  value: number
  message: string
  resolved: boolean
  firedAt: string
}

export function useAlertRules() {
  return useQuery({
    queryKey: ['alertRules'],
    queryFn: async () => {
      const { data } = await api.get<AlertRuleInfo[]>('/alerts/rules')
      return data
    },
  })
}

export function useCreateAlertRule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: Omit<AlertRuleInfo, 'id' | 'uuid' | 'createdAt'>) =>
      api.post('/alerts/rules', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['alertRules'] }),
  })
}

export function useUpdateAlertRule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...body }: Partial<AlertRuleInfo> & { id: number }) =>
      api.put(`/alerts/rules/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['alertRules'] }),
  })
}

export function useDeleteAlertRule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/alerts/rules/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['alertRules'] }),
  })
}

export function useAlertEvents(params?: { ruleId?: number; resolved?: boolean }) {
  return useQuery({
    queryKey: ['alertEvents', params],
    queryFn: async () => {
      const { data } = await api.get<AlertEventInfo[]>('/alerts/events', { params })
      return data
    },
  })
}
