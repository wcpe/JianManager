import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useClientStats } from '@/api/clientStats'
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

/**
 * 客户端分发统计看板（FR-095，见 ADR-023）。
 * 只读聚合 FR-093/094/092：下载趋势 + 版本分布 + 成功率/回退率 + 活跃机器码 + 来源 IP。
 * i18n（FR-016）+ 暗/亮色（FR-026，图表用主题 token）。
 */
export default function ClientStatsPanel({ channelId }: { channelId: string }) {
  const { t } = useTranslation()
  const [days, setDays] = useState(30)
  const { data, isLoading } = useClientStats(channelId, days)

  const downloadSeries: ChartSeries[] = [
    {
      key: 'requests',
      name: t('clientStats.downloads', '下载量'),
      points: (data?.downloads ?? []).map((d) => ({ ts: d.day, value: d.requests })),
    },
  ]
  const maxVersionReq = Math.max(1, ...(data?.versions ?? []).map((v) => v.requests))
  const pct = (r: number) => `${(r * 100).toFixed(1)}%`

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

      {/* 数字卡：活跃机器码 / 成功率 / 回退率 */}
      <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
        <StatCard label={t('clientStats.activeMachines', '活跃机器码')} value={String(data?.activeMachines ?? 0)} />
        <StatCard label={t('clientStats.successRate', '更新成功率')} value={data ? pct(data.successRate) : '-'} />
        <StatCard label={t('clientStats.rollbackRate', '回退率')} value={data ? pct(data.rollbackRate) : '-'} />
      </div>

      {/* 下载趋势 */}
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

      {/* 版本分布 */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientStats.versionDist', '版本分布')}</h3>
        <div className="border rounded-lg p-3 space-y-2">
          {(data?.versions ?? []).length === 0 && !isLoading && (
            <p className="text-xs text-muted-foreground">{t('clientStats.empty', '暂无数据')}</p>
          )}
          {(data?.versions ?? []).map((v) => (
            <div key={v.version} className="flex items-center gap-2 text-xs">
              <span className="w-12 font-mono shrink-0">v{v.version}</span>
              <div className="flex-1 bg-muted rounded h-4 overflow-hidden">
                <div
                  className="bg-primary h-full"
                  style={{ width: `${(v.requests / maxVersionReq) * 100}%` }}
                />
              </div>
              <span className="w-12 text-right tabular-nums shrink-0">{v.requests}</span>
            </div>
          ))}
        </div>
      </section>

      {/* 来源 IP Top 10 */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientStats.topIps', '来源 IP（Top 10）')}</h3>
        <div className="border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-muted">
              <tr>
                <th className="p-2 text-left">{t('clientStats.ip', 'IP')}</th>
                <th className="p-2 text-left">{t('clientStats.requestCount', '请求数')}</th>
              </tr>
            </thead>
            <tbody>
              {(data?.topIps ?? []).map((row) => (
                <tr key={row.ip} className="border-t">
                  <td className="p-2 font-mono text-xs">{row.ip}</td>
                  <td className="p-2 tabular-nums">{row.count}</td>
                </tr>
              ))}
              {(data?.topIps ?? []).length === 0 && !isLoading && (
                <tr>
                  <td colSpan={2} className="p-3 text-center text-muted-foreground">
                    {t('clientStats.empty', '暂无数据')}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>

      {/* 流量合计（信息性） */}
      {data && data.downloads.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {t('clientStats.totalBytes', '窗口内流量合计')}：
          {formatBytes(data.downloads.reduce((s, d) => s + d.bytes, 0))}
        </p>
      )}
    </div>
  )
}

/** 数字统计卡。 */
function StatCard({ label, value }: { label: string; value: string }) {
  return (
    <div className="border rounded-lg p-4">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="text-2xl font-semibold mt-1 tabular-nums">{value}</div>
    </div>
  )
}
