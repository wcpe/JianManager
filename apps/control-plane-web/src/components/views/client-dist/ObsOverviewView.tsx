import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { TimeSeriesChart, type ChartSeries } from '@jianmanager/ui'
import type { ClientDistObservability } from '@jianmanager/ui/lib/client-dist-stats-contracts'
import { InsightCards } from './InsightCards'
import { UpdateHeatmap } from './UpdateHeatmap'

/**
 * 分发观测总览区块的展示层（FR-426/427/428）。
 * 取数由应用侧容器负责（`useClientDistObservability`）；机器更新排行面板同为取数件，经 `machinePanel` slot 注入。
 */
export function ObsOverviewView({
  data,
  isLoading,
  isError,
  machinePanel,
}: {
  data?: ClientDistObservability
  isLoading: boolean
  isError: boolean
  machinePanel?: ReactNode
}) {
  const { t } = useTranslation()

  const series = data?.series ?? []
  const pullsSeries: ChartSeries[] = [
    {
      key: 'manifest',
      name: t('clientDistObs.trendManifest', 'Manifest 拉取'),
      points: series.map((p) => ({ ts: p.ts, value: p.manifestPulls })),
    },
    {
      key: 'artifact',
      name: t('clientDistObs.trendArtifact', '制品拉取'),
      points: series.map((p) => ({ ts: p.ts, value: p.artifactPulls })),
    },
  ]
  const bytesSeries: ChartSeries[] = [
    {
      key: 'bytes',
      name: t('clientDistObs.kpiDownloads', '下载字节'),
      points: series.map((p) => ({ ts: p.ts, value: p.downloadBytes })),
    },
  ]

  if (isLoading) {
    return (
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5" data-testid="obs-overview-loading">
        {Array.from({ length: 5 }, (_, i) => (
          <div key={i} className="h-24 animate-pulse rounded-lg bg-muted" />
        ))}
      </div>
    )
  }
  if (isError || !data) {
    return (
      <div className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground" data-testid="obs-overview-error">
        {t('clientDistObs.overviewUnavailable', '观测数据暂不可用')}
      </div>
    )
  }

  return (
    <div className="space-y-4" data-testid="obs-overview">
      <InsightCards summary={data.summary} compare={data.compare} />

      {/* 请求侧趋势（FR-425/427：跟随统一时间窗） */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientDistObs.trendPullsTitle', '拉取趋势')}</h3>
        <div className="rounded-lg border p-3">
          <TimeSeriesChart
            series={pullsSeries}
            valueFormatter={(v) => String(Math.round(v))}
            emptyHint={t('clientDistObs.heatmapEmpty', '所选时间段内没有更新活动')}
          />
        </div>
      </section>
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientDistObs.trendBytesTitle', '下载字节趋势')}</h3>
        <div className="rounded-lg border p-3">
          <TimeSeriesChart series={bytesSeries} emptyHint={t('clientDistObs.heatmapEmpty', '所选时间段内没有更新活动')} />
        </div>
      </section>

      <UpdateHeatmap series={series} />
      {machinePanel}
    </div>
  )
}
