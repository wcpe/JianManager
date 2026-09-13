import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import {
  Activity,
  AlertTriangle,
  Ban,
  Clock,
  Download,
  Gauge,
  RadioTower,
  ShieldAlert,
  ShieldCheck,
} from 'lucide-react'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { Panel } from '@jianmanager/ui/components/panel'
import { Sparkline } from '@jianmanager/ui'
import type { ChartSeries } from '@jianmanager/ui'
import { useClientDistObservability, type ClientDistStats } from '@/api/clientStats'
import { useClientDistSecurityOverview, type SecurityRankItem } from '@/api/clientDistSecurity'
import type { ClientRuntimeOverview } from '@/api/clientRuntimeStates'
import { buildClientDistHref } from '@/lib/client-dist-query'
import InsightCards from './InsightCards'
import type { ObsWindow } from './obs-window'
import {
  ErrorPanel,
  LinkableDistPanel,
  TrendCard,
  distBuckets,
  fmtBytes as fmtBytesCompact,
  kindLabel,
  lagLabel,
  parseLagLabel,
  platformLabel,
  resultLabel,
  reversePlatformLabel,
  runtimeUpdateSeries,
  type RuntimeLink,
} from './ops-shared'
import {
  KPI_I18N,
  formatKpiRate,
  resolveRequestRates,
} from '@/lib/client-dist-kpi'

/**
 * 页面 B · 总览 Tab（全面融合：分发健康 + 运行态 + 请求侧 + 安全态势）。
 * KPI 口径严格遵循 FR-356：更新侧率只信 observability，请求侧率只信 stats。
 */
