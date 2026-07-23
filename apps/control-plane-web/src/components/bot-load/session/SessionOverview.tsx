import { useTranslation } from 'react-i18next'
import { useSessionEvents } from './SessionEventProvider'
import { ConnectionFunnel } from './ConnectionFunnel'
import { ExecutorDistribution } from './ExecutorDistribution'
import { ThresholdVerdict } from './ThresholdVerdict'
import { DisclaimerBanner } from './DisclaimerBanner'
import { sumCommandCounts } from '@/lib/bot-load/session-store'
import { formatRatio } from '@/lib/bot-load/metrics'
import type { SessionTab } from '@/lib/bot-load/types'

export function SessionOverview({
  onNavigate,
}: {
  onNavigate?: (tab: SessionTab, params?: Record<string, string>) => void
}) {
  const { t } = useTranslation()
  const { run, live } = useSessionEvents()
  if (!run) return null

  const lc = run.loadCounts
  const onlineRate = lc.planned > 0 ? lc.connected / lc.planned : 0
  const cmd = sumCommandCounts(run.commandCounts)
  const latestMetric = live.liveMetrics[live.liveMetrics.length - 1]

  return (
    <div className="space-y-6" data-testid="session-overview">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Kpi label={t('botLoad.kpi.target')} value={String(run.targetBots)} />
        <Kpi label={t('botLoad.kpi.accepted')} value={String(lc.accepted)} />
        <Kpi label={t('botLoad.kpi.online')} value={`${lc.connected} (${formatRatio(onlineRate)})`} />
        <Kpi label={t('botLoad.kpi.failed')} value={String(lc.failed)} />
        <Kpi label={t('botLoad.kpi.cmdSent')} value={String(cmd.sent)} />
        <Kpi label={t('botLoad.kpi.cmdFailed')} value={String(cmd.failed + cmd.timedOut)} />
        <Kpi label={t('botLoad.kpi.stage')} value={String(run.currentStage)} />
        <Kpi label={t('botLoad.kpi.maxStable')} value={String(run.maxStableBots)} />
      </div>

      <DisclaimerBanner />

      <div className="grid gap-6 lg:grid-cols-2">
        <section className="rounded-lg border bg-card p-4">
          <ConnectionFunnel counts={lc} />
        </section>
        <section className="rounded-lg border bg-card p-4 space-y-3">
          <h3 className="text-sm font-semibold">{t('botLoad.commandPlanProgress')}</h3>
          <p className="text-xs text-muted-foreground">{t('botLoad.sentMeansChatOnly')}</p>
          <ul className="space-y-2 text-sm">
            {Object.entries(run.commandCounts).map(([id, c]) => (
              <li key={id} className="flex flex-wrap justify-between gap-2 border-b border-border/40 pb-1">
                <span className="font-medium">{id}</span>
                <span className="tabular-nums text-muted-foreground">
                  {c.sent}/{c.planned} · F{c.failed} T{c.timedOut} C{c.cancelled}
                </span>
              </li>
            ))}
            {Object.keys(run.commandCounts).length === 0 && (
              <li className="text-muted-foreground">{t('botLoad.noCommands')}</li>
            )}
          </ul>
          <div className="pt-2">
            <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              {t('botLoad.barrier')}
            </h4>
            <p className="text-sm tabular-nums">
              W{run.barrier.waiting} · A{run.barrier.arrived} · R{run.barrier.released} · T
              {run.barrier.timedOut}
            </p>
          </div>
        </section>
      </div>

      <section className="rounded-lg border bg-card p-4">
        <h3 className="mb-3 text-sm font-semibold">{t('botLoad.thresholdVerdict')}</h3>
        <ThresholdVerdict reasons={run.verdictReasons ?? []} />
      </section>

      <section className="rounded-lg border bg-card p-4">
        <ExecutorDistribution
          allocations={run.allocations ?? []}
          latestMetric={latestMetric}
          onFilterNode={(nodeId) => onNavigate?.('bots', { node: String(nodeId) })}
        />
      </section>

      {(live.warnings.length > 0 || Object.keys(run.failureSummary ?? {}).length > 0) && (
        <section className="grid gap-4 lg:grid-cols-2">
          <div className="rounded-lg border bg-card p-4">
            <h3 className="mb-2 text-sm font-semibold">{t('botLoad.recentWarnings')}</h3>
            <ul className="space-y-1 text-sm">
              {live.warnings.slice(0, 10).map((w) => (
                <li key={w.code + w.timestamp}>
                  <span className="font-mono text-xs">{w.code}</span> {w.message}
                </li>
              ))}
              {live.warnings.length === 0 && (
                <li className="text-muted-foreground">{t('botLoad.none')}</li>
              )}
            </ul>
          </div>
          <div className="rounded-lg border bg-card p-4">
            <h3 className="mb-2 text-sm font-semibold">{t('botLoad.failureSummary')}</h3>
            <ul className="space-y-1 text-sm">
              {Object.entries(run.failureSummary ?? {}).map(([k, v]) => (
                <li key={k} className="flex justify-between">
                  <button
                    type="button"
                    className="text-primary hover:underline"
                    onClick={() => onNavigate?.('failures', { category: k })}
                  >
                    {t(`botLoad.failureCategory.${k}`, k)}
                  </button>
                  <span className="tabular-nums">{v}</span>
                </li>
              ))}
            </ul>
          </div>
        </section>
      )}
    </div>
  )
}

function Kpi({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border bg-card p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="mt-1 text-lg font-semibold tabular-nums">{value}</div>
    </div>
  )
}
