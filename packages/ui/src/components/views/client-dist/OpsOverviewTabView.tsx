/**
 * @file OpsOverviewTabView：页面 B · 总览 Tab（分发健康 + 运行态/请求侧速览 + 趋势 + 分布 + 安全排行）的受控视图。
 *       更新侧观测、安全态势快照与运行态数据的取数，以及排行深链构造与跳转，均由应用容器负责。
 * @input lib/client-dist-stats-contracts（ClientDistStats/ClientDistObservability）、
 *        lib/client-dist-security-contracts（ClientDistSecurityOverview/SecurityRankItem）、
 *        lib/client-runtime-contracts（ClientRuntimeOverview）、lib/client-dist-kpi（KPI_I18N/formatKpiRate/resolveRequestRates）、
 *        views/client-dist/InsightCards（洞察卡）、views/client-dist/OpsShared（面板/趋势卡/分布与格式化）、
 *        StatCard/Sparkline/Panel 原语、lucide 图标、翻译上下文
 * @output OpsOverviewTabView、OpsOverviewTabViewProps、RankFilterKey、OpsOverviewLinkRenderer
 * @sync apps/control-plane-web/src/components/client-dist/OpsOverviewTab.tsx、
 *       apps/control-plane-web/src/components/client-dist/OpsOverviewTab.dom.test.tsx
 * @since FR-502（组件受控化迁包；原 FR-430 / FR-428 / FR-356 页面 B 总览 Tab）
 */
import type { ReactNode } from 'react'
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
import { Panel } from '@jianmanager/ui/components/panel'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { Sparkline } from '@jianmanager/ui'
import type { ChartSeries } from '@jianmanager/ui'
import type { ClientDistObservability, ClientDistStats } from '../../../lib/client-dist-stats-contracts'
import type { ClientDistSecurityOverview, SecurityRankItem } from '../../../lib/client-dist-security-contracts'
import type { ClientRuntimeOverview } from '../../../lib/client-runtime-contracts'
import { KPI_I18N, formatKpiRate, resolveRequestRates } from '../../../lib/client-dist-kpi'
import { InsightCards } from './InsightCards'
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
} from './OpsShared'

/** 可下钻的排行维度（仅这两类在日志页有对应过滤键，沿用原实现）。 */
export type RankFilterKey = 'ip' | 'channelId'

/** 排行条目深链渲染器：视图给出目标与样式类名，容器提供 router 语义（保持 SPA 导航）。 */
export type OpsOverviewLinkRenderer = (args: { to: string; className: string; children: ReactNode }) => ReactNode

/** 排行主体链接样式（视图持有，随渲染器一并注入）。 */
const RANK_LINK_CLASS = 'min-w-0 truncate font-medium text-primary hover:underline'

/**
 * 总览 Tab 的注入契约（受控视图，ADR-097 a 范式）。
 *
 * 受控边界：更新侧观测、安全态势与运行态数据经 props 注入（应用容器调
 * `useClientDistObservability` / `useClientDistSecurityOverview`，运行态与请求侧由页面持有）；
 * 分布面板下钻经 `onLink` 上报，排行深链目标经 `rankLogsHref` 注入、锚点渲染经 `renderLink` 注入。
 * 视图只保留展示与口径计算（KPI 口径见 FR-356：更新侧率只信 observability，请求侧率只信 stats）。
 */
export interface OpsOverviewTabViewProps {
  /** 请求侧统计（`/client-dist/stats`）；缺省按 0 / 空展示。 */
  stats?: ClientDistStats
  /** 运行态总览（页面持有）；缺省按 0 展示。 */
  runtime?: ClientRuntimeOverview
  /** 更新侧观测数据（含 series/summary/compare）；缺省按空展示。 */
  observability?: ClientDistObservability
  /** 更新侧取数中：健康区展示骨架卡。 */
  obsLoading: boolean
  /** 更新侧取数失败或数据缺失：健康区展示错误面板。 */
  obsError: boolean
  /** 安全态势近实时快照；缺省按 0 展示。 */
  security?: ClientDistSecurityOverview
  /** 安全态势取数中：活跃下载展示省略号占位。 */
  securityLoading: boolean
  /** 安全态势取数失败：安全排行展示错误面板。 */
  securityError: boolean
  /** 分布面板下钻（容器接路由跳转）。 */
  onLink: (link: RuntimeLink) => void
  /** 排行主体 → 全量日志深链（目标依赖当前查询串，故由容器构造）。 */
  rankLogsHref: (filterKey: RankFilterKey, subject: string) => string
  /** 深链渲染（容器注入 router Link）；缺省退化为原生 `<a href>`，便于无路由环境独立渲染。 */
  renderLink?: OpsOverviewLinkRenderer
}

