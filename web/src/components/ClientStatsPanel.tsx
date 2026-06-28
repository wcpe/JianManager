import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useClientStats } from '@/api/clientStats'
import {
  useClientDistObservability,
  type ObservabilityRange,
  type ObservabilityPlatformDist,
  type ObservabilityVersionDist,
  type ObservabilityLagDist,
} from '@/api/clientDistObservability'
import { TimeSeriesChart, type ChartSeries } from '@/components/charts/TimeSeriesChart'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

/** 字节数转人类可读。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

/** 天数窗口 → 观测端点 range 枚举（两看板共用一个时间选择器）。 */
const DAYS_TO_RANGE: Record<number, ObservabilityRange> = { 7: '7d', 30: '30d', 90: '90d' }

/** 平台 os 标识 → 展示名（空串=未知）。 */
function platformLabel(os: string): string {
  if (!os) return '—'
  const map: Record<string, string> = { windows: 'Windows', macos: 'macOS', linux: 'Linux' }
  return map[os] ?? os
}

/**
 * 客户端分发统计看板（FR-095 + FR-217/FR-219，见 ADR-023/ADR-049）。
 * 在 FR-095 单频道按日看板（下载趋势 / 来源 IP）之上，复用 FR-217 观测时序底座扩充维度：
 * 活跃客户端（去重含精确/近似标注）、版本分布与滞后占比、更新成功率/fail-static 率、平台分布。
 * i18n（FR-016）+ 暗/亮色（FR-026，图表用主题 token）。
 */
