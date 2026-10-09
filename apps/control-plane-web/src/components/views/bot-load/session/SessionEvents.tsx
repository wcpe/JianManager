import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@jianmanager/ui/components/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import type { BotLoadRunEvent } from '@/lib/bot-load-types'

export const SESSION_EVENTS_PAGE_SIZE = 50

/** 事件类型筛选项（服务端按 `type` 过滤）。 */
const EVENT_TYPES = [
  'run-state',
  'stage',
  'barrier',
  'scenario-action',
  'command-schedule',
  'command-send',
  'worker-health',
  'executor-crash',
  'safety-stop',
  'report-ready',
] as const

export interface SessionEventsData {
  items: BotLoadRunEvent[]
  total: number
  /** 分页视图锚点（首屏固定一次，后续页回传以冻结视图）。 */
  snapshotEventId?: string
}

export interface SessionEventsProps {
  /** 当前页（分页状态上提：它决定服务端取数）。 */
  page: number
  onPageChange: (page: number) => void
  /** 类型过滤（同上）。 */
  typeFilter: string
  onTypeFilterChange: (typeFilter: string) => void
  /** HTTP 事件页数据（容器经 useBotLoadEvents 取数）。 */
  data?: SessionEventsData
  isLoading?: boolean
  isError?: boolean
  /** 实时流头部事件（容器经 useSessionEvents 取数）；仅首屏且无过滤时与 HTTP 页合并去重。 */
  liveHead: BotLoadRunEvent[]
}

/**
 * 会话事件表（FR-371）：服务端分页 + 类型过滤，首屏把实时流头部事件按 eventId 合并去重后
 * 按时间倒序展示；翻页后视图由 snapshotEventId 冻结，避免新事件插入导致页漂移。
 */
export function SessionEvents({
  page,
  onPageChange,
  typeFilter,
  onTypeFilterChange,
  data,
  isLoading,
  isError,
  liveHead,
}: SessionEventsProps) {
  const { t } = useTranslation()

  const httpItems = useMemo(() => data?.items ?? [], [data?.items])
  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / SESSION_EVENTS_PAGE_SIZE))

  const items = useMemo(() => {
    if (page !== 1 || typeFilter) return httpItems
    const map = new Map<string, BotLoadRunEvent>()
    for (const e of httpItems) map.set(e.eventId, e)
    for (const e of liveHead) {
      if (!map.has(e.eventId)) map.set(e.eventId, e)
    }
    return [...map.values()].sort((a, b) => b.timestamp.localeCompare(a.timestamp))
  }, [httpItems, liveHead, page, typeFilter])

  return (
    <div className="space-y-3" data-testid="session-events">
      <div className="flex flex-wrap gap-2">
        <select
          className="h-8 rounded-md border bg-background px-2 text-sm"
          value={typeFilter}
          onChange={(e) => onTypeFilterChange(e.target.value)}
          aria-label={t('botLoad.eventType')}
        >
          <option value="">{t('botLoad.allTypes')}</option>
          {EVENT_TYPES.map((ty) => (
            <option key={ty} value={ty}>
              {ty}
            </option>
          ))}
        </select>
      </div>

      <div className="overflow-x-auto rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/40">
            <TableRow>
              <TableHead>{t('botLoad.time')}</TableHead>
              <TableHead>{t('botLoad.eventType')}</TableHead>
              <TableHead>eventId</TableHead>
              <TableHead>{t('botLoad.summary')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((e) => (
              <TableRow key={e.eventId}>
                <TableCell className="whitespace-nowrap text-xs">{e.timestamp}</TableCell>
                <TableCell className="font-mono text-xs">{e.type}</TableCell>
                <TableCell className="font-mono text-xs">{e.eventId}</TableCell>
                <TableCell className="max-w-md truncate text-xs text-muted-foreground">
                  {summarizeEvent(e)}
                </TableCell>
              </TableRow>
            ))}
            {items.length === 0 && !isLoading && (
              <TableRow>
                <TableCell colSpan={4} className="text-center text-muted-foreground">
                  {t('botLoad.noEvents')}
                </TableCell>
              </TableRow>
            )}
            {isError && (
              <TableRow>
                <TableCell colSpan={4} className="text-center text-amber-700 dark:text-amber-300">
                  {t('botLoad.eventsUnavailable')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <div className="flex justify-between text-xs text-muted-foreground">
        <span>{t('botLoad.totalCount', { count: total })}</span>
        <div className="flex gap-2">
          <Button size="xs" variant="ghost" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>
            {t('bots.prevPage')}
          </Button>
          <span>
            {page}/{totalPages}
          </span>
          <Button
            size="xs"
            variant="ghost"
            disabled={page >= totalPages}
            onClick={() => onPageChange(page + 1)}
          >
            {t('bots.nextPage')}
          </Button>
        </div>
      </div>
    </div>
  )
}

/** 事件摘要：按类型抽取关键 payload 字段，未知类型回退为 JSON 截断。 */
export function summarizeEvent(e: BotLoadRunEvent): string {
  const p = e.payload ?? {}
  if (e.type === 'command-send' && p.mode === 'aggregate') {
    return `agg sent=${p.sent}/${p.planned} fail=${p.failed}`
  }
  if (e.type === 'command-send' && p.mode === 'item') {
    return `${p.status} ${p.errorCode ?? ''} bot=${e.botUuid ?? ''}`
  }
  if (e.type === 'run-state') return String(p.runState ?? '')
  if (e.type === 'barrier') return `${p.state ?? ''} ${p.barrierKey ?? ''}`
  try {
    return JSON.stringify(p).slice(0, 120)
  } catch {
    return ''
  }
}
