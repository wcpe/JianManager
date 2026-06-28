import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 统一通知中心 API（FR-216，见 ADR-048）。
 * 把站内信（定向消息）+ 告警事件（系统警报）合并为一条只读通知流：列表（带来源/筛选/分页）、
 * 未读计数、标记已读。站内信按当前用户归属、告警面向全体（与既有 /alerts 可见性一致）。
 */

/** 通知来源判别。message=站内信（定向消息）；alert=告警事件（系统警报）。 */
export type FeedSource = 'message' | 'alert'

/** 统一通知级别（站内信四档，告警三档已就近映射到此）。 */
export type FeedLevel = 'info' | 'success' | 'warning' | 'error'

/** 一条统一通知（聚合 Notification 与 AlertEvent 的展示视图）。 */
export interface FeedItem {
  /** 来源判别（核心区分位）。 */
  source: FeedSource
  /** 源表主键（同 source 内唯一）。 */
  id: number
  level: FeedLevel
  title: string
  body: string
  read: boolean
  /** 发生时间（统一排序键）。 */
  createdAt: string
  /** 关联任务（仅 message 有）。 */
  taskId?: string
  /** 触发类型（仅 alert 有）。 */
  triggerType?: string
  /** 是否已确认（仅 alert 有）。 */
  acknowledged?: boolean
  /** 是否已恢复（仅 alert 有）。 */
  resolved?: boolean
}

/** 统一通知流查询参数。 */
export interface FeedQuery {
  /** 来源筛选：空=全部 / message / alert。 */
  source?: FeedSource
  /** 仅未读。 */
  unread?: boolean
  /** 标题/正文模糊查询。 */
  keyword?: string
  /** 页码，从 1 起。 */
  page?: number
  /** 每页条数（默认后端 50）。 */
  pageSize?: number
}

/** 统一通知流分页响应（后端 GET /notifications/feed 返回 {items,total}）。 */
export interface FeedPage {
  items: FeedItem[]
  total: number
}

/**
 * 统一通知流分页列表（FR-216）。
 * 页眉下拉用小 pageSize 拉最近若干条；通知中心页用全量筛选 + 分页。
 */
export function useNotificationFeed(params?: FeedQuery) {
  return useQuery({
    queryKey: ['notificationFeed', params],
    queryFn: async () => {
      const { data } = await api.get<FeedPage>('/notifications/feed', { params })
      return data
    },
    refetchInterval: 30000,
  })
}

/** 统一未读数（本人未读站内信 + 全局未读告警；轮询 30s，用于页眉角标）。 */
export function useFeedUnreadCount() {
  return useQuery({
    queryKey: ['notificationFeed', 'unread-count'],
    queryFn: async () => {
      const { data } = await api.get<{ unread: number }>('/notifications/feed/unread-count')
      return data.unread
    },
    refetchInterval: 30000,
  })
}

/** 标记单条通知为已读（按 source 下推到对应源）。 */
export function useMarkFeedRead() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ source, id }: { source: FeedSource; id: number }) =>
      api.post(`/notifications/feed/${source}/${id}/read`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notificationFeed'] }),
  })
}

/** 标记全部为已读（本人站内信 + 全局告警）。 */
export function useMarkAllFeedRead() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.post('/notifications/feed/read-all'),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notificationFeed'] }),
  })
}