export default function ClientStatsPanel({ channelId }: { channelId: string }) {
  const { t } = useTranslation()
  const [days, setDays] = useState(30)
  const { data, isLoading } = useClientStats(channelId, days)
  const obsRange = DAYS_TO_RANGE[days] ?? '30d'
  const { data: obs, isLoading: obsLoading } = useClientDistObservability(channelId, obsRange)

  const downloadSeries: ChartSeries[] = [
    {
      key: 'requests',
      name: t('clientStats.downloads', '下载量'),
      points: (data?.downloads ?? []).map((d) => ({ ts: d.day, value: d.requests })),
    },
  ]
  const maxIpReq = Math.max(1, ...(data?.topIps ?? []).map((r) => r.count))
  const pct = (r: number) => `${(r * 100).toFixed(1)}%`

  // 观测维度派生（FR-219）：活跃客户端取观测精确去重，回退 FR-095 机器码计数。
  const summary = obs?.summary
  const activeClients = summary?.activeMachines ?? data?.activeMachines ?? 0
  const activeExact = summary?.activeMachinesExact
  const versionDist = obs?.versionDist ?? []
  const platformDist = obs?.platformDist ?? []
  const lagDist = obs?.lagDist ?? []

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <p className="text-sm text-muted-foreground max-w-2xl">
          {t('clientStats.subtitle', '分发统计（来自拉取追踪与遥测聚合）。机器码不可信，仅作统计维度。')}
        </p>
        <Select value={String(days)} onValueChange={(v: string) => setDays(Number(v))}>
          <SelectTrigger size="sm" className="w-32">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="7">{t('clientStats.days7', '近 7 天')}</SelectItem>
            <SelectItem value="30">{t('clientStats.days30', '近 30 天')}</SelectItem>
            <SelectItem value="90">{t('clientStats.days90', '近 90 天')}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      {/* 数字卡：活跃客户端（去重）/ 更新成功率 / fail-static 率 / 回退率 */}
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <StatCard
          label={t('clientStats.activeClients', '活跃客户端')}
          value={String(activeClients)}
          hint={
            activeExact === undefined
              ? undefined
              : activeExact
                ? t('clientStats.activeExact', '精确去重')
                : t('clientStats.activeApprox', '人次近似')
          }
        />
        <StatCard
          label={t('clientStats.successRate', '更新成功率')}
          value={summary ? pct(summary.successRate) : data ? pct(data.successRate) : '-'}
        />
        <StatCard
          label={t('clientStats.failStaticRate', 'fail-static 率')}
          value={summary ? pct(summary.failStaticRate) : '-'}
          hint={t('clientStats.failStaticHint', '断网兜底启动')}
        />
        <StatCard
          label={t('clientStats.rollbackRate', '回退率')}
          value={summary ? pct(summary.rollbackRate) : data ? pct(data.rollbackRate) : '-'}
        />
      </div>

      {/* 下载趋势（FR-095） */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientStats.downloadTrend', '下载量趋势')}</h3>
        <div className="border rounded-lg p-3">
          <TimeSeriesChart
            series={downloadSeries}
            valueFormatter={(v) => String(Math.round(v))}
            emptyHint={t('clientStats.empty', '暂无数据')}
          />
        </div>
      </section>

      {/* 版本分布（FR-217 占比，按拉取量降序） */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientStats.versionDist', '版本分布')}</h3>
        <DistBars
          items={versionDist.map((v: ObservabilityVersionDist) => ({
            key: String(v.version),
            label: `v${v.version}`,
            count: v.count,
          }))}
          loading={obsLoading}
          emptyHint={t('clientStats.empty', '暂无数据')}
        />
      </section>

      {/* 版本滞后分布（FR-217：current - toVersion，0=已最新） */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientStats.lagDist', '版本滞后分布')}</h3>
        <DistBars
          items={lagDist.map((l: ObservabilityLagDist) => ({
            key: String(l.lag),
            label:
              l.lag === 0
                ? t('clientStats.lagLatest', '已最新')
                : t('clientStats.lagBehind', '落后 {{n}} 版', { n: l.lag }),
            count: l.count,
          }))}
          loading={obsLoading}
          emptyHint={t('clientStats.empty', '暂无数据')}
        />
      </section>

      {/* 平台分布（FR-217：来源遥测 os 占比） */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientStats.platformDist', '平台分布')}</h3>
        <DistBars
          items={platformDist.map((p: ObservabilityPlatformDist) => ({
            key: p.os || 'unknown',
            label: platformLabel(p.os),
            count: p.count,
          }))}
          loading={obsLoading}
          emptyHint={t('clientStats.empty', '暂无数据')}
        />
      </section>

      {/* 来源 IP Top 10（FR-095） */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientStats.topIps', '来源 IP（Top 10）')}</h3>
        <div className="space-y-2 rounded-lg border p-3">
          {(data?.topIps ?? []).length === 0 && !isLoading && (
            <p className="text-xs text-muted-foreground">{t('clientStats.empty', '暂无数据')}</p>
          )}
          {(data?.topIps ?? []).map((row) => (
            <div key={row.ip} className="flex items-center gap-2 text-xs">
              <span className="w-28 shrink-0 truncate font-mono" title={row.ip}>{row.ip}</span>
              <div className="h-4 flex-1 overflow-hidden rounded bg-muted">
                <div className="h-full bg-primary" style={{ width: `${(row.count / maxIpReq) * 100}%` }} />
              </div>
              <span className="w-12 shrink-0 text-right tabular-nums">{row.count}</span>
            </div>
          ))}
        </div>
      </section>

      {/* 流量合计（信息性） */}
      {data && data.downloads.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {t('clientStats.totalBytes', '窗口内流量合计')}{' '}
          {formatBytes(data.downloads.reduce((s, d) => s + d.bytes, 0))}
        </p>
      )}
    </div>
  )
}

/** 数字统计卡（可带次级提示，如去重口径标注）。 */
function StatCard({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="border rounded-lg p-4">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="text-2xl font-semibold mt-1 tabular-nums">{value}</div>
      {hint && <div className="mt-0.5 text-[11px] text-muted-foreground">{hint}</div>}
    </div>
  )
}

/** 一项分布条数据。 */
interface DistItem {
  key: string
  label: string
  count: number
}

/**
 * 占比分布条列表（版本 / 滞后 / 平台共用）。
 * 条宽按相对最大值，右侧同时显示占总数百分比与绝对计数。
 */
function DistBars({
  items,
  loading,
  emptyHint,
}: {
  items: DistItem[]
  loading: boolean
  emptyHint: string
}) {
  const total = items.reduce((s, it) => s + it.count, 0)
  const max = Math.max(1, ...items.map((it) => it.count))
  if (items.length === 0) {
    return (
      <div className="border rounded-lg p-3">
        {!loading && <p className="text-xs text-muted-foreground">{emptyHint}</p>}
      </div>
    )
  }
  return (
    <div className="border rounded-lg p-3 space-y-2">
      {items.map((it) => (
        <div key={it.key} className="flex items-center gap-2 text-xs">
          <span className="w-20 shrink-0 truncate font-mono" title={it.label}>{it.label}</span>
          <div className="flex-1 bg-muted rounded h-4 overflow-hidden">
            <div className="bg-primary h-full" style={{ width: `${(it.count / max) * 100}%` }} />
          </div>
          <span className="w-12 text-right tabular-nums text-muted-foreground shrink-0">
            {total > 0 ? `${((it.count / total) * 100).toFixed(0)}%` : '—'}
          </span>
          <span className="w-12 text-right tabular-nums shrink-0">{it.count}</span>
        </div>
      ))}
    </div>
  )
}
