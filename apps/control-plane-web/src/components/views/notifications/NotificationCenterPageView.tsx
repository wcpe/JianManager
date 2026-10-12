/**
 * @file NotificationCenterPageView：统一通知中心页的受控视图，通知流取数、标记已读与三处跳转由应用容器负责。
 * @input lib/threshold（StatusLevel）、lib/utils（cn）、布局层 PageShell/PageHeader、
 *         Panel/Button/StatusBadge 原语、翻译上下文
 * @output NotificationCenterPageView、NotificationCenterPageViewProps、NotificationFeedItem、
 *         NotificationFeedPage、NotificationFeedQuery、NotificationFeedSource、NotificationFeedLevel
 * @sync apps/control-plane-web/src/pages/NotificationCenterPage.tsx、apps/control-plane-web/src/pages/NotificationCenterPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-216 统一通知模型、FR-226 通知-任务联动）
 */
import { useTranslation } from 'react-i18next'
import { Bell } from 'lucide-react'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { Button } from '@jianmanager/ui/components/button'
import type { StatusLevel } from '@jianmanager/ui/lib/threshold'
import { cn } from '@jianmanager/ui/lib/utils'

/** 通知来源判别。message=站内信（定向消息）；alert=告警事件（系统警报）。 */
export type NotificationFeedSource = 'message' | 'alert'

/** 统一通知级别（站内信四档，告警三档已就近映射到此）。 */
export type NotificationFeedLevel = 'info' | 'success' | 'warning' | 'error'

/**
 * 单条统一通知（本视图渲染所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/notification-feed` 的 `FeedItem`：
 * 容器直接传 API 返回的完整对象也结构兼容，无需把该 API 类型迁进包
 * （同 `NotificationBell`、`ArtifactVersionsPageView` 的取舍）。
 * 未用到的告警字段（triggerType/acknowledged/resolved）不进视图，深处置留在告警页。
 */
export interface NotificationFeedItem {
  /** 源表主键（同 source 内唯一）。 */
  id: number
  source: NotificationFeedSource
  level: NotificationFeedLevel
  title: string
  body?: string
  read: boolean
  /** 发生时间（统一排序键）。 */
  createdAt: string
  /** 关联任务（仅 message 有；有值才渲染「查看任务」）。 */
  taskId?: string
}

/** 统一通知流分页响应（后端 GET /notifications/feed 返回 {items,total}）。 */
export interface NotificationFeedPage {
  items: NotificationFeedItem[]
  total: number
}

/**
 * 通知流查询参数（与应用侧 `FeedQuery` 同形）。
 * 任一字段变化都会触发重新取数，故整份状态由容器持有，视图只回传补丁。
 */
export interface NotificationFeedQuery {
  /** 来源筛选：空=全部 / message / alert。 */
  source?: NotificationFeedSource
  /** 仅未读。 */
  unread?: boolean
  /** 标题/正文模糊查询。 */
  keyword?: string
  /** 页码，从 1 起。 */
  page?: number
  /** 每页条数（默认后端 50）。 */
  pageSize?: number
}

/**
 * 受控边界（ADR-102 b 范式）：**不取数、不发请求、不碰路由、不弹 toast**。
 * - 通知流数据经 props 注入（容器调 `useNotificationFeed()`），**含其加载/错误态**——
 *   取数失败若与「没有消息」共用一个分支，用户会把故障读成「我没收到通知」；
 * - 筛选（来源/仅未读/关键字）与页码决定请求参数，整份留在容器——视图不存筛选副本，
 *   只用 `query` 渲染选中态；「改筛选即回第 1 页」与关键字防抖（300ms）都由容器在取数侧完成；
 * - 标记已读（单条/全部）与两处跳转（任务中心定位 / 告警页）以回调上报，容器执行 mutation 与 `navigate`。
 */
