import { useState, useRef, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { Bell, Check, CheckCheck } from 'lucide-react'
import {
  useNotifications,
  useUnreadCount,
  useMarkNotificationRead,
  useMarkAllNotificationsRead,
  type Notification,
  type NotificationLevel,
} from '@/api/notifications'
import { cn } from '@/lib/utils'

/** 级别 → 圆点配色。 */
const LEVEL_DOT: Record<NotificationLevel, string> = {
  info: 'bg-muted-foreground',
  success: 'bg-emerald-500',
  warning: 'bg-amber-500',
  error: 'bg-destructive',
}

/**
 * 站内信收件箱（FR-183，见 ADR-040）。
 *
 * 自包含的铃铛 + 下拉面板：轮询未读数（角标）与列表，支持标记单条/全部已读。
 *
 * 注意（解耦约束）：本组件**仅就位、不在 FR-183 内挂载**——顶栏（ConsoleHeader）由并行的
 * FR-179 改造，挂载点由其接入，避免两个 FR 同时改顶栏冲突。组件无外部依赖，可直接 `<NotificationInbox />`
 * 放进任意容器（自管开合与定位）。
 */
export default function NotificationInbox({ className }: { className?: string }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)

  const { data: unread = 0 } = useUnreadCount()
  const { data: items, isLoading } = useNotifications(false, 30)
  const markRead = useMarkNotificationRead()
  const markAll = useMarkAllNotificationsRead()

  // 点击外部关闭。
  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])

  return (
    <div ref={rootRef} className={cn('relative', className)}>
      <button
        type="button"
        aria-label={t('notifications.title')}
        onClick={() => setOpen((v) => !v)}
        className="relative inline-flex size-9 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      >
        <Bell className="size-4.5" />
        {unread > 0 && (
          <span className="absolute -right-0.5 -top-0.5 flex min-w-4 items-center justify-center rounded-full bg-destructive px-1 text-[10px] font-semibold leading-4 text-white">
            {unread > 99 ? '99+' : unread}
          </span>
        )}
      </button>

      {open && (
        <div className="absolute right-0 z-50 mt-2 w-80 overflow-hidden rounded-lg border bg-popover shadow-lg">
          <div className="flex items-center justify-between border-b px-3 py-2">
            <span className="text-sm font-semibold">{t('notifications.title')}</span>
            <button
              type="button"
              onClick={() => markAll.mutate()}
              disabled={markAll.isPending || unread === 0}
              className="inline-flex items-center gap-1 text-[11px] text-muted-foreground transition-colors hover:text-foreground disabled:opacity-40"
            >
              <CheckCheck className="size-3.5" />
              {t('notifications.markAllRead')}
            </button>
          </div>

          <div className="max-h-96 overflow-auto">
            {isLoading && !items ? (
              <p className="px-3 py-8 text-center text-sm text-muted-foreground">{t('common.loading')}</p>
            ) : !items || items.length === 0 ? (
              <p className="px-3 py-8 text-center text-sm text-muted-foreground">{t('notifications.empty')}</p>
            ) : (
              items.map((n) => (
                <NotificationRow key={n.id} item={n} onMarkRead={() => markRead.mutate(n.id)} />
              ))
            )}
          </div>
        </div>
      )}
    </div>
  )
}

/** 单条站内信：未读高亮 + 标记已读按钮。 */
function NotificationRow({ item, onMarkRead }: { item: Notification; onMarkRead: () => void }) {
  const { t } = useTranslation()
  const isUnread = !item.readAt
  return (
    <div
      className={cn(
        'flex items-start gap-2 border-b border-border/60 px-3 py-2.5 last:border-b-0',
        isUnread && 'bg-accent/40',
      )}
    >
      <span className={cn('mt-1.5 size-2 shrink-0 rounded-full', LEVEL_DOT[item.level])} />
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">{item.title}</p>
        {item.body && <p className="mt-0.5 break-words text-[11px] text-muted-foreground">{item.body}</p>}
        <p className="mt-1 font-mono text-[10px] text-muted-foreground">
          {new Date(item.createdAt).toLocaleString()}
        </p>
      </div>
      {isUnread && (
        <button
          type="button"
          aria-label={t('notifications.markRead')}
          title={t('notifications.markRead')}
          onClick={onMarkRead}
          className="mt-0.5 inline-flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <Check className="size-3.5" />
        </button>
      )}
    </div>
  )
}