export function OpsOverviewTabView({
  stats,
  runtime,
  observability,
  obsLoading,
  obsError,
  security,
  securityLoading,
  securityError,
  onLink,
  rankLogsHref,
  renderLink,
}: OpsOverviewTabViewProps) {
  const { t } = useTranslation()

  const series = observability?.series ?? []
  const compare = observability?.compare

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

  const lagBuckets = distBuckets(observability?.lagDist ?? [], (x) => x.count, (x) => lagLabel(x.lag ?? 0, t))
  const platformBuckets = distBuckets(observability?.platformDist ?? [], (x) => x.count, (x) => platformLabel(x.os ?? ''))
  const versionBuckets = distBuckets(observability?.versionDist ?? [], (x) => x.count, (x) => `v${x.version ?? 0}`)
  const resultBuckets = distBuckets(stats?.results ?? [], (r) => r.count, (r) => resultLabel(r.result, t))

  const sparkActive = series.map((p) => ({ value: p.activeMachines }))
  const sparkRequests = series.map((p) => ({ value: p.manifestPulls + p.artifactPulls }))

  return (
    <div className="space-y-4" data-testid="ops-overview">
      <section className="space-y-2" data-testid="ops-overview-health">
        <SectionHeader title={t('clientDistOps.overview.sectionHealth', '分发健康')} />
        {obsLoading ? (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
            {Array.from({ length: 5 }, (_, i) => (
              <div key={i} className="h-24 animate-pulse rounded-lg bg-muted" />
            ))}
          </div>
        ) : obsError || !observability ? (
          <ErrorPanel
            title={t('clientDistOps.overview.sectionHealth', '分发健康')}
            message={t('clientDistObs.overviewUnavailable', '观测数据暂不可用')}
          />
        ) : (
          <InsightCards summary={observability.summary} compare={compare} />
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
            tone={(security?.abnormalRequests ?? 0) > 0 ? 'warning' : 'neutral'}
            label={t('clientDistOps.overview.kpiAbnormal')}
            value={String(security?.abnormalRequests ?? 0)}
          />
          <StatCard
            icon={<Ban className="size-3.5" />}
            label={t('clientDistOps.overview.kpiAuth')}
            value={`${security?.unauthorizedRequests ?? 0}/${security?.forbiddenRequests ?? 0}/${security?.rateLimitedRequests ?? 0}`}
          />
          <StatCard
            icon={<Gauge className="size-3.5" />}
            label={t('clientDistOps.overview.kpiActiveDownloads')}
            value={String(security?.activeDownloads ?? (securityLoading ? '…' : 0))}
          />
          <StatCard
            icon={<Download className="size-3.5" />}
            label={t('clientDistOps.overview.kpiBandwidth')}
            value={fmtBytesCompact(security?.downloadBytesPerSecond ?? 0)}
            sub="/s"
          />
          <StatCard
            icon={<ShieldCheck className="size-3.5" />}
            label={t('clientDistOps.overview.kpiBlockedIp')}
            value={String(security?.blockedIpCount ?? 0)}
          />
          <StatCard
            icon={<ShieldCheck className="size-3.5" />}
            label={t('clientDistOps.overview.kpiProtected')}
            value={`${security?.throttledKeyCount ?? 0}/${security?.protectedChannelCount ?? 0}`}
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
        {securityError ? (
          <ErrorPanel
            title={t('clientDistOps.overview.sectionRanks', '安全排行')}
            message={t(
              'clientDistOps.overview.error',
              '安全总览接口暂不可用，请确认后端 /client-dist/security/overview 已落地。',
            )}
          />
        ) : (
          <div className="grid gap-4 lg:grid-cols-2 xl:grid-cols-4">
            <RankList
              title={t('clientDistOps.overview.rankTopIp')}
              items={security?.topIps ?? []}
              filterKey="ip"
              rankLogsHref={rankLogsHref}
              renderLink={renderLink}
            />
            <RankList title={t('clientDistOps.overview.rankTopKey')} items={security?.topKeys ?? []} rankLogsHref={rankLogsHref} renderLink={renderLink} />
            <RankList
              title={t('clientDistOps.overview.rankTopChannel')}
              items={security?.topChannels ?? []}
              filterKey="channelId"
              rankLogsHref={rankLogsHref}
              renderLink={renderLink}
            />
            <RankList title={t('clientDistOps.overview.rankTopPlayer')} items={security?.topPlayers ?? []} rankLogsHref={rankLogsHref} renderLink={renderLink} />
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
  rankLogsHref,
  renderLink,
}: {
  title: string
  items: SecurityRankItem[]
  /** 可下钻维度；缺省时条目主体为纯文本（密钥/玩家名排行不下钻）。 */
  filterKey?: RankFilterKey
  rankLogsHref: (filterKey: RankFilterKey, subject: string) => string
  renderLink?: OpsOverviewLinkRenderer
}) {
  const { t } = useTranslation()
  return (
    <Panel title={title}>
      {items.length === 0 ? (
        <p className="py-10 text-center text-sm text-muted-foreground">{t('clientDistOps.overview.rankEmpty')}</p>
      ) : (
        <ul className="space-y-2">
          {items.slice(0, 8).map((item) => {
            const label = item.subject || '—'
            return (
              <li
                key={item.subject}
                className="flex items-center justify-between gap-3 rounded-md border bg-muted/25 px-3 py-2 text-sm"
              >
                {filterKey ? (
                  renderLink ? (
                    renderLink({ to: rankLogsHref(filterKey, item.subject), className: RANK_LINK_CLASS, children: label })
                  ) : (
                    <a className={RANK_LINK_CLASS} href={rankLogsHref(filterKey, item.subject)}>
                      {label}
                    </a>
                  )
                ) : (
                  <span className="min-w-0 truncate font-medium">{label}</span>
                )}
                <span className="shrink-0 text-muted-foreground">
                  {t('clientDistOps.overview.rankCount', { n: item.count })}
                  {item.bytes ? ` · ${fmtBytesCompact(item.bytes)}` : ''}
                </span>
              </li>
            )
          })}
        </ul>
      )}
    </Panel>
  )
}
