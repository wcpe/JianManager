import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useBotLoadFailures, useRetryBotLoadFailed } from '@/api/bot-load'
import { Button } from '@jianmanager/ui/components/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { FAILURE_CATEGORIES, readFailureFilter, writeFailureFilter } from '@/lib/bot-load/filters'
import type { BotLoadFailure } from '@/lib/bot-load/types'
import { FailureTraceDrawer } from './FailureTraceDrawer'
import { useSessionEvents } from './SessionEventProvider'

const PAGE_SIZE = 50

export function SessionFailures({ runId }: { runId: number | string }) {
  const { t } = useTranslation()
  const { run } = useSessionEvents()
  const [params, setParams] = useSearchParams()
  const filter = useMemo(() => readFailureFilter(params), [params])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [trace, setTrace] = useState<BotLoadFailure | null>(null)
  const [retryResult, setRetryResult] = useState<string | null>(null)
  const retryMut = useRetryBotLoadFailed()

  const query = useBotLoadFailures(runId, {
    page: filter.page,
    pageSize: PAGE_SIZE,
    category: filter.category,
    errorCode: filter.errorCode,
    botUuid: filter.botUuid,
    executorNodeId: filter.node,
    stepId: filter.step,
  })

  const items = query.data?.items ?? []
  const total = query.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const summary = run?.failureSummary ?? {}

  const patch = (partial: Partial<typeof filter>) => {
    setParams(writeFailureFilter(params, { ...filter, ...partial }), { replace: true })
  }

  const doRetry = (botUuids?: string[]) => {
    const requestId = crypto.randomUUID()
    retryMut.mutate(
      {
        id: runId,
        requestId,
        botUuids,
        errorCodes: filter.errorCode ? [filter.errorCode] : undefined,
      },
      {
        onSuccess: (res) => {
          setRetryResult(
            t('botLoad.retryResultDetail', {
              requested: res.requested,
              accepted: res.accepted,
              skipped: res.skipped,
              errors: res.errors.length,
            }),
          )
          if (res.errors.length) {
            toast.message(
              res.errors
                .slice(0, 5)
                .map((e) => `${e.botUuid ?? '?'}: ${e.errorCode} ${e.message}`)
                .join('\n'),
            )
          } else {
            toast.success(t('botLoad.retryAccepted'))
          }
        },
        onError: () => toast.error(t('botLoad.actionFailed')),
      },
    )
  }

  return (
    <div className="space-y-4" data-testid="session-failures">
      <div className="grid gap-2 sm:grid-cols-5">
        {FAILURE_CATEGORIES.map((cat) => (
          <button
            key={cat}
            type="button"
            className={`rounded-lg border p-3 text-left ${filter.category === cat ? 'border-primary bg-primary/5' : 'bg-card'}`}
            onClick={() =>
              patch({ category: filter.category === cat ? undefined : cat, page: 1 })
            }
          >
            <div className="text-xs text-muted-foreground">
              {t(`botLoad.failureCategory.${cat}`)}
            </div>
            <div className="text-lg font-semibold tabular-nums">{summary[cat] ?? 0}</div>
          </button>
        ))}
      </div>

      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          variant="outline"
          disabled={selected.size === 0 || retryMut.isPending}
          onClick={() => doRetry([...selected])}
        >
          {t('botLoad.retrySelected')}
        </Button>
        <Button size="sm" variant="outline" disabled={retryMut.isPending} onClick={() => doRetry()}>
          {t('botLoad.retryFiltered')}
        </Button>
      </div>
      {retryResult && (
        <p className="rounded border bg-muted/30 px-3 py-2 text-sm" data-testid="retry-result">
          {retryResult}
        </p>
      )}

      <div className="overflow-x-auto rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/40">
            <TableRow>
              <TableHead className="w-10" />
              <TableHead>{t('botLoad.time')}</TableHead>
              <TableHead>{t('botLoad.category')}</TableHead>
              <TableHead>{t('botLoad.errorCode')}</TableHead>
              <TableHead>{t('botLoad.botName')}</TableHead>
              <TableHead>{t('botLoad.message')}</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((f) => (
              <TableRow key={f.id}>
                <TableCell>
                  <input
                    type="checkbox"
                    checked={f.botUuid ? selected.has(f.botUuid) : false}
                    disabled={!f.botUuid}
                    onChange={() => {
                      if (!f.botUuid) return
                      setSelected((prev) => {
                        const next = new Set(prev)
                        if (next.has(f.botUuid!)) next.delete(f.botUuid!)
                        else next.add(f.botUuid!)
                        return next
                      })
                    }}
                  />
                </TableCell>
                <TableCell className="whitespace-nowrap text-xs">{f.occurredAt}</TableCell>
                <TableCell>
                  {t(`botLoad.failureCategory.${f.category}`, f.category)}
                  {f.legacyCategory === 'probe' && (
                    <span className="ml-1 text-[10px] text-amber-700">legacy</span>
                  )}
                </TableCell>
                <TableCell className="font-mono text-xs">{f.errorCode}</TableCell>
                <TableCell className="font-mono text-xs">{f.botUuid ?? '—'}</TableCell>
                <TableCell className="max-w-xs truncate text-xs">{f.message}</TableCell>
                <TableCell>
                  <Button size="xs" variant="ghost" onClick={() => setTrace(f)}>
                    {t('botLoad.traceBtn')}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
            {items.length === 0 && !query.isLoading && (
              <TableRow>
                <TableCell colSpan={7} className="text-center text-muted-foreground">
                  {t('botLoad.noFailures')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <div className="flex justify-between text-xs text-muted-foreground">
        <span>{t('botLoad.totalCount', { count: total })}</span>
        <div className="flex gap-2">
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

      <FailureTraceDrawer failure={trace} open={trace != null} onOpenChange={(o) => !o && setTrace(null)} />
    </div>
  )
}
