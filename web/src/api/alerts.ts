import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'

/** 告警规则（FR-011 + FR-085）。 */
export interface AlertRuleInfo {
  id: number
  uuid: string
  name: string
  triggerType: string
  level: string
  targetType: string
  targetId: number | null
  metric: string
  operator: string
  threshold: number
  durationSec: number
  keyword: string
  eventMatch: string
  channelIds: string
  dedupWindowSec: number
  silenceStart: string
  silenceEnd: string
  notifyRecover: boolean
  notifyType: string
  notifyTarget: string
  enabled: boolean
  createdAt: string
}

/** 告警事件（FR-011 + FR-085）。 */
export interface AlertEventInfo {
  id: number
  ruleId: number
  targetId: number
  level: string
  triggerType: string
  value: number
  message: string
  count: number
  resolved: boolean
  firedAt: string
  lastFiredAt?: string
  resolvedAt?: string
  acknowledged: boolean
  acknowledgedBy?: number
  acknowledgedAt?: string
  read: boolean
  rule?: { name?: string }
}

/** 通道连接配置（凭证子字段经 ${ENV} 引用）。 */
export interface ChannelConfig {
  url?: string
  token?: string
  chatId?: string
  host?: string
  port?: number
  username?: string
  password?: string
  from?: string
  to?: string
}

/** 通知通道（FR-085）。 */
export interface AlertChannelInfo {
  id: number
  uuid: string
  name: string
  type: string
  enabled: boolean
  config: string
  createdAt: string
}

/** 创建告警规则请求体。 */
export interface CreateRuleBody {
  name: string
  triggerType: string
  level: string
  targetType: string
  targetId?: number | null
  metric?: string
  operator?: string
  threshold?: number
  durationSec?: number
  keyword?: string
  eventMatch?: string
  channelIds?: number[]
  dedupWindowSec?: number
  silenceStart?: string
  silenceEnd?: string
  notifyRecover?: boolean
  notifyType?: string
  notifyTarget?: string
}

// ── 规则 ──

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
    mutationFn: (body: CreateRuleBody) => api.post('/alerts/rules', body),
    onSuccess: () => {
      toast.success('告警规则已创建')
      qc.invalidateQueries({ queryKey: ['alertRules'] })
    },
    onError: (err: { response?: { data?: { message?: string } } }) =>
      toast.error(err.response?.data?.message || '创建告警规则失败'),
  })
}

/** 更新告警规则的可变字段（与 CreateRuleBody 不同：触发类型/目标不可改）。 */
export interface UpdateRuleBody {
  id: number
  enabled?: boolean
  threshold?: number
  level?: string
  channelIds?: number[]
  dedupWindowSec?: number
  silenceStart?: string
  silenceEnd?: string
  notifyRecover?: boolean
  keyword?: string
  eventMatch?: string
}

export function useUpdateAlertRule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...body }: UpdateRuleBody) => api.put(`/alerts/rules/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['alertRules'] }),
    onError: (err: { response?: { data?: { message?: string } } }) =>
      toast.error(err.response?.data?.message || '更新失败'),
  })
}

export function useDeleteAlertRule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/alerts/rules/${id}`),
    onSuccess: () => {
      toast.success('告警规则已删除')
      qc.invalidateQueries({ queryKey: ['alertRules'] })
    },
  })
}

// ── 事件 ──

export interface EventQuery {
  ruleId?: number
  resolved?: boolean
  acknowledged?: boolean
  level?: string
  triggerType?: string
}

export function useAlertEvents(params?: EventQuery) {
  return useQuery({
    queryKey: ['alertEvents', params],
    queryFn: async () => {
      const { data } = await api.get<AlertEventInfo[]>('/alerts/events', { params })
      return data
    },
  })
}

export function useUnreadAlertCount() {
  return useQuery({
    queryKey: ['alertUnread'],
    queryFn: async () => {
      const { data } = await api.get<{ unread: number }>('/alerts/events/unread-count')
      return data.unread
    },
    refetchInterval: 30000,
  })
}

export function useAcknowledgeEvent() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post(`/alerts/events/${id}/ack`),
    onSuccess: () => {
      toast.success('告警已确认')
      qc.invalidateQueries({ queryKey: ['alertEvents'] })
      qc.invalidateQueries({ queryKey: ['alertUnread'] })
    },
  })
}

export function useMarkAllRead() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.post('/alerts/events/read-all'),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['alertEvents'] })
      qc.invalidateQueries({ queryKey: ['alertUnread'] })
    },
  })
}

// ── 通道 ──

export function useAlertChannels() {
  return useQuery({
    queryKey: ['alertChannels'],
    queryFn: async () => {
      const { data } = await api.get<AlertChannelInfo[]>('/alerts/channels')
      return data
    },
  })
}

export interface ChannelBody {
  name: string
  type: string
  enabled?: boolean
  config: ChannelConfig
}

export function useCreateAlertChannel() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: ChannelBody) => api.post('/alerts/channels', body),
    onSuccess: () => {
      toast.success('通道已创建')
      qc.invalidateQueries({ queryKey: ['alertChannels'] })
    },
    onError: (err: { response?: { data?: { message?: string } } }) =>
      toast.error(err.response?.data?.message || '创建通道失败'),
  })
}

export function useUpdateAlertChannel() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...body }: ChannelBody & { id: number }) =>
      api.put(`/alerts/channels/${id}`, body),
    onSuccess: () => {
      toast.success('通道已更新')
      qc.invalidateQueries({ queryKey: ['alertChannels'] })
    },
    onError: (err: { response?: { data?: { message?: string } } }) =>
      toast.error(err.response?.data?.message || '更新通道失败'),
  })
}

export function useDeleteAlertChannel() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/alerts/channels/${id}`),
    onSuccess: () => {
      toast.success('通道已删除')
      qc.invalidateQueries({ queryKey: ['alertChannels'] })
    },
    onError: (err: { response?: { data?: { message?: string } } }) =>
      toast.error(err.response?.data?.message || '删除失败（可能被规则引用）'),
  })
}

export function useTestAlertChannel() {
  return useMutation({
    mutationFn: (id: number) => api.post(`/alerts/channels/${id}/test`),
    onSuccess: () => toast.success('测试通知已发送'),
    onError: (err: { response?: { data?: { message?: string } } }) =>
      toast.error(err.response?.data?.message || '测试发送失败'),
  })
}
