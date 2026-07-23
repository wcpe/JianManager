import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useBotLoadRunBots } from '@/api/bot-load'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { readBotFilter, writeBotFilter } from '@/lib/bot-load/filters'

const PAGE_SIZE = 50

export function SessionBots({ runId }: { runId: number | string }) {
  const { t } = useTranslation()
  const [params, setParams] = useSearchParams()
  const filter = useMemo(() => readBotFilter(params), [params])
  const [selected, setSelected] = useState<Set<string>>(new Set())

  const query = useBotLoadRunBots(runId, {
    page: filter.page,
    pageSize: PAGE_SIZE,
    q: filter.q,
    status: filter.status,
    executorNodeId: filter.node,
    stepId: filter.step,
    errorCode: filter.error,
  })

  const items = query.data?.items ?? []
  const total = query.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const patch = (partial: Partial<typeof filter>) => {
    setParams(writeBotFilter(params, { ...filter, ...partial }), { replace: true })
  }

  const toggle = (uuid: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(uuid)) next.delete(uuid)
      else next.add(uuid)
      return next
    })
  }

  const togglePage = () => {
    const allOnPage = items.every((b) => selected.has(b.uuid))
    setSelected((prev) => {
      const next = new Set(prev)
      for (const b of items) {
        if (allOnPage) next.delete(b.uuid)
        else next.add(b.uuid)
      }
      return next
    })
  }

  return (
    <div className="space-y-3" data-testid="session-bots">
      <div className="flex flex-wrap gap-2">
        <Input
          className="max-w-xs"
          placeholder={t('botLoad.searchBots')}
          defaultValue={filter.q ?? ''}
          onBlur={(e) => patch({ q: e.target.value || undefined, page: 1 })}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              patch({ q: (e.target as HTMLInputElement).value || undefined, page: 1 })
            }
          }}
        />
        <Input
          className="w-36"
          placeholder={t('botLoad.status')}
          defaultValue={filter.status ?? ''}
          onBlur={(e) => patch({ status: e.target.value || undefined, page: 1 })}
        />
        <Input
          className="w-28"
          placeholder={t('botLoad.node')}
          defaultValue={filter.node ?? ''}
          onBlur={(e) => patch({ node: e.target.value || undefined, page: 1 })}
        />
      </div>

      <div className="overflow-x-auto rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/40">
            <TableRow>
              <TableHead className="w-10">
                <input
                  type="checkbox"
                  aria-label={t('botLoad.selectPage')}
                  checked={items.length > 0 && items.every((b) => selected.has(b.uuid))}
                  onChange={togglePage}
                />
              </TableHead>
              <TableHead>{t('botLoad.botName')}</TableHead>
              <TableHead>{t('botLoad.status')}</TableHead>
              <TableHead>{t('botLoad.node')}</TableHead>
              <TableHead>{t('botLoad.step')}</TableHead>
              <TableHead>{t('botLoad.reconnects')}</TableHead>
              <TableHead>{t('botLoad.error')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((b) => (
              <TableRow key={b.uuid}>
                <TableCell>
                  <input
                    type="checkbox"
                    checked={selected.has(b.uuid)}
                    onChange={() => toggle(b.uuid)}
                    aria-label={b.name}
                  />
                </TableCell>
                <TableCell className="font-medium">{b.name}</TableCell>
                <TableCell>{b.status}</TableCell>
                <TableCell>{b.executorNodeId ?? '—'}</TableCell>
                <TableCell className="font-mono text-xs">{b.stepId ?? b.commandId ?? '—'}</TableCell>
                <TableCell className="tabular-nums">{b.reconnectCount}</TableCell>
                <TableCell className="max-w-[16rem] truncate text-xs text-destructive">
                  {b.lastError ?? '—'}
                </TableCell>
              </TableRow>
            ))}
            {items.length === 0 && !query.isLoading && (
              <TableRow>
                <TableCell colSpan={7} className="text-center text-muted-foreground">
                  {t('botLoad.noBots')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span>
          {t('botLoad.totalCount', { count: total })} · {t('botLoad.selectedCount', { count: selected.size })}
        </span>
        <div className="flex items-center gap-2">
          <Button size="xs" variant="ghost" disabled={filter.page <= 1} onClick={() => patch({ page: filter.page - 1 })}>
            {t('bots.prevPage')}
          </Button>
          <span>
            {filter.page}/{totalPages}
          </span>
          <Button
            size="xs"
            variant="ghost"
            disabled={filter.page >= totalPages}
            onClick={() => patch({ page: filter.page + 1 })}
          >
            {t('bots.nextPage')}
          </Button>
        </div>
      </div>
    </div>
  )
}
