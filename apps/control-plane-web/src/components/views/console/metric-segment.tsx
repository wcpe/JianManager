import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'

/**
 * 服务器控制台顶栏的指标分段原语（方案 B 状态条）。
 * 从应用侧 `InstanceConsolePage` 拆出的纯展示件：无取数、无状态，仅按 props 渲染。
 */

/** 顶栏指标分段（方案 B 状态条）：label + 等宽值成一段，阈值越线才上色；dot 为状态圆点。 */
export function MetricSegment({ label, value, tone, dot }: { label: string; value: string; tone?: 'warn' | 'danger'; dot?: boolean }) {
  return (
    // title 补全精确值与口径：分段只显「12%」，悬停能看到「CPU 12%」上下文。
    <span className="inline-flex items-center gap-1.5" title={`${label} ${value}`}>
      {dot && (
        <span
          aria-hidden
          className={cn(
            'size-1.5 rounded-full',
            tone === 'warn' ? 'bg-status-warning' : tone === 'danger' ? 'bg-status-danger' : 'bg-status-success',
          )}
        />
      )}
      <span className="text-[11px] text-muted-foreground">{label}</span>
      <b className={cn(
        'font-mono text-xs font-semibold tabular-nums',
        tone === 'warn' && 'text-status-warning',
        tone === 'danger' && 'text-status-danger',
      )}>{value}</b>
    </span>
  )
}

/** 分段之间的细分隔线。 */
export function MetricDivider() {
  return <span aria-hidden className="h-3 w-px shrink-0 bg-border" />
}

/** 探针缺失聚合芯片（方案 B）：取代逐项「需探针」，悬停列出不可用项。对齐交由外层容器管理。 */
export function ProbeMissingChip() {
  const { t } = useTranslation()
  return (
    <span
      title={t('serverConsole.probeUnavailable')}
      className="inline-flex items-center gap-1.5 rounded-md bg-muted px-2 py-0.5 text-[11px] text-muted-foreground"
    >
      <span aria-hidden className="size-1.5 rounded-full bg-muted-foreground/60" />
      {t('serverConsole.probeChip')}
    </span>
  )
}
