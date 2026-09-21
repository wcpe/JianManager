import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import { activeMetricSources, metricSourceLabelKey, type AvailabilityBits } from '@/lib/metrics-availability'

/**
 * 数据来源标注（FR-447）：按 `探针 → SLP → Query` 优先级列出本拍命中来源。
 *
 * 三源皆无时不渲染（由调用方显式渲染「不可用」）。`title` 悬停展示完整清单，
 * `data-metric-sources` 供测试断言（如 `probe,slp`）。
 */
export function MetricSourceChips({ metrics, className }: { metrics: AvailabilityBits; className?: string }) {
  const { t } = useTranslation()
  const sources = activeMetricSources(metrics)
  if (sources.length === 0) return null
  const labels = sources.map((source) => t(metricSourceLabelKey(source)))
  return (
    <span
      className={cn('inline-flex items-center gap-1 rounded-full border bg-muted/60 px-2 py-0.5 text-[10px] text-muted-foreground', className)}
      title={`${t('metrics.source.label')}: ${labels.join(' · ')}`}
      data-metric-sources={sources.join(',')}
    >
      <span aria-hidden className="size-1.5 rounded-full bg-status-success/70" />
      {labels.join(' · ')}
    </span>
  )
}
