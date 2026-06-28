import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Download, Users, RefreshCw, Server, Boxes } from 'lucide-react'
import { useClientChannels } from '@/api/clientChannels'
import {
  useClientDistObservability,
  type ClientDistSeriesPoint,
  type ClientDistDistItem,
} from '@/api/clientStats'
import { useAuthStore } from '@/stores/auth'
import { Panel } from '@/components/ui/panel'
import { StatCard } from '@/components/ui/stat-card'
import { MiniBar } from '@/components/ui/mini-bar'
import { RangePicker, type MetricRange } from '@/components/charts/RangePicker'
import { TimeSeriesChart, type ChartSeries } from '@/components/charts/TimeSeriesChart'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { DistBucket } from '@/lib/platform-stats'

const ROLE_PLATFORM_ADMIN = 10

/** 客户端分发观测端点的区间枚举（24h/7d/30d/90d/180d，ADR-049）。 */
type ObsRange = '24h' | '7d' | '30d' | '90d' | '180d'

/** 「总」频道哨兵值（Select 不接受空串 value）：选它时不向端点传 channelId，取跨频道汇总。 */
const ALL_CHANNELS = '__all__'

/** 把页面统一的 MetricRange 映射到分发观测端点支持的区间枚举（小时档归 24h，180d 由 90d 升）。 */
function toObsRange(r: MetricRange): ObsRange {
  switch (r) {
    case '1h':
    case '6h':
    case '24h':
      return '24h'
    case '7d':
      return '7d'
    case '30d':
      return '30d'
    case '90d':
      return '90d'
    default:
      return '7d'
  }
}

/** 字节 → 紧凑可读（G/M/K）。 */
function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  if (b >= 1e3) return `${(b / 1024).toFixed(0)}K`
  return String(b)
}

/** 率（0~1 小数）→ 百分比字符串。 */
function fmtRate(r: number): string {
  return `${((Number.isFinite(r) ? r : 0) * 100).toFixed(1)}%`
}

/** 平台 os 标识 → 展示名（空串=未知）。 */
function platformLabel(os: string): string {
  if (!os) return '—'
  const map: Record<string, string> = { windows: 'Windows', macos: 'macOS', linux: 'Linux' }
  return map[os] ?? os
}

/**
 * 把分发观测分布数组（含真实 count）转为按 count 降序的占比桶。
 * 与 StatisticsPage 的 `tallyBy`（按出现次数计数）不同：本处保留端点返回的真实计数，占比以区间总计为分母。
 */
function distBuckets(items: ClientDistDistItem[], label: (it: ClientDistDistItem) => string): DistBucket[] {
  const total = items.reduce((s, it) => s + it.count, 0)
  return items
    .map((it) => ({ key: label(it), count: it.count, pct: total > 0 ? it.count / total : 0 }))
    .sort((a, b) => b.count - a.count)
}

