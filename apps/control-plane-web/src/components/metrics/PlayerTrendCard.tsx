import { useTranslation } from 'react-i18next'
import { Users } from 'lucide-react'
import { usePlayerTrend } from '@/api/metrics'
import { Panel } from '@jianmanager/ui/components/panel'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { Sparkline } from '@jianmanager/ui'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import type { MetricRange } from '@jianmanager/ui'

/** 24 时段柱状图的 props。 */
interface HourlyBarsProps {
  /** 24 个小时桶的均值（索引即小时）。 */
  dist: number[]
  /** 峰值（用于柱高归一）；<= 0 时回退到 dist 自身最大值。 */
  peak: number
}

/** 24 时段柱状图（纯 div，避免为一屏柱图引入 recharts + ResizeObserver，jsdom 下也可断言）。 */
export function HourlyBars({ dist, peak }: HourlyBarsProps) {
  const { t } = useTranslation()
  const max = peak > 0 ? peak : Math.max(...dist, 0)
  return (
    <div className="flex h-24 items-end gap-[2px]" data-testid="hourly-dist">
      {dist.map((v, h) => (
        <div
          key={h}
          className="flex min-w-0 flex-1 flex-col items-center justify-end"
          title={`${t('playerTrend.hour', { hour: h })}: ${v.toFixed(1)}`}
        >
          <div
            className="w-full rounded-t-[2px] bg-primary/70"
            style={{ height: `${max > 0 ? Math.max(1, (v / max) * 88) : 1}px` }}
          />
        </div>
      ))}
    </div>
  )
}

/** 玩家在线趋势卡的 props。 */
interface PlayerTrendCardProps {
  /** 统计窗口（同时决定后端聚合粒度，回显在「粒度」卡上）。 */
  range: MetricRange
}

/**
 * 玩家在线趋势卡（FR-469）：全网合计曲线 + 24 时段分布 + 峰值/日均。
 *
 * 时段口径：后端按 `tz` 参数（前端下发浏览器时区）分桶。M4 修复后，若该时区在后端不可解析
 * （宿主缺 zoneinfo 等），后端**回退 UTC** 并在 `timezone` 字段标注回退来源——
 * 卡片角标直接展示实际生效时区，故「下发了什么」与「实际用了什么」不会被误读。
 * 时段分布固定 24 项；半小时偏移时区（如 Asia/Kolkata）会把整点桶标注到 :30，见 API.md 口径注记。
 */
export function PlayerTrendCard({ range }: PlayerTrendCardProps) {
  const { t } = useTranslation()
  const tz = typeof Intl !== 'undefined' ? Intl.DateTimeFormat().resolvedOptions().timeZone : undefined
  const { data, isError, isLoading } = usePlayerTrend({ range, tz })
  const points = (data?.trend ?? []).map((p) => ({ value: p.avg }))
  const peakAt = data?.peakAt ? new Date(data.peakAt).toLocaleString() : null
  // 后端回退时 `timezone` 形如 "UTC (fallback from Not/AZone)"，据此给出显式提示。
  const tzFallback = !!data?.timezone && data.timezone.includes('fallback from')

  return (
    <Panel
      title={t('playerTrend.title')}
      icon={<Users className="size-4" />}
      actions={data?.timezone ? <span className="text-[11px] text-muted-foreground">{t('playerTrend.timezone')}: {data.timezone}</span> : undefined}
    >
      {isError ? (
        <p className="py-5 text-center text-sm text-muted-foreground">{t('playerTrend.error')}</p>
      ) : isLoading ? (
        <p className="py-5 text-center text-sm text-muted-foreground">{t('playerTrend.loading')}</p>
      ) : !data || points.length === 0 ? (
        <p className="py-5 text-center text-sm text-muted-foreground">{t('playerTrend.empty')}</p>
      ) : (
        <div className="space-y-4">
          {tzFallback && (
            <p className="text-[11px] text-muted-foreground">{t('playerTrend.tzFallback')}</p>
          )}
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
            <StatCard
              label={t('playerTrend.peak')}
              value={data.peakValue.toFixed(0)}
              // 峰值时刻若不带标签，用户只看到一串裸时间（自审 M6）：用单条插值键作 sub。
              sub={peakAt ? t('playerTrend.peakAt', { at: peakAt }) : undefined}
            />
            <StatCard label={t('playerTrend.dailyAvg')} value={data.dailyAvg.toFixed(1)} />
            <StatCard label={t('playerTrend.granularity')} value={data.resolution} sub={range} />
          </div>
          <Sparkline points={points} className="h-10 w-full" />
          <div>
            <p className="mb-1.5 text-[11px] text-muted-foreground">{t('playerTrend.hourly')}</p>
            <HourlyBars dist={data.hourlyDist} peak={data.peakValue} />
            <MiniBar value={data.peakValue > 0 ? (data.dailyAvg / data.peakValue) * 100 : 0} level="info" />
          </div>
        </div>
      )}
    </Panel>
  )
}
