/**
 * FR-372 压测运行详情页：`/bots/sessions/:id?tab=overview|bots|metrics|failures|events|config`
 */
import { useCallback } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useBotLoadRun } from '@/api/bot-load'
import { useTabParam } from '@/lib/use-tab-param'
import { SESSION_TABS, type SessionTab, isTerminalRunState } from '@/lib/bot-load/types'
import { SessionEventProvider, useSessionEvents } from '@/components/bot-load/session/SessionEventProvider'
import { SessionHeader } from '@/components/bot-load/session/SessionHeader'
import { SessionOverview } from '@/components/bot-load/session/SessionOverview'
import { SessionBots } from '@/components/bot-load/session/SessionBots'
import { SessionMetrics } from '@/components/bot-load/session/SessionMetrics'
import { SessionFailures } from '@/components/bot-load/session/SessionFailures'
import { SessionEvents } from '@/components/bot-load/session/SessionEvents'
import { SessionConfig } from '@/components/bot-load/session/SessionConfig'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@jianmanager/ui/components/tabs'

export default function BotLoadSessionPage() {
  const { t } = useTranslation()
  const { id } = useParams<{ id: string }>()
  const runId = id ?? ''
  const numericId = Number(runId)
  const validId = Number.isFinite(numericId) && numericId > 0 ? numericId : runId

  const detail = useBotLoadRun(validId || null)
  const snapshot = detail.data
  const isV2 = snapshot?.schemaVersion === 2

  if (!runId) {
    return (
      <div className="p-6">
        <p className="text-sm text-muted-foreground">{t('botLoad.invalidId')}</p>
        <Link to="/bots" className="text-sm text-primary hover:underline">
          {t('botLoad.backToBots')}
        </Link>
      </div>
    )
  }

  if (detail.isLoading) {
    return <div className="p-6 text-muted-foreground">{t('common.loading')}</div>
  }

  if (detail.isError || !snapshot) {
    return (
      <div className="space-y-3 p-6">
        <p className="text-sm text-destructive">{t('botLoad.loadFailed')}</p>
        <Link to="/bots" className="text-sm text-primary hover:underline">
          {t('botLoad.backToBots')}
        </Link>
      </div>
    )
  }

  if (!isV2) {
    return (
      <div className="space-y-3 p-6" data-testid="session-legacy-hint">
        <h1 className="text-lg font-semibold">{snapshot.name ?? snapshot.namePrefix ?? `#${snapshot.id}`}</h1>
        <p className="text-sm text-muted-foreground">{t('botLoad.legacySchemaHint')}</p>
        <pre className="overflow-auto rounded border bg-muted/30 p-3 text-xs">
          {JSON.stringify(snapshot, null, 2)}
        </pre>
        <Link to="/bots" className="text-sm text-primary hover:underline">
          {t('botLoad.backToBots')}
        </Link>
      </div>
    )
  }

  return (
    <SessionEventProvider runId={validId} snapshot={snapshot}>
      <SessionPageBody runId={validId} />
    </SessionEventProvider>
  )
}

function SessionPageBody({ runId }: { runId: number | string }) {
  const { t } = useTranslation()
  const { run, live, streamStatus } = useSessionEvents()
  const [tab, setTab] = useTabParam<SessionTab>('tab', 'overview', SESSION_TABS)
  const [, setSearchParams] = useSearchParams()

  const onNavigate = useCallback(
    (nextTab: SessionTab, params?: Record<string, string>) => {
      setSearchParams(
        (prev) => {
          const n = new URLSearchParams(prev)
          if (nextTab === 'overview') n.delete('tab')
          else n.set('tab', nextTab)
          if (params) {
            for (const [k, v] of Object.entries(params)) {
              if (v) n.set(k, v)
              else n.delete(k)
            }
          }
          return n
        },
        { replace: true },
      )
      setTab(nextTab)
    },
    [setSearchParams, setTab],
  )

  if (!run) return null

  const reportReady = live.reportReady || isTerminalRunState(run.runState)

  return (
    <div className="mx-auto max-w-7xl space-y-4 p-1 sm:p-0" data-testid="bot-load-session-page">
      <SessionHeader run={run} streamStatus={String(streamStatus)} reportReady={reportReady} />

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList className="flex h-auto flex-wrap gap-1" aria-label={t('botLoad.tabs')}>
          {SESSION_TABS.map((key) => (
            <TabsTrigger key={key} value={key} className="text-xs sm:text-sm">
              {t(`botLoad.tab.${key}`)}
            </TabsTrigger>
          ))}
        </TabsList>

        <TabsContent value="overview" className="mt-4">
          <SessionOverview onNavigate={onNavigate} />
        </TabsContent>
        <TabsContent value="bots" className="mt-4">
          {tab === 'bots' ? <SessionBots runId={runId} /> : null}
        </TabsContent>
        <TabsContent value="metrics" className="mt-4">
          {tab === 'metrics' ? <SessionMetrics runId={runId} /> : null}
        </TabsContent>
        <TabsContent value="failures" className="mt-4">
          {tab === 'failures' ? <SessionFailures runId={runId} /> : null}
        </TabsContent>
        <TabsContent value="events" className="mt-4">
          {tab === 'events' ? <SessionEvents runId={runId} /> : null}
        </TabsContent>
        <TabsContent value="config" className="mt-4">
          {tab === 'config' ? <SessionConfig /> : null}
        </TabsContent>
      </Tabs>
    </div>
  )
}