/** 构成分布面板：标签 + 计数 + 占比条（信息色，占比高不视作异常）。空集显占位。 */
function DistPanel({ title, buckets, empty }: { title: string; buckets: DistBucket[]; empty: string }) {
  return (
    <Panel title={title}>
      {buckets.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">{empty}</p>
      ) : (
        <ul className="space-y-2.5">
          {buckets.map((b) => (
            <li key={b.key} className="space-y-1">
              <div className="flex items-baseline justify-between text-sm">
                <span className="font-medium">{b.key}</span>
                <span className="tabular-nums text-muted-foreground">
                  {b.count}
                  <span className="ml-1.5 text-xs">{(b.pct * 100).toFixed(0)}%</span>
                </span>
              </div>
              <MiniBar value={b.pct * 100} level="info" />
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

/** 时序趋势图卡：套 Panel 标题 + 内部 TimeSeriesChart。 */
function TrendCard({
  title,
  series,
  valueFormatter,
  yDomain,
  empty,
}: {
  title: string
  series: ChartSeries[]
  valueFormatter?: (v: number) => string
  yDomain?: [number | 'auto', number | 'auto']
  empty: string
}) {
  return (
    <Panel title={title} bodyClassName="p-3">
      <TimeSeriesChart series={series} valueFormatter={valueFormatter} yDomain={yDomain} emptyHint={empty} />
    </Panel>
  )
}

/**
 * 观测·客户端分发监控页（FR-218，消费 FR-217 观测底座 `GET /client-dist/observability`）。
 * 总览（不筛=跨频道汇总）+ 频道筛选器（下拉选单频道）；内容三段：
 * ① KPI 卡（区间汇总 summary）② 时序趋势（拉取/更新次数·活跃客户端·成功率随时间，消费 series）
 * ③ 分布/榜单（版本/平台/滞后 distBuckets）。复用既有 Panel/StatCard/RangePicker/TimeSeriesChart，零后端改动。
 * **平台管理员**端点：非管理员整页降级为权限提示（不发起请求）。与 FR-220 统计页（平台规模与构成）互补——本页偏分发时序与多维构成。
 * i18n（FR-016）+ 暗/亮色 + 双主题（FR-164，图表用主题 token）。
 */
export default function ClientDistMonitoringPage() {
  const { t } = useTranslation()
  const [range, setRange] = useState<MetricRange>('7d')
  const [channel, setChannel] = useState<string>(ALL_CHANNELS)

  const isPlatformAdmin = useAuthStore((s) => s.role) === ROLE_PLATFORM_ADMIN
  const { data: channels } = useClientChannels()

  // 分发观测仅平台管理员可查；非管理员不发起请求（enabled=false），整页降级。
  const channelId = channel === ALL_CHANNELS ? undefined : channel
  const { data, isError, isLoading } = useClientDistObservability({
    channelId,
    range: toObsRange(range),
    enabled: isPlatformAdmin,
  })

  const summary = data?.summary
  const series = data?.series ?? []

  // 时序派生：单图叠拉取/制品两线；更新成功率按桶 success/total 现算（端点 series 无率字段）。
  const pullSeries: ChartSeries[] = [
    {
      key: 'manifestPulls',
      name: t('clientDistMonitor.manifestPulls'),
      points: series.map((p: ClientDistSeriesPoint) => ({ ts: p.ts, value: p.manifestPulls })),
    },
    {
      key: 'artifactPulls',
      name: t('clientDistMonitor.artifactPulls'),
      points: series.map((p: ClientDistSeriesPoint) => ({ ts: p.ts, value: p.artifactPulls })),
    },
  ]
  const activeSeries: ChartSeries[] = [
    {
      key: 'activeMachines',
      name: t('clientDistMonitor.activeClients'),
      points: series.map((p: ClientDistSeriesPoint) => ({ ts: p.ts, value: p.activeMachines })),
    },
  ]
  const updateSeries: ChartSeries[] = [
    {
      key: 'updateTotal',
      name: t('clientDistMonitor.updateTotal'),
      points: series.map((p: ClientDistSeriesPoint) => ({ ts: p.ts, value: p.updateTotal })),
    },
    {
      key: 'updateSuccess',
      name: t('clientDistMonitor.updateSuccess'),
      points: series.map((p: ClientDistSeriesPoint) => ({ ts: p.ts, value: p.updateSuccess })),
    },
  ]
  const successRateSeries: ChartSeries[] = [
    {
      key: 'successRate',
      name: t('clientDistMonitor.successRate'),
      points: series.map((p: ClientDistSeriesPoint) => ({
        ts: p.ts,
        value: p.updateTotal > 0 ? (p.updateSuccess / p.updateTotal) * 100 : null,
      })),
    },
  ]

  const versionBuckets = distBuckets(data?.versionDist ?? [], (v) => `v${v.version ?? '—'}`)
  const platformBuckets = distBuckets(data?.platformDist ?? [], (p) => platformLabel(p.os ?? ''))
  const lagBuckets = distBuckets(data?.lagDist ?? [], (l) =>
    l.lag === 0
      ? t('clientDistMonitor.lagLatest')
      : t('clientDistMonitor.lagBehind', { n: l.lag ?? 0 }),
  )

  // 频道筛选器（总 + 各频道）。
  const channelPicker = (
    <Select value={channel} onValueChange={setChannel}>
      <SelectTrigger size="sm" className="w-44">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL_CHANNELS}>{t('clientDistMonitor.allChannels')}</SelectItem>
        {(channels ?? []).map((c) => (
          <SelectItem key={c.channelId} value={c.channelId}>
            {c.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold">{t('clientDistMonitor.title')}</h1>
          <p className="mt-0.5 text-xs text-muted-foreground">{t('clientDistMonitor.subtitle')}</p>
        </div>
        <div className="flex items-center gap-2">
          {isPlatformAdmin && channelPicker}
          <RangePicker value={range} onChange={setRange} />
        </div>
      </div>

      {!isPlatformAdmin ? (
        <Panel title={t('clientDistMonitor.title')}>
          <p className="py-10 text-center text-sm text-muted-foreground">
            {t('clientDistMonitor.adminOnly')}
          </p>
        </Panel>
      ) : isError ? (
        <Panel title={t('clientDistMonitor.title')}>
          <p className="py-10 text-center text-sm text-muted-foreground">
            {t('clientDistMonitor.loadError')}
          </p>
        </Panel>
      ) : (
        <>
          {/* ① 区间 KPI 卡 */}
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
            <StatCard
              icon={<Download className="size-3.5" />}
              label={t('clientDistMonitor.manifestPulls')}
              value={String(summary?.manifestPulls ?? 0)}
              sub={t('clientDistMonitor.distribution')}
            />
            <StatCard
              icon={<Download className="size-3.5" />}
              label={t('clientDistMonitor.artifactPulls')}
              value={String(summary?.artifactPulls ?? 0)}
            />
            <StatCard
              icon={<Server className="size-3.5" />}
              label={t('clientDistMonitor.downloadBytes')}
              value={fmtBytes(summary?.downloadBytes ?? 0)}
            />
            <StatCard
              icon={<Users className="size-3.5" />}
              label={t('clientDistMonitor.activeClients')}
              value={String(summary?.activeMachines ?? 0)}
              sub={
                summary?.activeMachinesExact === false
                  ? t('clientDistMonitor.approx')
                  : summary?.activeMachinesExact
                    ? t('clientDistMonitor.exact')
                    : undefined
              }
            />
            <StatCard
              icon={<RefreshCw className="size-3.5" />}
              tone="success"
              label={t('clientDistMonitor.successRate')}
              value={fmtRate(summary?.successRate ?? 0)}
            />
            <StatCard
              icon={<Boxes className="size-3.5" />}
              tone="info"
              label={t('clientDistMonitor.casHitRate')}
              value={fmtRate(summary?.casHitRate ?? 0)}
            />
          </div>

          {/* ② 时序趋势 */}
          <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
            <TrendCard
              title={t('clientDistMonitor.trendPulls')}
              series={pullSeries}
              valueFormatter={(v) => String(Math.round(v))}
              empty={t('clientDistMonitor.empty')}
            />
            <TrendCard
              title={t('clientDistMonitor.trendActive')}
              series={activeSeries}
              valueFormatter={(v) => String(Math.round(v))}
              empty={t('clientDistMonitor.empty')}
            />
            <TrendCard
              title={t('clientDistMonitor.trendUpdates')}
              series={updateSeries}
              valueFormatter={(v) => String(Math.round(v))}
              empty={t('clientDistMonitor.empty')}
            />
            <TrendCard
              title={t('clientDistMonitor.trendSuccessRate')}
              series={successRateSeries}
              valueFormatter={(v) => `${Math.round(v)}%`}
              yDomain={[0, 100]}
              empty={t('clientDistMonitor.empty')}
            />
          </div>

          {/* ③ 分布 / 榜单 */}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <DistPanel
              title={t('clientDistMonitor.versionDist')}
              buckets={versionBuckets}
              empty={isLoading ? t('common.loading') : t('clientDistMonitor.empty')}
            />
            <DistPanel
              title={t('clientDistMonitor.platformDist')}
              buckets={platformBuckets}
              empty={isLoading ? t('common.loading') : t('clientDistMonitor.empty')}
            />
            <DistPanel
              title={t('clientDistMonitor.lagDist')}
              buckets={lagBuckets}
              empty={isLoading ? t('common.loading') : t('clientDistMonitor.empty')}
            />
          </div>
        </>
      )}
    </div>
  )
}
