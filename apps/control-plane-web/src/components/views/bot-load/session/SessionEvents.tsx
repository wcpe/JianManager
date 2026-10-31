/* eslint-disable react-refresh/only-export-components -- 视图与随其导出的纯逻辑/变体同文件（沿用原组织方式），非组件导出按仓库约定在此豁免 */
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { PaginationBar } from '@jianmanager/ui/components/pagination'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@jianmanager/ui/components/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import type { BotLoadRunEvent } from '@/lib/bot-load/bot-load-types'

export const SESSION_EVENTS_PAGE_SIZE = 50

/** 事件类型筛选项（服务端按 `type` 过滤）。 */
const EVENT_TYPES = [  'run-state',
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

/** 
 * 「全部类型」的哨兵值：Radix Select 的 item 不接受空字符串（空串被它用来表示未选中），
 * 而本视图对外契约是 `''` 代表全部（父组件与请求参数都按空串判），故在控件的边界上做一次映射。
 */
const ALL_TYPES = '__all__'

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
 * 会话事件表：服务端分页 + 类型过滤，首屏把实时流头部事件按 eventId 合并去重后
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
        {/* 事件类型是代码内固定枚举（+「全部」）→ 走组件库 Select（Radix），
            与其它筛选器同款触发器样式与键盘导航。
            注意：Radix Select 不接受空字符串作为 item value（会与「未选中」冲突），
            故「全部」用哨兵值 ALL_TYPES 表达。 */}
        <Select value={typeFilter === '' ? ALL_TYPES : typeFilter} onValueChange={(v) => onTypeFilterChange(v === ALL_TYPES ? '' : v)}>
          <SelectTrigger aria-label={t('botLoad.eventType')} size="sm" className="w-auto">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL_TYPES}>{t('botLoad.allTypes')}</SelectItem>
            {EVENT_TYPES.map((ty) => (
              <SelectItem key={ty} value={ty}>
                {ty}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
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

      <PaginationBar
        page={page}
        totalPages={totalPages}
        total={total}
        onChange={onPageChange}
        totalLabel={t('botLoad.totalCount', { count: total })}
        // 此处原文案是裸「页码/总页数」（无「第…页」包装），PaginationBar 默认拼接与之等价。
        prevLabel={t('bots.prevPage')}
        nextLabel={t('bots.nextPage')}
        className="text-xs"
      />
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