export default function OpsOverviewTab({
  channelId,
  window,
  stats,
  runtime,
  onLink,
}: {
  channelId?: string
  window: ObsWindow
  stats?: ClientDistStats
  runtime?: ClientRuntimeOverview
  onLink: (link: RuntimeLink) => void
}) {
  const { t } = useTranslation()
  const obs = useClientDistObservability({ channelId, window })
  const security = useClientDistSecurityOverview()

  const series = obs.data?.series ?? []
  const compare = obs.data?.compare

  const requestRates = resolveRequestRates(stats ?? null)

  const requestCount = (stats?.downloads ?? []).reduce((sum, d) => sum + d.requests, 0)
  const lagMachines = (runtime?.lagDist ?? []).filter((l) => l.lag > 0).reduce((s, l) => s + l.count, 0)

  const pullsSeries: ChartSeries[] = [
    {
      key: 'manifest',
      name: kindLabel('manifest', t),
      points: series.map((p) => ({ ts: p.ts, value: p.manifestPulls })),
    },
    {
      key: 'artifact',
      name: kindLabel('artifact', t),
      points: series.map((p) => ({ ts: p.ts, value: p.artifactPulls })),
    },
  ]
  const bytesSeries: ChartSeries[] = [
    {
      key: 'bytes',
      name: t(KPI_I18N.downloadBytes, t('clientDistObs.kpiDownloads', '下载字节')),
      points: series.map((p) => ({ ts: p.ts, value: p.downloadBytes })),
    },
  ]
  const activeSeries: ChartSeries[] = [
    {
      key: 'active',
      name: t('clientDistObs.kpiMachines', '活跃机器'),
      points: series.map((p) => ({ ts: p.ts, value: p.activeMachines })),
    },
  ]
  const updateSeries = runtimeUpdateSeries(runtime?.updateResultSeries ?? [], t)

  const lagBuckets = distBuckets(obs.data?.lagDist ?? [], (x) => x.count, (x) => lagLabel(x.lag ?? 0, t))
  const platformBuckets = distBuckets(obs.data?.platformDist ?? [], (x) => x.count, (x) => platformLabel(x.os ?? ''))
  const versionBuckets = distBuckets(obs.data?.versionDist ?? [], (x) => x.count, (x) => `v${x.version ?? 0}`)
  const resultBuckets = distBuckets(stats?.results ?? [], (r) => r.count, (r) => resultLabel(r.result, t))

  const sparkActive = series.map((p) => ({ value: p.activeMachines }))
  const sparkRequests = series.map((p) => ({ value: p.manifestPulls + p.artifactPulls }))

  return (
    <div className="space-y-4" data-testid="ops-overview">
      <section className="space-y-2" data-testid="ops-overview-health">
        <SectionHeader title={t('clientDistOps.overview.sectionHealth', '分发健康')} />
        {obs.isLoading ? (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
            {Array.from({ length: 5 }, (_, i) => (
              <div key={i} className="h-24 animate-pulse rounded-lg bg-muted" />
            ))}
          </div>
        ) : obs.isError || !obs.data ? (
          <ErrorPanel
            title={t('clientDistOps.overview.sectionHealth', '分发健康')}
            message={t('clientDistObs.overviewUnavailable', '观测数据暂不可用')}
          />
        ) : (
          <InsightCards summary={obs.data.summary} compare={compare} />
        )}
      </section>

      <section className="space-y-2" data-testid="ops-overview-glance">
        <SectionHeader title={t('clientDistOps.overview.sectionGlance', '运行与请求速览')} />
        <p className="text-xs text-muted-foreground">
          {t('clientDistOps.overview.requestRateNote', '请求成功率=HTTP<400，不等于更新成功率')}
        </p>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
          <StatCard
            icon={<Clock className="size-3.5" />}
            label={t('clientDistOps.recentStarted', '近 5 分钟启动')}
            value={String(runtime?.summary.recentStarted ?? 0)}
            sub={t('clientDistOps.last5m', '近 5 分钟')}
          />
          <StatCard
            icon={<Clock className="size-3.5" />}
            label={t('clientDistOps.todayStarted', '今日启动')}
            value={String(runtime?.summary.todayStarted ?? 0)}
          />
          <StatCard
            icon={<RadioTower className="size-3.5" />}
            label={t('clientDistOps.overview.kpiRuntimeMachines', '运行态机器')}
            value={String(runtime?.items.length ?? 0)}
            trend={<Sparkline points={sparkActive} className="h-7" ariaLabel={t('clientDistObs.kpiMachines', '活跃机器')} />}
          />
          <StatCard
            icon={<AlertTriangle className="size-3.5" />}
            tone={lagMachines > 0 ? 'warning' : 'neutral'}
            label={t('clientDistOps.overview.kpiLagMachines', '落后版本机器')}
            value={String(lagMachines)}
          />
          <StatCard
            icon={<Download className="size-3.5" />}
            label={t(KPI_I18N.downloadRequests, t('clientDistOps.totalRequests', '请求总量'))}
            value={requestCount.toLocaleString()}
            trend={<Sparkline points={sparkRequests} className="h-7" />}
          />
          <StatCard
            icon={<Activity className="size-3.5" />}
            tone={requestRates.successRate !== null && requestRates.successRate < 0.95 ? 'warning' : 'neutral'}
            label={t(KPI_I18N.requestSuccessRate, '请求成功率')}
            value={formatKpiRate(requestRates.successRate)}
          />
        </div>

        <SectionHeader title={t('clientDistOps.overview.sectionSecurity', '安全态势')} />
        <p className="text-xs text-muted-foreground">
          {t('clientDistOps.overview.securitySnapshotNote', '近实时快照，不随页头时间窗')}
        </p>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
          <StatCard
            icon={<ShieldAlert className="size-3.5" />}
            tone={(security.data?.abnormalRequests ?? 0) > 0 ? 'warning' : 'neutral'}
            label={t('clientDistOps.overview.kpiAbnormal')}
            value={String(security.data?.abnormalRequests ?? 0)}
          />
          <StatCard
            icon={<Ban className="size-3.5" />}
            label={t('clientDistOps.overview.kpiAuth')}
            value={`${security.data?.unauthorizedRequests ?? 0}/${security.data?.forbiddenRequests ?? 0}/${security.data?.rateLimitedRequests ?? 0}`}
          />
          <StatCard
            icon={<Gauge className="size-3.5" />}
            label={t('clientDistOps.overview.kpiActiveDownloads')}
            value={String(security.data?.activeDownloads ?? (security.isLoading ? '…' : 0))}
          />
          <StatCard
            icon={<Download className="size-3.5" />}
            label={t('clientDistOps.overview.kpiBandwidth')}
            value={fmtBytesCompact(security.data?.downloadBytesPerSecond ?? 0)}
            sub="/s"
          />
          <StatCard
            icon={<ShieldCheck className="size-3.5" />}
            label={t('clientDistOps.overview.kpiBlockedIp')}
            value={String(security.data?.blockedIpCount ?? 0)}
          />
          <StatCard
            icon={<ShieldCheck className="size-3.5" />}
            label={t('clientDistOps.overview.kpiProtected')}
            value={`${security.data?.throttledKeyCount ?? 0}/${security.data?.protectedChannelCount ?? 0}`}
          />
        </div>
      </section>

      <section className="space-y-2" data-testid="ops-overview-trends">
        <SectionHeader title={t('clientDistOps.overview.sectionTrends', '趋势')} />
        <div className="grid gap-3 lg:grid-cols-2">
          <TrendCard
            title={t('clientDistObs.trendPullsTitle', '拉取趋势')}
            series={pullsSeries}
            valueFormatter={(v) => String(Math.round(v))}
            empty={t('clientDistObs.heatmapEmpty', '所选时间段内没有更新活动')}
          />
          <TrendCard
            title={t('clientDistOps.updateResultTrend', '更新结果趋势')}
            series={updateSeries}
            valueFormatter={(v) => String(Math.round(v))}
            empty={t('clientDistOps.emptyClients', '所选时间段内没有更新活动')}
          />
          <TrendCard
            title={t('clientDistObs.trendBytesTitle', '下载字节趋势')}
            series={bytesSeries}
            empty={t('clientDistObs.heatmapEmpty', '所选时间段内没有更新活动')}
          />
          <TrendCard
            title={t('clientDistOps.overview.trendActiveTitle', '活跃机器趋势')}
            series={activeSeries}
            valueFormatter={(v) => String(Math.round(v))}
            empty={t('clientDistObs.heatmapEmpty', '所选时间段内没有更新活动')}
          />
        </div>
      </section>

      <section className="space-y-2" data-testid="ops-overview-dists">
        <SectionHeader title={t('clientDistOps.overview.sectionDists', '分布')} />
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <LinkableDistPanel
            title={t('clientDistOps.lagDist', '版本滞后分布')}
            buckets={lagBuckets}
            empty={t('clientDistObs.heatmapEmpty', '所选时间段内没有更新活动')}
            onPick={(key) => onLink({ lag: parseLagLabel(key) })}
          />
          <LinkableDistPanel
            title={t('clientDistOps.platformDist', '平台分布')}
            buckets={platformBuckets}
            empty={t('clientDistObs.heatmapEmpty', '所选时间段内没有更新活动')}
            onPick={(key) => onLink({ platform: reversePlatformLabel(key) })}
          />
          <LinkableDistPanel
            title={t('clientDistOps.overview.distVersionTitle', '客户端版本分布')}
            buckets={versionBuckets}
            empty={t('clientDistObs.heatmapEmpty', '所选时间段内没有更新活动')}
            onPick={(key) => onLink({ version: Number(key.replace(/^v/, '')) })}
          />
          <LinkableDistPanel
            title={t('clientDistOps.resultDist', '请求结果分布')}
            buckets={resultBuckets}
            empty={t('clientStats.empty', '暂无数据')}
            onPick={() => onLink({})}
          />
        </div>
      </section>

      <section className="space-y-2" data-testid="ops-overview-ranks">
        <SectionHeader title={t('clientDistOps.overview.sectionRanks', '安全排行')} />
        {security.isError ? (
          <ErrorPanel
            title={t('clientDistOps.overview.sectionRanks', '安全排行')}
            message={t(
              'clientDistOps.overview.error',
              '安全总览接口暂不可用，请确认后端 /client-dist/security/overview 已落地。',
            )}
          />
        ) : (
          <div className="grid gap-4 lg:grid-cols-2 xl:grid-cols-4">
            <RankList title={t('clientDistOps.overview.rankTopIp')} items={security.data?.topIps ?? []} filterKey="ip" />
            <RankList title={t('clientDistOps.overview.rankTopKey')} items={security.data?.topKeys ?? []} />
            <RankList title={t('clientDistOps.overview.rankTopChannel')} items={security.data?.topChannels ?? []} filterKey="channelId" />
            <RankList title={t('clientDistOps.overview.rankTopPlayer')} items={security.data?.topPlayers ?? []} />
          </div>
        )}
      </section>
    </div>
  )
}

