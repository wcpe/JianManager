import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import type { MetricResolution } from '@/api/metrics'
import { Panel } from '@/components/ui/panel'
import { RangePicker, ResolutionPicker, type MetricRange } from '@/components/charts/RangePicker'
import { MonitorSkeleton, type MonitorSource } from '@/components/charts/MonitorSkeleton'
import { MetricsOverviewStrip } from '@/components/charts/MetricsOverviewStrip'
import { MetricComparePanel } from '@/components/charts/MetricComparePanel'
import { DrillTargetPicker, targetKey, type DrillTarget } from '@/components/charts/DrillTargetPicker'
import { useTargetSeries } from '@/components/charts/use-target-series'
import {
  NODE_CHART_DEFS,
  INSTANCE_CHART_DEFS,
  PLATFORM_CHART_DEFS,
  type MetricChartDef,
} from '@/lib/monitor-metrics'

/** 据 target 选用的图定义集（平台 4 / 节点 6 / 实例 6）。 */
function defsFor(kind: DrillTarget['kind']): MetricChartDef[] {
  if (kind === 'node') return NODE_CHART_DEFS
  if (kind === 'instance') return INSTANCE_CHART_DEFS
  return PLATFORM_CHART_DEFS
}

/**
 * 统一监控页（FR-169 + FR-221 时序剖析增强）。在既有 FR-060/061 时序底座上加四个剖析维度：
 * 1. 关键指标概览——一屏看当前值 + sparkline 趋势缩略；
 * 2. 自定义聚合粒度 + 时间范围——页级 RangePicker + ResolutionPicker（auto/30s/5m/1h，ADR-013 三档）；
 * 3. 多指标对比/叠加——勾选多条指标在同一图叠加对比；
 * 4. 下钻——平台→节点→实例→世界 逐级钻取（面包屑 + 各层下拉）。
 * 仅纯前端消费既有 /metrics/series、/metrics/overview，不改后端、不另立 ADR（沿用 ADR-013）。
 */
export default function MonitoringPage() {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()
  const { data: instances } = useInstances()
  const [target, setTarget] = useState<DrillTarget>({ kind: 'platform' })
  // 页级范围 + 粒度（驱动概览/对比；主图网格各图另有独立范围，但共享该页级粒度）。
  const [range, setRange] = useState<MetricRange>('24h')
  const [resolution, setResolution] = useState<MetricResolution>('auto')
  // 多指标对比的选中集合（随 target 切换重置，见 key）。
  const [compareSel, setCompareSel] = useState<string[]>([])

  const tKey = targetKey(target)

  // 概览/对比/主图共享的数据源描述（MonitorSource 与 SeriesTarget 同构）。
  const source: MonitorSource =
    target.kind === 'platform' ? { kind: 'platform' } : { kind: target.kind, uuid: target.uuid }
  // 概览/对比共享的原始序列（页级范围 + 粒度）。
  const { series: raw, isLoading } = useTargetSeries(source, range, resolution)

  // 当前实例的世界名列表（来自其分世界序列），供下钻到世界的下拉。
  const worlds = useMemo(() => {
    if (target.kind !== 'instance') return []
    const set = new Set<string>()
    for (const s of raw) if (s.world) set.add(s.world)
    return [...set].sort()
  }, [raw, target.kind])

  const worldFilter = target.kind === 'instance' ? target.world : undefined

  const toggleCompare = (metricKey: string) =>
    setCompareSel((prev) =>
      prev.includes(metricKey) ? prev.filter((k) => k !== metricKey) : [...prev, metricKey],
    )

  // target 切换时重置对比选择（不同 target 指标目录不同）。
  const onChangeTarget = (next: DrillTarget) => {
    if (next.kind !== target.kind) setCompareSel([])
    setTarget(next)
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-xl font-bold">{t('monitor.title')}</h1>
        <div className="flex flex-wrap items-center gap-2">
          <ResolutionPicker value={resolution} onChange={setResolution} />
          <RangePicker value={range} onChange={setRange} />
        </div>
      </div>

      {/* 下钻：平台 → 节点 → 实例 → 世界 */}
      <Panel bodyClassName="px-3 py-2">
        <DrillTargetPicker
          target={target}
          onChange={onChangeTarget}
          nodes={nodes ?? []}
          instances={instances ?? []}
          worlds={worlds}
        />
      </Panel>

      {/* 关键指标概览：当前值 + sparkline 趋势缩略 */}
      <Panel title={t('monitor.overview.title')}>
        <MetricsOverviewStrip kind={target.kind} raw={raw} isLoading={isLoading} />
      </Panel>

      {/* 多指标对比/叠加 */}
      <Panel title={t('monitor.compare.title')}>
        <MetricComparePanel kind={target.kind} raw={raw} selected={compareSel} onToggle={toggleCompare} />
      </Panel>

      <Panel bodyClassName="px-3 py-2 text-[11px] text-muted-foreground">{t('monitor.hint')}</Panel>

      {/* 主图网格（每图独立时间筛选 + brush + hover），共享页级粒度与下钻世界聚焦 */}
      <MonitorSkeleton
        key={tKey}
        defs={defsFor(target.kind)}
        source={source}
        defaultRange={range}
        resolution={resolution}
        worldFilter={worldFilter}
      />
    </div>
  )
}
