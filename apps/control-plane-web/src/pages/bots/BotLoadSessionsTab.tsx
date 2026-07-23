import { useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Plus } from 'lucide-react'
import {
  useBotStressSessions,
  useStartBotStressSession,
  useStopBotStressSession,
  type BotStressSessionCounts,
} from '@/api/bots'
import { mergeSearchParams, readSessionsFilter } from '@/lib/bot-load/url-state'
import BotLoadWizard from '@/components/bot-load/BotLoadWizard'
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

/**
 * 压测会话列表 tab。
 * 详情页路由 `/bots/sessions/:id` 由 FR-372 承接；此处仅列表与创建入口。
 */
export default function BotLoadSessionsTab() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const filter = readSessionsFilter(searchParams)
  const page = filter.page ?? 1
  const pageSize = 20
  const [search, setSearch] = useState(filter.q ?? '')
  const [wizardOpen, setWizardOpen] = useState(false)

  const sessions = useBotStressSessions({ page, pageSize })
  const startSession = useStartBotStressSession()
  const stopSession = useStopBotStressSession()
  const items = sessions.data?.items ?? []
  const total = sessions.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  const setPage = (p: number) => {
    setSearchParams(mergeSearchParams(searchParams, { page: p <= 1 ? null : p }), { replace: true })
  }

  const run = (kind: 'start' | 'stop', id: number) => {
    const mutation = kind === 'start' ? startSession : stopSession
    mutation.mutate(id, {
      onError: () => toast.error(t('bots.stressActionFailed')),
    })
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t('botsLoad.searchSessions')}
          className="h-9 w-56"
        />
        <Button className="ml-auto" onClick={() => setWizardOpen(true)}>
          <Plus className="size-4" /> {t('botsLoad.createRun')}
        </Button>
      </div>

      {sessions.isError && (
        <div className="rounded border border-destructive/40 bg-destructive/10 p-3 text-sm">
          {t('botsLoad.sessionsLoadFailed')}
          <Button size="xs" variant="outline" className="ml-2" onClick={() => sessions.refetch()}>
            {t('common.refresh')}
          </Button>
        </div>
      )}

      {sessions.isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : items.length === 0 ? (
        <p className="rounded-lg border py-10 text-center text-muted-foreground">{t('botsLoad.sessionsEmpty')}</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <Table>
            <TableHeader className="bg-muted/40">
              <TableRow>
                <TableHead>{t('bots.namePrefix')}</TableHead>
                <TableHead>{t('bots.instance')}</TableHead>
                <TableHead>{t('bots.status')}</TableHead>
                <TableHead className="text-right">{t('bots.count')}</TableHead>
                <TableHead className="text-right">{t('bots.statusDistribution')}</TableHead>
                <TableHead className="text-right">{t('bots.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((session) => (
                <TableRow
                  key={session.id}
                  className="cursor-pointer"
                  onClick={() => navigate(`/bots/sessions/${session.id}?tab=overview`)}
                >
                  <TableCell className="font-medium">{session.namePrefix}</TableCell>
                  <TableCell>{session.instanceId}</TableCell>
                  <TableCell>{t(`bots.stressStatus_${session.status}`, session.status)}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {session.counts.total}/{session.count}
                  </TableCell>
                  <TableCell>
                    <StatusDist counts={session.counts} />
                  </TableCell>
                  <TableCell onClick={(e) => e.stopPropagation()}>
                    <div className="flex justify-end gap-1">
                      <Button
                        size="xs"
                        variant="outline"
                        disabled={session.status !== 'pending' || startSession.isPending}
                        onClick={() => run('start', session.id)}
                      >
                        {t('bots.startSession')}
                      </Button>
                      <Button
                        size="xs"
                        variant="outline"
                        disabled={session.status === 'stopped' || stopSession.isPending}
                        onClick={() => run('stop', session.id)}
                      >
                        {t('bots.stopSession')}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span>{t('bots.totalCount', { count: total })}</span>
        <div className="flex items-center gap-2">
          <Button size="xs" variant="ghost" disabled={page <= 1} onClick={() => setPage(page - 1)}>
            {t('bots.prevPage')}
          </Button>
          <span>{t('bots.pageOf', { page, totalPages })}</span>
          <Button
            size="xs"
            variant="ghost"
            disabled={page >= totalPages}
            onClick={() => setPage(page + 1)}
          >
            {t('bots.nextPage')}
          </Button>
        </div>
      </div>

      <BotLoadWizard open={wizardOpen} onOpenChange={setWizardOpen} />
    </div>
  )
}

function StatusDist({ counts }: { counts: BotStressSessionCounts }) {
  const { t } = useTranslation()
  const entries = Object.entries(counts.byStatus).filter(([, c]) => c > 0)
  if (entries.length === 0) {
    return <span className="block text-right text-xs text-muted-foreground">—</span>
  }
  return (
    <div className="flex flex-wrap justify-end gap-1">
      {entries.map(([status, count]) => (
        <span key={status} className="rounded border px-1.5 py-0.5 text-xs">
          {t(`bots.status_${status}`, status)} {count}
        </span>
      ))}
    </div>
  )
}
