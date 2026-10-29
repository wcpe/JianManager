import { useTranslation } from 'react-i18next'
import { AlertTriangle, Bell, ClipboardList, ServerCrash } from 'lucide-react'

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'

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
  /** 任务类站内信携带，用于跳任务中心定位该任务。 */
  taskId?: string
}

/** 
 * 「需要处理」条目（首页收缩后上移到页眉）。
 *
 * 与 `NotificationFeedEntry` 分开：那些是「已发生的通知」，这些是**此刻就要动手**的事，
 * 两者语义不同、处置方式也不同（前者读，后者跳转去修）。同列会让「要动手的」被已读通知淹没。
 */
export interface AttentionEntry {
  key: string
  /** 紧急度：0 崩溃实例 / 1 失败任务 / 1.5 严重告警 / 2 警告。 */
  rank: number
  /** 类别，用于图标与分组。 */
  kind: 'instance' | 'task' | 'alert'
  title: string
  /** 补充说明（实例的节点名、任务的失败原因等）。 */
  detail?: string
  /** 状态徽标文案。 */
  statusLabel?: string
  /** 状态级别：决定徽标配色。 */
  statusLevel?: 'danger' | 'warning' | 'info' | 'neutral' | 'success'
  /** 点击跳转目标。 */
  href?: string
}

export interface NotificationBellProps {
  /** 未读数（外壳取数）。 */
  unread: number
  /** 最近通知（外壳取数，已按窗口截断）。 */
  items?: NotificationFeedEntry[]
  /** 「需要处理」条目（外壳取数：崩溃实例 / 失败任务 / 严重告警）。 */
  attention?: AttentionEntry[]
  /** 「需要处理」取数中。 */
  attentionLoading?: boolean
  /** 打开单条：任务类站内信→任务中心定位；告警→告警页；其余→通知中心（快捷跳转）。 */
  onOpenItem: (item: NotificationFeedEntry) => void
  /** 打开「需要处理」某条（跳去修）。 */
  onOpenAttention?: (entry: AttentionEntry) => void
  /** 查看全部 → 通知中心页。 */
  onViewAll: () => void
}

/** 「需要处理」在下拉里最多显示几条（其余走「查看全部」去对应页面）。 */
const ATTENTION_PREVIEW_LIMIT = 5

/** 状态徽标配色（与 StatusBadge 的档位同色系，但这里是紧凑内联版本）。 */
const ATTENTION_STATUS_CLASS: Record<string, string> = {
  danger: 'bg-status-danger/15 text-status-danger',
  warning: 'bg-status-warning/15 text-status-warning',
  info: 'bg-status-info/15 text-status-info',
  success: 'bg-status-success/15 text-status-success',
  neutral: 'bg-muted text-muted-foreground',
}

/** 统一通知级别 → 圆点配色类（站内信四档；告警三档已在后端就近映射到此）。 */
function feedLevelDotClass(level: string): string {
  if (level === 'error') return 'bg-status-danger'
  if (level === 'warning') return 'bg-status-warning'
  if (level === 'success') return 'bg-status-success'
  return 'bg-status-info'
}

/** 
 * 统一通知铃铛（见 ADR-048）：合并原「站内信收件箱」+「告警铃铛」为单一入口。
 * 未读计数（统一：本人站内信 + 全局告警，30s 轮询）+ 下拉只读最近通知（消息/告警混合，各带来源标识与级别色点）；
 * 点「查看全部」进通知中心页。处置（确认/认领）仍在告警页，本下拉只读预览。
 *
 * 受控视图（ADR-103 c 范式）：未读数、最近通知与两处跳转由外壳注入。
 */
