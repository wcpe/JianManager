/* eslint-disable react-refresh/only-export-components -- 与组件同文件导出 MetricRange 类型/常量，仅影响 Fast Refresh */
import { useTranslation } from 'react-i18next'
import type { MetricResolution } from '@/api/metrics'
import { cn } from '@/lib/utils'

/** 时序查询区间（FR-060/FR-061），与后端 /metrics range 枚举一致。 */
export type MetricRange = '1h' | '6h' | '24h' | '7d' | '30d' | '90d'

export const METRIC_RANGES: MetricRange[] = ['1h', '6h', '24h', '7d', '30d', '90d']

/** 聚合粒度档位选项（FR-221）：auto + ADR-013 三档（raw 30s / 5m / 1h）。 */
export const METRIC_RESOLUTIONS: MetricResolution[] = ['auto', 'raw', '5m', '1h']

/** 时间区间选择器（FR-061）：供总览/监控/实例详情的历史曲线复用。 */
export function RangePicker({
  value,
  onChange,
  className,
}: {
  value: MetricRange
  onChange: (range: MetricRange) => void
  className?: string
}) {
  return (
    <div className={cn('inline-flex items-center rounded-md border p-0.5', className)} role="tablist">
      {METRIC_RANGES.map((r) => (
        <button
          key={r}
          type="button"
          role="tab"
          aria-selected={value === r}
          onClick={() => onChange(r)}
          className={cn(
            'rounded px-2 py-0.5 text-xs font-medium transition-colors',
            value === r ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground',
          )}
        >
          {r}
        </button>
      ))}
    </div>
  )
}

/**
 * 聚合粒度选择器（FR-221，ADR-013 三档降采样）：让用户在 auto/raw/5m/1h 间切换，
 * 与 RangePicker 配合实现「自定义聚合粒度 + 时间范围」。auto 即按区间自动选档（既有默认行为）。
 */
export function ResolutionPicker({
  value,
  onChange,
  className,
}: {
  value: MetricResolution
  onChange: (resolution: MetricResolution) => void
  className?: string
}) {
  const { t } = useTranslation()
  return (
    <div className={cn('inline-flex items-center rounded-md border p-0.5', className)} role="tablist" aria-label={t('monitor.resolution')}>
      {METRIC_RESOLUTIONS.map((r) => (
        <button
          key={r}
          type="button"
          role="tab"
          aria-selected={value === r}
          onClick={() => onChange(r)}
          className={cn(
            'rounded px-2 py-0.5 text-xs font-medium transition-colors',
            value === r ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground',
          )}
        >
          {t(`monitor.res.${r}`)}
        </button>
      ))}
    </div>
  )
}
