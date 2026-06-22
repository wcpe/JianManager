import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useMetricSeries, type MetricSeries } from '@/api/metrics'
import { useProbeUpdateStatus, useUpdateProbe } from '@/api/probe'
import { Panel } from '@/components/ui/panel'
import { Button } from '@/components/ui/button'
import { TimeSeriesChart, type ChartSeries } from '@/components/charts/TimeSeriesChart'
import { RangePicker, type MetricRange } from '@/components/charts/RangePicker'

/**
 * 探针在线更新卡（FR-068）：展示探针连接状态 + 内嵌最新版本 + 上次推送时间，
 * 「更新探针」推送内嵌 jar（下次重启生效），「更新并重启」推送后立即重启实例生效。
 */
function ProbeUpdateCard({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: st } = useProbeUpdateStatus(instanceId)
  const update = useUpdateProbe(instanceId)
  if (!st) return null
  const doUpdate = (restart: boolean) =>
    update.mutate(restart, {
      onSuccess: (r) => toast.success(r.restarted ? t('probe.updatedRestarted') : t('probe.updatedPending')),
      onError: () => toast.error(t('probe.updateFailed')),
    })
  return (
    <Panel title={t('probe.title')}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 p-2 text-xs">
        <span className={st.probeConnected ? 'font-medium text-green-600 dark:text-green-400' : 'text-muted-foreground'}>
          {st.probeConnected ? t('probe.connected') : t('probe.disconnected')}
        </span>
        <span className="text-muted-foreground">
          {t('probe.embeddedVersion')}: {st.embeddedAvailable ? st.embeddedVersion || '—' : 'N/A'}
        </span>
        {st.lastPushedAt && (
          <span className="text-muted-foreground">
            {t('probe.lastPushed')}: {new Date(st.lastPushedAt).toLocaleString()}
          </span>
        )}
        <div className="ml-auto flex gap-2">
          <Button size="sm" variant="outline" disabled={!st.embeddedAvailable || update.isPending} onClick={() => doUpdate(false)}>
            {t('probe.update')}
          </Button>
          <Button size="sm" variant="outline" disabled={!st.embeddedAvailable || update.isPending} onClick={() => doUpdate(true)}>
            {t('probe.updateRestart')}
          </Button>
        </div>
      </div>
      {!st.embeddedAvailable && <div className="px-2 pb-2 text-xs text-muted-foreground">{t('probe.notEmbedded')}</div>}
    </Panel>
  )
}

/** 字节 → G/M/K。 */
function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  return `${(b / 1024).toFixed(0)}K`
}

/**
 * 实例监控段（FR-060/FR-061）：消费 /metrics/series（scope=instance）渲染历史曲线——
 * TPS/MSPT/堆/在线/线程/CPU + 分世界区块。探针不可用时段渲染为断点。
 */
export default function MetricsSegment({ instanceUuid, instanceId }: { instanceUuid: string; instanceId: number }) {
  const { t } = useTranslation()
  const [range, setRange] = useState<MetricRange>('24h')
  const { data, isLoading } = useMetricSeries({ scope: 'instance', targetId: instanceUuid, range })

  const series = data?.series ?? []
  const one = (metricKey: string, name: string): ChartSeries[] => {
    const s = series.find((x) => x.metricKey === metricKey && x.world === '')
    if (!s) return []
    return [{ key: metricKey, name, points: s.points.map((p) => ({ ts: p.ts, value: p.avg })) }]
  }
  // 多指标同图（如堆 used·max 叠加）
  const many = (...pairs: [string, string][]): ChartSeries[] => pairs.flatMap(([k, n]) => one(k, n))
  // 分世界：同一 metricKey 下每个 world 一条线
  const byWorld = (metricKey: string): ChartSeries[] =>
    series
      .filter((x: MetricSeries) => x.metricKey === metricKey && x.world !== '')
      .map((s) => ({ key: s.world, name: s.world, points: s.points.map((p) => ({ ts: p.ts, value: p.avg })) }))

  if (isLoading) {
    return <div className="p-4 text-sm text-muted-foreground">{t('common.loading')}</div>
  }

  return (
    <div className="space-y-3 p-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">{t('metrics.title')}</h3>
        <RangePicker value={range} onChange={setRange} />
      </div>
      <ProbeUpdateCard instanceId={instanceId} />
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2 xl:grid-cols-3">
        <Panel title={t('metrics.tps')}>
          <TimeSeriesChart series={one('inst_tps', t('metrics.tps'))} height={160} valueFormatter={(v) => v.toFixed(1)} />
        </Panel>
        <Panel title={t('metrics.mspt')}>
          <TimeSeriesChart series={one('inst_mspt', t('metrics.mspt'))} height={160} valueFormatter={(v) => `${v.toFixed(1)}ms`} />
        </Panel>
        <Panel title={t('metrics.heap')}>
          <TimeSeriesChart
            series={many(['inst_heap_used', t('metrics.heapUsed')], ['inst_heap_max', t('metrics.heapMax')])}
            height={160}
            valueFormatter={fmtBytes}
          />
        </Panel>
        <Panel title={t('metrics.players')}>
          <TimeSeriesChart series={one('inst_players_online', t('metrics.players'))} height={160} valueFormatter={(v) => v.toFixed(0)} />
        </Panel>
        <Panel title={t('metrics.threads')}>
          <TimeSeriesChart series={one('inst_threads', t('metrics.threads'))} height={160} valueFormatter={(v) => v.toFixed(0)} />
        </Panel>
        <Panel title={t('metrics.cpu')}>
          <TimeSeriesChart series={one('inst_cpu_pct', t('metrics.cpu'))} height={160} valueFormatter={(v) => `${v.toFixed(0)}%`} />
        </Panel>
        <Panel title={t('metrics.worldChunks')}>
          <TimeSeriesChart series={byWorld('world_loaded_chunks')} height={160} valueFormatter={(v) => v.toFixed(0)} />
        </Panel>
        <Panel title={t('metrics.worldEntities')}>
          <TimeSeriesChart series={byWorld('world_entities')} height={160} valueFormatter={(v) => v.toFixed(0)} />
        </Panel>
        <Panel title={t('metrics.worldTileEntities')}>
          <TimeSeriesChart series={byWorld('world_tile_entities')} height={160} valueFormatter={(v) => v.toFixed(0)} />
        </Panel>
      </div>
    </div>
  )
}