export interface NotificationCenterPageViewProps {
  /** 当前查询参数（容器持有）；视图据此渲染 Tab 选中态与分页器。 */
  query: NotificationFeedQuery
  /** 当前页通知流；缺省视作空列表（渲染空态）。 */
  data?: NotificationFeedPage
  /** 首屏取数在途：渲染加载态，而不是抢先显示「暂无通知」。 */
  isLoading?: boolean
  /** 取数失败：渲染错误态。缺省 false。 */
  error?: boolean
  /** 标记全部已读在途：禁用页头按钮。 */
  markingAll?: boolean
  /** 改筛选条件（来源/仅未读/关键字）；容器负责把页码归 1。 */
  onPatchFilter: (patch: Partial<NotificationFeedQuery>) => void
  /** 翻页（不改动其它筛选条件）。 */
  onPageChange: (page: number) => void
  /** 标记全部已读。 */
  onMarkAllRead: () => void
  /** 标记单条已读。 */
  onMarkRead: (item: NotificationFeedItem) => void
  /** 任务类站内信「查看任务」：跳任务中心并定位该任务（FR-226）。 */
  onOpenTask: (taskId: string) => void
  /** 告警条目「查看告警详情」：跳告警页（确认/认领等深处置仍在告警页）。 */
  onOpenAlertDetail: () => void
}

