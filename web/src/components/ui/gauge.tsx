import { resourceLevel, statusColorVar, type StatusLevel } from '@/lib/threshold'

/** 环形资源仪表盘（FR-061）：按阈值变色，用于节点/总览的 CPU/内存/磁盘占用。 */
export function ResourceGauge({
  label,
  value,
  max = 100,
  unit = '%',
  level,
  size = 92,
  thickness = 8,
}: {
  label: string
  value: number
  max?: number
  unit?: string
  /** 显式等级；不传按 value/max 百分比走资源阈值。 */
  level?: StatusLevel
  size?: number
  thickness?: number
}) {
  const pct = max > 0 ? Math.min(100, Math.max(0, (value / max) * 100)) : 0
  const lvl = level ?? resourceLevel(pct)
  const r = (size - thickness) / 2
  const circ = 2 * Math.PI * r
  const offset = circ * (1 - pct / 100)
  const color = statusColorVar(lvl)
  return (
    <div className="flex flex-col items-center gap-1">
      <div className="relative" style={{ width: size, height: size }}>
        <svg width={size} height={size} className="-rotate-90">
          <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke="var(--muted)" strokeWidth={thickness} />
          <circle
            cx={size / 2}
            cy={size / 2}
            r={r}
            fill="none"
            stroke={color}
            strokeWidth={thickness}
            strokeDasharray={circ}
            strokeDashoffset={offset}
            strokeLinecap="round"
            style={{ transition: 'stroke-dashoffset 0.4s ease' }}
          />
        </svg>
        <div className="absolute inset-0 flex flex-col items-center justify-center">
          <span className="text-lg font-semibold tabular-nums" style={{ color }}>
            {Number.isFinite(value) ? Math.round(value) : '—'}
          </span>
          <span className="text-[11px] text-muted-foreground">{unit}</span>
        </div>
      </div>
      <span className="text-xs text-muted-foreground">{label}</span>
    </div>
  )
}
