import { useTranslation } from 'react-i18next'
import { Activity, AlertTriangle, Download, Server, Users } from 'lucide-react'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { Badge } from '@jianmanager/ui/components/badge'
import type { ChartSeries } from '@jianmanager/ui'
import type { ClientDistStats, StatsIP } from '@/api/clientStats'
import {
  DistPanel,
  ErrorPanel,
  LinkableDistPanel,
  TrendCard,
  distBuckets,
  fmtBytes,
  fmtRate,
  kindRequests,
  resultLabel,
  totalBytes,
  type RuntimeLink,
} from './ops-shared'
import {
  KPI_I18N,
  activeClientsHintKey,
  clientDistEmptyI18nKey,
  formatKpiRate,
  resolveActiveClients,
  resolveClientDistEmptyKind,
  resolveRequestRates,
} from '@/lib/client-dist-kpi'

/**
 * 页面 B · 统计 Tab（FR-430，迁自旧监控页 `StatisticsTab`）。
 * 展示**跨频道**请求侧 KPI：清单/制品拉取、下载字节、活跃客户端、请求成功/失败率 + 趋势与分布。
 * 请求成功率 ≠ 更新成功率（后者在「机器 / 客户端」Tab）。
 */
export default function OpsStatisticsTab({
  stats,
  isError,
  isLoading,
  onLink,
}: {
  stats?: ClientDistStats
  isError: boolean
  isLoading: boolean
  onLink: (link: RuntimeLink) => void
}) {
  const { t } = useTranslation()
  const active = resolveActiveClients(null, stats)
  const requestRates = resolveRequestRates(stats)
  const activeHintKey = activeClientsHintKey(active.exactness, active.source)
  const requestCount = (stats?.downloads ?? []).reduce((sum, d) => sum + d.requests, 0)
  const emptyKind = resolveClientDistEmptyKind({
    loading: isLoading,
    requestCount,
    updateTotal: requestCount > 0 ? 1 : 0,
    active,
  })
  const emptyKey = clientDistEmptyI18nKey(emptyKind)
  const downloadSeries: ChartSeries[] = [
    {
      key: 'requests',
      name: t(KPI_I18N.downloadRequests, t('clientDistOps.totalRequests')),
      points: (stats?.downloads ?? []).map((p) => ({ ts: p.day, value: p.requests })),
    },
  ]
  const bytesSeries: ChartSeries[] = [
    {
      key: 'bytes',
      name: t(KPI_I18N.downloadBytes, t('clientDistOps.downloadBytes')),
      points: (stats?.downloads ?? []).map((p) => ({ ts: p.day, value: p.bytes })),
    },
  ]
  const versionBuckets = distBuckets(stats?.versions ?? [], (v) => v.requests, (v) => `v${v.version}`)
  const resultBuckets = distBuckets(stats?.results ?? [], (r) => r.count, (r) => resultLabel(r.result, t))
  const ipBuckets = distBuckets(stats?.topIps ?? [], (r: StatsIP) => r.count, (r: StatsIP) => r.ip || '—')

  if (isError) return <ErrorPanel title={t('clientDistOps.tabStatistics')} message={t('clientDistOps.loadError')} />
  return (
    <div className="space-y-4" data-kpi-scope="client-dist-monitor-statistics">
      <Badge variant="outline" data-testid="ops-granularity">{t('clientDistOps.granularityCrossChannel')}</Badge>
      {emptyKey ? (
        <p data-testid="monitor-stats-empty-kind" data-empty-kind={emptyKind} className="rounded-md border border-dashed border-border/80 bg-muted/30 px-3 py-2 text-sm text-muted-foreground">
          {t(emptyKey)}
        </p>
      ) : null}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <StatCard icon={<Download className="size-3.5" />} label={t(KPI_I18N.manifestPulls, t('clientDistOps.manifestPulls'))} value={String(kindRequests(stats, 'manifest'))} />
        <StatCard icon={<Download className="size-3.5" />} label={t(KPI_I18N.artifactPulls, t('clientDistOps.artifactPulls'))} value={String(kindRequests(stats, 'artifact'))} />
        <StatCard icon={<Server className="size-3.5" />} label={t(KPI_I18N.downloadBytes, t('clientDistOps.downloadBytes'))} value={fmtBytes(totalBytes(stats))} />
        <StatCard
          icon={<Users className="size-3.5" />}
          label={t(KPI_I18N.activeClients, t('clientDistOps.activeClients'))}
          value={String(active.value)}
          sub={activeHintKey ? t(activeHintKey) : t('clientDistOps.fromRequests')}
        />
        <StatCard
          icon={<Activity className="size-3.5" />}
          tone="success"
          label={t(KPI_I18N.requestSuccessRate, t('clientDistOps.requestSuccessRate'))}
          value={formatKpiRate(requestRates.successRate, fmtRate(0))}
        />
        <StatCard
          icon={<AlertTriangle className="size-3.5" />}
          tone="warning"
          label={t(KPI_I18N.requestFailureRate, t('clientDistOps.requestFailureRate'))}
          value={formatKpiRate(requestRates.failureRate, fmtRate(0))}
        />
      </div>
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <TrendCard title={t(KPI_I18N.downloadTrend, t('clientDistOps.requestTrend'))} series={downloadSeries} valueFormatter={(v) => String(Math.round(v))} empty={isLoading ? t('common.loading') : t('clientDistOps.empty')} />
        <TrendCard title={t('clientDistOps.downloadBytesTrend')} series={bytesSeries} valueFormatter={fmtBytes} empty={isLoading ? t('common.loading') : t('clientDistOps.empty')} />
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <LinkableDistPanel title={t('clientDistOps.versionDist')} buckets={versionBuckets} empty={t('clientDistOps.empty')} onPick={(key) => onLink({ version: Number(key.replace(/^v/, '')) })} />
        <DistPanel title={t('clientDistOps.resultDist')} buckets={resultBuckets} empty={t('clientDistOps.empty')} />
        <LinkableDistPanel title={t('clientDistOps.topIps')} buckets={ipBuckets} empty={t('clientDistOps.empty')} onPick={(ip) => onLink({ ip })} />
      </div>
    </div>
  )
}