/** 统一通知级别 → StatusBadge 等级。error→danger、success/warning/info 同名直映。 */
function feedLevelStatus(level: NotificationFeedLevel): StatusLevel {
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
export function NotificationCenterPageView({
  query,
  data,
  isLoading = false,
  error = false,
  markingAll = false,
  onPatchFilter,
  onPageChange,
  onMarkAllRead,
  onMarkRead,
  onOpenTask,
  onOpenAlertDetail,
}: NotificationCenterPageViewProps) {
  const { t } = useTranslation()

  const items = data?.items ?? []
  const total = data?.total ?? 0
  const curPage = query.page ?? 1
  const pageSize = query.pageSize ?? 50
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  // key 用稳定的来源值（`undefined` 即「全部」）而不是翻译文案：文案随语言/措辞变化，
  // 拿它当 key 会让 React 在切语言时把整个 Tab 列表当成新节点重建（丢焦点、重挂载）。
  const sourceTabs: { key: string; value: NotificationFeedSource | undefined; label: string }[] = [
    { key: 'all', value: undefined, label: t('notificationCenter.sourceAll') },
    { key: 'message', value: 'message', label: t('notificationCenter.sourceMessage') },
    { key: 'alert', value: 'alert', label: t('notificationCenter.sourceAlert') },
  ]

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语（PageShell / PageHeader）。
    <PageShell data-page="notifications">
      <PageHeader
        title={t('notificationCenter.title')}
        actions={
          <Button variant="outline" size="sm" onClick={onMarkAllRead} disabled={markingAll}>
            {t('notificationCenter.markAllRead')}
          </Button>
        }
      />

      {/* 类型筛选 + 仅未读 + 关键字 */}
      <div className="jm-toolbar-surface flex flex-wrap items-center gap-2 p-2">
        <div className="flex gap-1 rounded-lg border p-0.5">
          {sourceTabs.map((tab) => (
            <button
              key={tab.key}
              type="button"
              onClick={() => onPatchFilter({ source: tab.value })}
              className={cn(
                'rounded-md px-3 py-1 text-sm font-medium transition-colors',
                query.source === tab.value
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
            checked={query.unread ?? false}
            onChange={(e) => onPatchFilter({ unread: e.target.checked || undefined })}
          />
          {t('notificationCenter.onlyUnread')}
        </label>

        {/* aria-label 落在可聚焦的 input 自身（占位符不足以作可达名）。 */}
        <input
          className="rounded border p-2 text-sm"
          placeholder={t('notificationCenter.keywordPlaceholder')}
          aria-label={t('notificationCenter.keywordPlaceholder')}
          value={query.keyword ?? ''}
          onChange={(e) => onPatchFilter({ keyword: e.target.value || undefined })}
        />
      </div>

      <Panel bodyClassName="p-0">
        {/* 三态互斥且顺序固定：在途 → 失败 → 空。取数失败绝不能落到「暂无通知」那一支
            （那会把故障说成「你没收到消息」，是用户最容易误信的一种假成功）。 */}
        {isLoading ? (
          <div className="flex min-h-[40vh] flex-col items-center justify-center gap-3 text-center">
            <Bell className="size-10 animate-pulse text-muted-foreground/40" />
            <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
          </div>
        ) : error ? (
          <div className="flex min-h-[40vh] flex-col items-center justify-center gap-3 text-center" role="alert">
            <Bell className="size-10 text-muted-foreground/40" />
            <p className="text-sm text-muted-foreground">{t('common.error')}</p>
          </div>
        ) : items.length === 0 ? (
          <div className="flex min-h-[40vh] flex-col items-center justify-center gap-3 text-center">
            <Bell className="size-10 text-muted-foreground/40" />
            <p className="text-sm text-muted-foreground">{t('notificationCenter.empty')}</p>
          </div>
        ) : (
          <ul className="divide-y">
            {items.map((it) => (
              <NotificationRow
                key={`${it.source}-${it.id}`}
                item={it}
                onMarkRead={onMarkRead}
                onOpenTask={onOpenTask}
                onOpenAlertDetail={onOpenAlertDetail}
              />
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
            onClick={() => onPageChange(curPage - 1)}
          >
            {t('notificationCenter.prevPage')}
          </Button>
          <span>{t('notificationCenter.pageOf', { page: curPage, total: totalPages })}</span>
          <Button
            variant="outline"
            size="xs"
            disabled={curPage >= totalPages}
            onClick={() => onPageChange(curPage + 1)}
          >
            {t('notificationCenter.nextPage')}
          </Button>
        </div>
      )}
    </PageShell>
  )
}

/** 单条通知行：来源徽标 + 级别 + 标题/正文/时间 + 未读高亮 + 标记已读 +（告警）查看详情。 */
function NotificationRow({
  item,
  onMarkRead,
  onOpenTask,
  onOpenAlertDetail,
}: {
  item: NotificationFeedItem
  onMarkRead: (item: NotificationFeedItem) => void
  onOpenTask: (taskId: string) => void
  onOpenAlertDetail: () => void
}) {
  const { t } = useTranslation()
  const sourceLabel = item.source === 'alert' ? t('notificationCenter.badgeAlert') : t('notificationCenter.badgeMessage')
  // 先取到局部常量：闭包里对 props 属性的收窄不成立，局部 const 才能在 onClick 中安全使用。
  const taskId = item.taskId

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
          {/* 未读不能只靠颜色（圆点色 + 行底色的淡色高亮）传达：色觉障碍与读屏用户都收不到。
              圆点保持视觉，另配一段仅读屏可见的文字作为可访问表达（原先挂在不可聚焦的 span 上的
              `title` 既不可聚焦、也不是可靠的可访问名来源）。 */}
          {!item.read && (
            <span className="inline-flex shrink-0 items-center gap-1 text-primary">
              <span className="size-1.5 rounded-full bg-primary" aria-hidden="true" />
              <span className="sr-only">{t('notificationCenter.unread')}</span>
            </span>
          )}
        </div>
        {item.body && <p className="mt-1 break-words text-sm text-muted-foreground">{item.body}</p>}
        <p className="mt-1 font-mono text-[11px] text-muted-foreground">{new Date(item.createdAt).toLocaleString()}</p>
      </div>
      <div className="flex shrink-0 flex-col items-end gap-1">
        {!item.read && (
          <Button
            type="button"
            variant="link"
            size="xs"
            className="h-auto p-0"
            onClick={() => onMarkRead(item)}
          >
            {t('notificationCenter.markRead')}
          </Button>
        )}
        {/* 任务类站内信（含 taskId）→ 一键跳任务中心并定位该任务（FR-226 联动）。 */}
        {item.source === 'message' && taskId && (
          <Button type="button" variant="link" size="xs" className="h-auto p-0" onClick={() => onOpenTask(taskId)}>
            {t('notificationCenter.viewTask')}
          </Button>
        )}
        {item.source === 'alert' && (
          <Button type="button" variant="link" size="xs" className="h-auto p-0 text-muted-foreground hover:text-foreground" onClick={onOpenAlertDetail}>
            {t('notificationCenter.viewAlertDetail')}
          </Button>
        )}
      </div>
    </li>
  )
}
