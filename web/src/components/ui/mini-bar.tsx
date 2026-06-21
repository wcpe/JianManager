import { cn } from '@/lib/utils'
import { resourceLevel, statusColorVar, type StatusLevel } from '@/lib/threshold'

/** 迷你资源条（FR-061）：value/max → 阈值变色细条，用于密集表格内嵌资源占用。 */
export function MiniBar({
  value,
  max = 100,
  level,
  className,
}: {
  value: number
  max?: number
  /** 显式指定等级；不传按 value/max 百分比走资源阈值。 */
  level?: StatusLevel
  className?: string
}) {
  const pct = max > 0 ? Math.min(100, Math.max(0, (value / max) * 100)) : 0
  const lvl = level ?? resourceLevel(pct)
  return (
    <div className={cn('h-1.5 w-full overflow-hidden rounded-full bg-muted', className)}>
      <div
        className="h-full rounded-full transition-[width]"
        style={{ width: `${pct}%`, backgroundColor: statusColorVar(lvl) }}
      />
    </div>
  )
}
