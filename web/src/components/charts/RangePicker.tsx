import { cn } from '@/lib/utils'

/** 时序查询区间（FR-060/FR-061），与后端 /metrics range 枚举一致。 */
export type MetricRange = '1h' | '6h' | '24h' | '7d' | '30d' | '90d'

export const METRIC_RANGES: MetricRange[] = ['1h', '6h', '24h', '7d', '30d', '90d']

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
