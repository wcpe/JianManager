import { useTranslation } from 'react-i18next'
import { Bell } from 'lucide-react'

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { cn } from '@jianmanager/ui'

/**
 * 统一通知条目（站内信 / 告警混合；字段取渲染与快捷跳转所需）。
 * 结构化子集，不牵动应用侧 `FeedItem` 契约。
 */
export interface NotificationFeedEntry {
  id: number
  /** 'message' 站内信 / 'alert' 告警。 */
  source: string
  /** 四档级别：info / success / warning / error。 */
  level: string
  title: string
  body?: string
  createdAt: string
  read: boolean
  /** 任务类站内信携带，用于跳任务中心定位该任务（FR-226）。 */
  taskId?: string
}

export interface NotificationBellProps {
  /** 未读数（外壳取数）。 */
  unread: number
  /** 最近通知（外壳取数，已按窗口截断）。 */
  items?: NotificationFeedEntry[]
  /** 打开单条：任务类站内信→任务中心定位；告警→告警页；其余→通知中心（FR-226 快捷跳转）。 */
  onOpenItem: (item: NotificationFeedEntry) => void
  /** 查看全部 → 通知中心页。 */
  onViewAll: () => void
}

/** 统一通知级别 → 圆点配色类（站内信四档；告警三档已在后端就近映射到此）。 */
function feedLevelDotClass(level: string): string {
  if (level === 'error') return 'bg-status-danger'
  if (level === 'warning') return 'bg-status-warning'
  if (level === 'success') return 'bg-status-success'
  return 'bg-status-info'
}

/**
 * 统一通知铃铛（FR-216，见 ADR-048）：合并原「站内信收件箱」+「告警铃铛」为单一入口。
 * 未读计数（统一：本人站内信 + 全局告警，30s 轮询）+ 下拉只读最近通知（消息/告警混合，各带来源标识与级别色点）；
 * 点「查看全部」进通知中心页。处置（确认/认领）仍在告警页，本下拉只读预览。
 *
 * 受控视图（ADR-097 c 范式）：未读数、最近通知与两处跳转由外壳注入。
 */
export function NotificationBell({ unread, items, onOpenItem, onViewAll }: NotificationBellProps) {
  const { t } = useTranslation()
  const recent = items ?? []

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t('header.notifications')}
          className="relative rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <Bell className="size-4" />
          {unread > 0 && (
            <span className="absolute -right-0.5 -top-0.5 grid min-w-4 place-items-center rounded-full bg-status-danger px-1 text-[10px] font-semibold leading-4 text-white">
              {unread > 99 ? '99+' : unread}
            </span>
          )}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <div className="flex items-center justify-between px-2 py-1.5 text-xs font-medium">
          <span>{t('header.notifications')}</span>
          {unread > 0 && <span className="text-muted-foreground">{t('header.unreadCount', { count: unread })}</span>}
        </div>
        <DropdownMenuSeparator />
        {recent.length === 0 ? (
          <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('notificationCenter.empty')}</div>
        ) : (
          <div className="max-h-72 overflow-y-auto">
            {recent.map((it) => (
              <NotificationPreviewRow key={`${it.source}-${it.id}`} item={it} onOpen={() => onOpenItem(it)} />
            ))}
          </div>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={onViewAll}>
          {t('header.viewAllNotifications')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** 下拉内单条通知预览：级别色点 + 来源徽标 + 标题/正文 + 时间 + 未读点。 */
function NotificationPreviewRow({ item, onOpen }: { item: NotificationFeedEntry; onOpen: () => void }) {
  const { t } = useTranslation()
  const sourceLabel = item.source === 'alert' ? t('notificationCenter.badgeAlert') : t('notificationCenter.badgeMessage')
  return (
    <button type="button" onClick={onOpen} className="flex w-full items-start gap-2 px-2 py-1.5 text-left text-xs transition-colors hover:bg-accent/60">
      <span className={cn('mt-1 size-1.5 shrink-0 rounded-full', feedLevelDotClass(item.level))} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span
            className={cn(
              'shrink-0 rounded px-1 py-px text-[10px] font-medium',
              item.source === 'alert'
                ? 'bg-status-warning/15 text-status-warning'
                : 'bg-primary/10 text-primary',
            )}
          >
            {sourceLabel}
          </span>
          <p className="truncate text-foreground">{item.title}</p>
        </div>
        {item.body && <p className="mt-0.5 truncate text-[11px] text-muted-foreground">{item.body}</p>}
        <p className="text-[11px] text-muted-foreground">{new Date(item.createdAt).toLocaleString()}</p>
      </div>
      {!item.read && <span className="mt-1 size-1.5 shrink-0 rounded-full bg-primary" />}
    </button>
  )
}
