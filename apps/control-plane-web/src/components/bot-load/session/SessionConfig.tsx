import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Button } from '@jianmanager/ui/components/button'
import { useSessionEvents } from './SessionEventProvider'

export function SessionConfig() {
  const { t } = useTranslation()
  const { run } = useSessionEvents()
  if (!run) return null

  const config = run.config ?? {}
  const safeConfig = {
    server: config.server,
    port: config.port,
    auth: config.auth,
    version: config.version,
  }

  const yaml =
    run.orchestrationYaml ??
    (run.commandSchedule
      ? JSON.stringify(run.commandSchedule, null, 2)
      : run.scenario
        ? JSON.stringify(run.scenario, null, 2)
        : '')

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      toast.success(t('common.copied', '已复制'))
    } catch {
      toast.error(t('common.copyFailed'))
    }
  }

  return (
    <div className="space-y-4" data-testid="session-config">
      <p className="text-sm text-muted-foreground">{t('botLoad.configSnapshotHint')}</p>

      <section className="rounded-lg border bg-card p-4 space-y-2">
        <h3 className="text-sm font-semibold">{t('botLoad.target')}</h3>
        <dl className="grid gap-1 text-sm sm:grid-cols-2">
          <div>
            <dt className="text-xs text-muted-foreground">{t('botLoad.targetInstance')}</dt>
            <dd>{run.instanceName ?? run.instanceId}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t('botLoad.templateId')}</dt>
            <dd>{run.templateId ?? t('botLoad.none')}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t('botLoad.profile')}</dt>
            <dd>{run.loadProfile?.type ?? '—'}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t('botLoad.targetBots')}</dt>
            <dd>{run.targetBots}</dd>
          </div>
        </dl>
      </section>

      <section className="rounded-lg border bg-card p-4 space-y-2">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold">{t('botLoad.connection')}</h3>
          <Button size="xs" variant="ghost" onClick={() => copy(JSON.stringify(safeConfig, null, 2))}>
            {t('common.copy', '复制')}
          </Button>
        </div>
        <pre className="overflow-auto rounded-md border bg-muted/30 p-3 text-xs">
          {JSON.stringify(safeConfig, null, 2)}
        </pre>
        <p className="text-xs text-muted-foreground">{t('botLoad.noSecretsShown')}</p>
      </section>

      <section className="rounded-lg border bg-card p-4 space-y-2">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold">{t('botLoad.commandPlan')}</h3>
          <Button size="xs" variant="ghost" disabled={!yaml} onClick={() => copy(yaml)}>
            {t('common.copy', '复制')}
          </Button>
        </div>
        <pre className="max-h-96 overflow-auto rounded-md border bg-muted/30 p-3 text-xs whitespace-pre-wrap">
          {yaml || t('botLoad.noCommands')}
        </pre>
      </section>

      <section className="rounded-lg border bg-card p-4 space-y-2">
        <h3 className="text-sm font-semibold">{t('botLoad.thresholds')}</h3>
        <pre className="overflow-auto rounded-md border bg-muted/30 p-3 text-xs">
          {JSON.stringify(run.thresholds ?? {}, null, 2)}
        </pre>
      </section>

      <section className="rounded-lg border bg-card p-4 space-y-2">
        <h3 className="text-sm font-semibold">{t('botLoad.allocations')}</h3>
        <ul className="space-y-1 text-sm">
          {(run.allocations ?? []).map((a) => (
            <li key={a.batchId} className="flex justify-between border-b border-border/40 py-1">
              <span>
                {a.executorNodeName} (#{a.executorNodeId})
              </span>
              <span className="tabular-nums">{a.plannedCount}</span>
            </li>
          ))}
          {(run.allocations ?? []).length === 0 && (
            <li className="text-muted-foreground">{t('botLoad.noExecutors')}</li>
          )}
        </ul>
      </section>
    </div>
  )
}
