import { useTranslation } from 'react-i18next'
import { Button } from '@jianmanager/ui/components/button'
import { copyToClipboard } from '@/lib/clipboard'
import { formatRatio } from '@/lib/bot-load-metrics'
import { sumCommandCounts } from '@/lib/bot-load-session-store'
import type { BotLoadMetricPoint, BotLoadRunV2, SessionTab } from '@/lib/bot-load-types'
import { ConnectionFunnel } from '@/components/views/bot-load/session/ConnectionFunnel'
import { DisclaimerBanner } from '@/components/views/bot-load/session/DisclaimerBanner'
import { ExecutorDistribution } from '@/components/views/bot-load/session/ExecutorDistribution'
import { ThresholdVerdict } from '@/components/views/bot-load/session/ThresholdVerdict'

/** 会话概览所需的实时流切片（容器经 useSessionEvents 取数）。 */
export interface SessionLiveSlice {
  liveMetrics: BotLoadMetricPoint[]
  warnings: { code: string; message: string; timestamp: string }[]
}

export interface SessionOverviewProps {
  /** 会话快照（容器取数）。 */
  run: BotLoadRunV2
  /** 实时流切片（容器取数）。 */
  live: SessionLiveSlice
  /** 跳转（容器注入路由；如按执行节点过滤 Bot 列表、按类别过滤失败明细）。 */
  onNavigate?: (tab: SessionTab, params?: Record<string, string>) => void
}

/**
 * 压测会话概览（FR-371）：KPI 栅格 + 连接漏斗 + 命令计划进度 + 阈值判定 + 执行器分布 +
 * 告警与失败摘要。子件（漏斗 / 分布 / 判定 / 免责横幅）均已在包内。
 */
export function SessionOverview({ run, live, onNavigate }: SessionOverviewProps) {
  const { t } = useTranslation()

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
              {live.warnings.length === 0 && <li className="text-muted-foreground">{t('botLoad.none')}</li>}
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

// ── 会话配置快照 ─────────────────────────────────────────────────────

export interface SessionConfigProps {
  /** 会话快照（容器取数）。 */
  run: BotLoadRunV2
  /** 复制结果回执（容器注入 toast；组件库不依赖 toast 实现）。 */
  onNotify?: (level: 'success' | 'error', message: string) => void
}

/**
 * 会话配置快照：目标 / 连接（脱敏）/ 命令计划 / 阈值 / 执行器分配，逐段可复制。
 *
 * 复制统一走 `copyToClipboard`（FR-188）：面板常跑在 `http://<LAN-IP>:50100` 非安全上下文，
 * 那里 `navigator.clipboard` 为 undefined，裸调 writeText 必抛错、复制永远失败；封装内有
 * execCommand 回退，非安全上下文下仍能复制成功。
 */
export function SessionConfig({ run, onNotify }: SessionConfigProps) {
  const { t } = useTranslation()

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
    const ok = await copyToClipboard(text)
    if (ok) onNotify?.('success', t('common.copied', '已复制'))
    else onNotify?.('error', t('common.copyFailed'))
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