export function NotificationBell({
  unread,
  items,
  attention = [],
  attentionLoading = false,
  onOpenItem,
  onOpenAttention,
  onViewAll,
}: NotificationBellProps) {
  const { t } = useTranslation()
  const recent = items ?? []

  /* 
    「需要处理」按紧急度排序后只展示前若干条：它是下拉面板，不是工作台。
    超出部分通过「查看全部」去对应页面处理，避免把下拉撑成一个页面。
  */
  const urgent = attention.slice().sort((a, b) => a.rank - b.rank)
  const urgentVisible = urgent.slice(0, ATTENTION_PREVIEW_LIMIT)
  const urgentRest = Math.max(0, urgent.length - urgentVisible.length)

  // 未读数把「需要处理」也算进去：否则铃铛显示 0、点开却有一堆待办，用户会以为数字坏了。
  const badge = unread + urgent.length

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        {/* 页眉图标入口：28px（p-1.5 + 16px 图标）是页眉图标位的既定尺寸，
            用 ghost 的 size-8 会整体放大 4px 并把右上角未读徽标顶偏，故取 invisible+auto 保留原内边距 */}
        <Button
          type="button"
          variant="invisible"
          size="auto"
          aria-label={t('header.notifications')}
          className="relative rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <Bell className="size-4" />
          {badge > 0 && (
            <span
              className={cn(
                'absolute -right-0.5 -top-0.5 grid min-w-4 place-items-center rounded-full px-1 text-[10px] font-semibold leading-4 text-white',
                // 有紧急项时用危险色，纯未读用主色——一眼区分「有人发了消息」与「有东西坏了」。
                urgent.length > 0 ? 'bg-status-danger' : 'bg-primary',
              )}
            >
              {badge > 99 ? '99+' : badge}
            </span>
          )}
        </Button>
      </DropdownMenuTrigger>
      {/* w-96：比原来的 w-80 更宽，正文才有一行约 30 字的可读宽度 */}
      <DropdownMenuContent align="end" className="w-96 p-0">
        <div className="flex items-center justify-between gap-2 px-3 py-2 text-xs font-medium">
          <span>{t('header.notifications')}</span>
          {/* 这里只报**未读通知**数，不把「需要处理」加进来：
              徽标相加是为了「铃铛变红」这个视觉信号，但文字标签写「未读」时若含待办项，
              就成了把两类不同语义的东西报成一个数。待办数在下方分组标题里单独给。 */}
          {unread > 0 && <span className="shrink-0 text-muted-foreground">{t('header.unreadCount', { count: unread })}</span>}
        </div>

        {/* ── 需要处理（要动手的）── */}
        {(attentionLoading || urgent.length > 0) && (
          <>
            <DropdownMenuSeparator />
            <div className="flex items-center gap-1.5 px-3 pb-1 pt-2 text-[11px] font-semibold text-status-danger">
              <AlertTriangle className="size-3" />
              {t('header.attentionTitle', '需要处理')}
              {urgent.length > 0 && <span className="tabular-nums">({urgent.length})</span>}
            </div>
            {attentionLoading ? (
              <div className="px-3 py-3 text-center text-xs text-muted-foreground">{t('common.loading')}</div>
            ) : (
              <div className="max-h-64 overflow-y-auto">
                {urgentVisible.map((entry) => (
                  <AttentionPreviewRow key={entry.key} entry={entry} onOpen={() => onOpenAttention?.(entry)} />
                ))}
              </div>
            )}
            {urgentRest > 0 && (
              <div className="px-3 py-1 text-[11px] text-muted-foreground">
                {t('header.attentionMore', { count: urgentRest, defaultValue: '还有 {{count}} 项未显示' })}
              </div>
            )}
          </>
        )}

        {/* ── 最近通知（已发生的）── */}
        <DropdownMenuSeparator />
        <div className="px-3 pb-1 pt-2 text-[11px] font-semibold text-muted-foreground">
          {t('header.recentNotifications', '最近通知')}
        </div>
        {recent.length === 0 ? (
          <div className="px-3 py-4 text-center text-xs text-muted-foreground">{t('notificationCenter.empty')}</div>
        ) : (
          <div className="max-h-72 overflow-y-auto">
            {recent.map((it) => (
              <NotificationPreviewRow key={`${it.source}-${it.id}`} item={it} onOpen={() => onOpenItem(it)} />
            ))}
          </div>
        )}

        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={onViewAll} className="justify-center text-xs">
          {t('header.viewAllNotifications')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** 
 * 「需要处理」单行：图标 + 标题 + 补充说明 + 状态徽标，整行可点（跳去修）。
 *
 * 与通知预览的区别：这里的 `detail` **不截断成一行**而是两行截断
 * （`line-clamp-2`）——失败原因、节点名这类信息被截成半句就看不出该不该动手。
 */
function AttentionPreviewRow({ entry, onOpen }: { entry: AttentionEntry; onOpen: () => void }) {
  const Icon = entry.kind === 'instance' ? ServerCrash : entry.kind === 'task' ? ClipboardList : AlertTriangle
  return (
    <Button
      type="button"
      variant="invisible"
      size="auto"
      onClick={onOpen}
      data-testid={`attention-${entry.kind}`}
      className="flex w-full items-start gap-2 px-3 py-2 text-left text-xs font-normal transition-colors hover:bg-accent/60"
    >
      <Icon className="mt-0.5 size-3.5 shrink-0 text-status-danger" />
      <div className="min-w-0 flex-1">
        <div className="flex items-start justify-between gap-2">
          <p className="min-w-0 break-words text-foreground">{entry.title}</p>
          {entry.statusLabel && (
            <span
              className={cn(
                'shrink-0 rounded px-1 py-px text-[10px] font-medium',
                ATTENTION_STATUS_CLASS[entry.statusLevel ?? 'danger'],
              )}
            >
              {entry.statusLabel}
            </span>
          )}
        </div>
        {entry.detail && (
          <p className="mt-0.5 line-clamp-2 break-words text-[11px] text-muted-foreground">{entry.detail}</p>
        )}
      </div>
    </Button>
  )
}

/** 下拉内单条通知预览：级别色点 + 来源徽标 + 标题/正文 + 时间 + 未读点。 */
function NotificationPreviewRow({ item, onOpen }: { item: NotificationFeedEntry; onOpen: () => void }) {
  const { t } = useTranslation()
  const sourceLabel = item.source === 'alert' ? t('notificationCenter.badgeAlert') : t('notificationCenter.badgeMessage')
  return (
    <Button
      type="button"
      variant="invisible"
      size="auto"
      onClick={onOpen}
      className="flex w-full items-start gap-2 px-2 py-1.5 text-left text-xs font-normal transition-colors hover:bg-accent/60"
    >
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
    </Button>
  )
}
