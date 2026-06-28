import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { Bell } from 'lucide-react'

import {
  useNotificationFeed,
  useMarkFeedRead,
  useMarkAllFeedRead,
  type FeedItem,
  type FeedSource,
  type FeedLevel,
  type FeedQuery,
} from '@/api/notification-feed'
import { Panel } from '@/components/ui/panel'
import { StatusBadge } from '@/components/ui/status-badge'
import { Button } from '@/components/ui/button'
import type { StatusLevel } from '@/lib/threshold'
import { cn } from '@/lib/utils'

/** 统一通知级别 → StatusBadge 等级。error→danger、success/warning/info 同名直映。 */
function feedLevelStatus(level: FeedLevel): StatusLevel {
  switch (level) {
    case 'error':
      return 'danger'
    case 'warning':
      return 'warning'
    case 'success':
      return 'success'
    default:
      return 'info'
  }
}

/**
 * 通知中心页（FR-216，见 ADR-048）。
 * 统一消费站内信（定向消息）+ 告警事件（系统警报）合并的通知流：
 * 按类型[消息/告警]筛选、仅未读、关键字查询、分页、行内/全部标记已读。
 * 告警条目附「查看告警详情」入口跳 /alerts（确认/认领等深处置仍在告警页）。
 */
export default function NotificationCenterPage() {
  const { t } = useTranslation()
  const [filter, setFilter] = useState<FeedQuery>({})
  const { data: page } = useNotificationFeed(filter)
  const markAll = useMarkAllFeedRead()

  const items = page?.items ?? []
  const total = page?.total ?? 0
  const curPage = filter.page ?? 1
  const pageSize = filter.pageSize ?? 50
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  // 改筛选条件即回第 1 页；翻页用单独 setFilter。
  const patchFilter = (patch: Partial<FeedQuery>) => setFilter((f) => ({ ...f, ...patch, page: 1 }))

  const sourceTabs: { value: FeedSource | undefined; label: string }[] = [
    { value: undefined, label: t('notificationCenter.sourceAll') },
    { value: 'message', label: t('notificationCenter.sourceMessage') },
    { value: 'alert', label: t('notificationCenter.sourceAlert') },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold">{t('notificationCenter.title')}</h1>
        <Button variant="outline" size="sm" onClick={() => markAll.mutate()} disabled={markAll.isPending}>
          {t('notificationCenter.markAllRead')}
        </Button>
      </div>

      {/* 类型筛选 + 仅未读 + 关键字 */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex gap-1 rounded-lg border p-0.5">
          {sourceTabs.map((tab) => (
            <button
              key={tab.label}
              type="button"
              onClick={() => patchFilter({ source: tab.value })}
              className={cn(
                'rounded-md px-3 py-1 text-sm font-medium transition-colors',
                filter.source === tab.value
                  ? 'bg-primary text-primary-foreground'
                  : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
              )}
            >
              {tab.label}
            </button>
          ))}
        </div>

        <label className="flex items-center gap-1.5 text-sm text-muted-foreground">
          <input
            type="checkbox"
            className="size-4 accent-primary"
            checked={filter.unread ?? false}
            onChange={(e) => patchFilter({ unread: e.target.checked || undefined })}
          />
          {t('notificationCenter.onlyUnread')}
        </label>

        <input
          className="rounded border p-2 text-sm"
          placeholder={t('notificationCenter.keywordPlaceholder')}
          value={filter.keyword ?? ''}
          onChange={(e) => patchFilter({ keyword: e.target.value || undefined })}
        />
      </div>

      <Panel bodyClassName="p-0">
        {items.length === 0 ? (
          <div className="flex min-h-[40vh] flex-col items-center justify-center gap-3 text-center">
            <Bell className="size-10 text-muted-foreground/40" />
            <p className="text-sm text-muted-foreground">{t('notificationCenter.empty')}</p>
          </div>
        ) : (
          <ul className="divide-y">
            {items.map((it) => (
              <NotificationRow key={`${it.source}-${it.id}`} item={it} />
            ))}
          </ul>
        )}
      </Panel>

      {total > 0 && (
        <div className="flex items-center justify-end gap-3 text-sm text-muted-foreground">
          <span>{t('notificationCenter.total', { count: total })}</span>
          <Button
            variant="outline"
            size="xs"
            disabled={curPage <= 1}
            onClick={() => setFilter((f) => ({ ...f, page: curPage - 1 }))}
          >
            {t('notificationCenter.prevPage')}
          </Button>
          <span>{t('notificationCenter.pageOf', { page: curPage, total: totalPages })}</span>
          <Button
            variant="outline"
            size="xs"
            disabled={curPage >= totalPages}
            onClick={() => setFilter((f) => ({ ...f, page: curPage + 1 }))}
          >
            {t('notificationCenter.nextPage')}
          </Button>
        </div>
      )}
    </div>
  )
}

/** 单条通知行：来源徽标 + 级别 + 标题/正文/时间 + 未读高亮 + 标记已读 +（告警）查看详情。 */
function NotificationRow({ item }: { item: FeedItem }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const markRead = useMarkFeedRead()
  const sourceLabel = item.source === 'alert' ? t('notificationCenter.badgeAlert') : t('notificationCenter.badgeMessage')

  return (
    <li className={cn('flex items-start gap-3 px-4 py-3', !item.read && 'bg-primary/5')}>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span
            className={cn(
              'shrink-0 rounded px-1.5 py-px text-[11px] font-medium',
              item.source === 'alert'
                ? 'bg-status-warning/15 text-status-warning'
                : 'bg-primary/10 text-primary',
            )}
          >
            {sourceLabel}
          </span>
          <StatusBadge level={feedLevelStatus(item.level)} label={item.level} dot={false} />
          <span className="truncate font-medium">{item.title}</span>
          {!item.read && <span className="size-1.5 shrink-0 rounded-full bg-primary" title={t('notificationCenter.unread')} />}
        </div>
        {item.body && <p className="mt-1 break-words text-sm text-muted-foreground">{item.body}</p>}
        <p className="mt-1 font-mono text-[11px] text-muted-foreground">{new Date(item.createdAt).toLocaleString()}</p>
      </div>
      <div className="flex shrink-0 flex-col items-end gap-1">
        {!item.read && (
          <button
            type="button"
            className="text-xs text-primary hover:underline"
            onClick={() => markRead.mutate({ source: item.source, id: item.id })}
          >
            {t('notificationCenter.markRead')}
          </button>
        )}
        {item.source === 'alert' && (
          <button type="button" className="text-xs text-muted-foreground hover:underline" onClick={() => navigate('/alerts')}>
            {t('notificationCenter.viewAlertDetail')}
          </button>
        )}
      </div>
    </li>
  )
}