function SectionHeader({ title }: { title: string }) {
  return <h3 className="text-sm font-medium">{title}</h3>
}

function RankList({
  title,
  items,
  filterKey,
}: {
  title: string
  items: SecurityRankItem[]
  filterKey?: 'ip' | 'channelId'
}) {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  return (
    <Panel title={title}>
      {items.length === 0 ? (
        <p className="py-10 text-center text-sm text-muted-foreground">{t('clientDistOps.overview.rankEmpty')}</p>
      ) : (
        <ul className="space-y-2">
          {items.slice(0, 8).map((item) => (
            <li
              key={item.subject}
              className="flex items-center justify-between gap-3 rounded-md border bg-muted/25 px-3 py-2 text-sm"
            >
              {filterKey ? (
                <Link
                  className="min-w-0 truncate font-medium text-primary hover:underline"
                  to={buildClientDistHref('/client-dist-ops', searchParams, {
                    [filterKey]: item.subject,
                    tab: 'logs',
                    type: 'request',
                  })}
                >
                  {item.subject || '—'}
                </Link>
              ) : (
                <span className="min-w-0 truncate font-medium">{item.subject || '—'}</span>
              )}
              <span className="shrink-0 text-muted-foreground">
                {t('clientDistOps.overview.rankCount', { n: item.count })}
                {item.bytes ? ` · ${fmtBytesCompact(item.bytes)}` : ''}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}
