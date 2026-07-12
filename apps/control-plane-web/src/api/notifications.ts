import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 站内信 API（FR-183，见 ADR-040）。
 * 投递给当前用户的消息（任务完成/失败等）；只读/操作自己的站内信。
 */

/** 站内信级别。 */
export type NotificationLevel = 'info' | 'success' | 'warning' | 'error'

/** 一条站内信。 */
export interface Notification {
  id: number
  userId: number
  level: NotificationLevel
  title: string
  body: string
  /** 关联任务的业务 ID（可空）。 */
  taskId?: string
  /** 已读时间，缺省表示未读。 */
  readAt?: string
  createdAt: string
}

/**
 * 当前用户的站内信列表（FR-183）。
 * onlyUnread=true 仅未读。轮询 15s 拉取（站内信非高频，间隔放宽）。
 */
export function useNotifications(onlyUnread = false, limit = 50) {
  return useQuery({
    queryKey: ['notifications', { onlyUnread, limit }],
    queryFn: async () => {
      const { data } = await api.get<Notification[]>('/notifications', {
        params: { unread: onlyUnread ? 'true' : undefined, limit },
      })
      return data
    },
    refetchInterval: 15000,
  })
}

/** 未读站内信数量（轮询 15s，用于角标）。 */
export function useUnreadCount() {
  return useQuery({
    queryKey: ['notifications', 'unread-count'],
    queryFn: async () => {
      const { data } = await api.get<{ unread: number }>('/notifications/unread-count')
      return data.unread
    },
    refetchInterval: 15000,
  })
}

/** 标记一条站内信为已读。 */
export function useMarkNotificationRead() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post(`/notifications/${id}/read`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications'] }),
  })
}

/** 标记当前用户全部未读站内信为已读。 */
export function useMarkAllNotificationsRead() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.post('/notifications/read-all'),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications'] }),
  })
}
