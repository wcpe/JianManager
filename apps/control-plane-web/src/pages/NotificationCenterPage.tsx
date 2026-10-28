// 视图已回迁应用侧（迁包已撤销，见 ADR-103）；本层只做通知流取数、标记已读与两处跳转接线。
import { useState } from 'react'
import { useNavigate } from 'react-router'
import { useNotificationFeed, useMarkFeedRead, useMarkAllFeedRead } from '@/api/notification-feed'
import type { FeedQuery } from '@/api/notification-feed'
import { useDebounced } from '@/lib/hooks/use-debounced'
import { NotificationCenterPageView } from '@/components/views/notifications/NotificationCenterPageView'

/**
 * 通知中心页（FR-216，见 ADR-048）容器：统一通知流查询、单条/全部标记已读两个 mutation
 * 与任务中心定位 / 告警页两处跳转留在此处；筛选状态决定请求参数（改筛选即回第 1 页），
 * 故整份留在容器，列表与分页展示交共享视图。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function NotificationCenterPage() {
  const navigate = useNavigate()
  const [filter, setFilter] = useState<FeedQuery>({})
  /**
   * 关键字防抖（300ms）留在容器：输入框必须**即时回显**（`filter.keyword` 原样喂给视图），
   * 但请求键只用停止键入后的值。否则每敲一个字都发一次 `/notifications/feed`
   * ——该查询还带 30 秒轮询，会与在途请求叠成一阵请求风暴。
   * 与 PlayersPage 的取数策略同一形态：视图只管受控输入，取数口径（含防抖）归容器。
   */
  const debouncedKeyword = useDebounced(filter.keyword ?? '', 300).trim()
  const { data: page, isLoading, isError } = useNotificationFeed({
    ...filter,
    keyword: debouncedKeyword || undefined,
  })
  const markRead = useMarkFeedRead()
  const markAll = useMarkAllFeedRead()

  // 改筛选条件即回第 1 页；翻页用 onPageChange（只改页码，不动其它条件）。
  const patchFilter = (patch: Partial<FeedQuery>) => setFilter((f) => ({ ...f, ...patch, page: 1 }))

  return (
    <NotificationCenterPageView
      query={filter}
      data={page}
      isLoading={isLoading}
      error={isError}
      markingAll={markAll.isPending}
      onPatchFilter={patchFilter}
      onPageChange={(next) => setFilter((f) => ({ ...f, page: next }))}
      onMarkAllRead={() => markAll.mutate()}
      onMarkRead={(item) => markRead.mutate({ source: item.source, id: item.id })}
      onOpenTask={(taskId) => navigate(`/tasks?task=${taskId}`)}
      onOpenAlertDetail={() => navigate('/alerts')}
    />
  )
}
