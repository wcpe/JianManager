import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useBotLoadEvents } from '@/api/bot-load'
import { Button } from '@jianmanager/ui/components/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { useSessionEvents } from './SessionEventProvider'
import type { BotLoadRunEvent } from '@/lib/bot-load/types'

const PAGE_SIZE = 50

export function SessionEvents({ runId }: { runId: number | string }) {
  const { t } = useTranslation()
  const { live } = useSessionEvents()
  const [page, setPage] = useState(1)
  const [typeFilter, setTypeFilter] = useState('')
  const [snapshotEventId, setSnapshotEventId] = useState<string | undefined>()

  const query = useBotLoadEvents(runId, {
    page,
    pageSize: PAGE_SIZE,
    type: typeFilter || undefined,
    snapshotEventId: page > 1 ? snapshotEventId : undefined,
  })

  useEffect(() => {
    if (page === 1 && query.data?.snapshotEventId) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- 首屏分页锚点随查询结果固定一次
      setSnapshotEventId(query.data.snapshotEventId)
    }
  }, [page, query.data?.snapshotEventId])

  const httpItems = query.data?.items ?? []
  const total = query.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const items = useMemo(() => {
    if (page !== 1 || typeFilter) return httpItems
    const map = new Map<string, BotLoadRunEvent>()
    for (const e of httpItems) map.set(e.eventId, e)
    for (const e of live.historyHead) {
      if (!map.has(e.eventId)) map.set(e.eventId, e)
    }
    return [...map.values()].sort((a, b) => b.timestamp.localeCompare(a.timestamp))
  }, [httpItems, live.historyHead, page, typeFilter])

  return (
    <div className="space-y-3" data-testid="session-events">
      <div className="flex flex-wrap gap-2">
        <select
          className="h-8 rounded-md border bg-background px-2 text-sm"
          value={typeFilter}
          onChange={(e) => {
            setTypeFilter(e.target.value)
            setPage(1)
            setSnapshotEventId(undefined)
          }}
          aria-label={t('botLoad.eventType')}
        >
          <option value="">{t('botLoad.allTypes')}</option>
          {(
            [
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
          ).map((ty) => (
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
            {items.length === 0 && !query.isLoading && (
              <TableRow>
                <TableCell colSpan={4} className="text-center text-muted-foreground">
                  {t('botLoad.noEvents')}
                </TableCell>
              </TableRow>
            )}
            {query.isError && (
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
          <Button size="xs" variant="ghost" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
            {t('bots.prevPage')}
          </Button>
          <span>
            {page}/{totalPages}
          </span>
          <Button
            size="xs"
            variant="ghost"
            disabled={page >= totalPages}
            onClick={() => {
              if (query.data?.snapshotEventId) setSnapshotEventId(query.data.snapshotEventId)
              setPage((p) => p + 1)
            }}
          >
            {t('bots.nextPage')}
          </Button>
        </div>
      </div>
    </div>
  )
}

function summarizeEvent(e: BotLoadRunEvent): string {
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
