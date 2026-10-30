import { resourceLevel, statusColorVar, type StatusLevel } from '../lib/threshold'

/** 环形资源仪表盘：按阈值变色，用于节点/总览的 CPU/内存/磁盘占用。 */
export function ResourceGauge({
  label,
  value,
  max = 100,
  unit = '%',
  level,
  size = 92,
  thickness = 8,
  decimals = 0,
}: {
  /** 环下方的说明；省略则只渲染环，供「环 + 卡内标签」的紧凑布局复用。 */
  label?: string
  value: number
  max?: number
  unit?: string
  /** 显式等级；不传按 value/max 百分比走资源阈值。 */
  level?: StatusLevel
  size?: number
  thickness?: number
  /** 中心数值保留的小数位（默认 0；如负载倍数用 2）。 */
  decimals?: number
}) {
  // 缺数（NaN）时环按 0% 画：中心仍显示「—」，但不能把 NaN 写进 SVG 属性。
  const safeValue = Number.isFinite(value) ? value : 0
  const pct = max > 0 ? Math.min(100, Math.max(0, (safeValue / max) * 100)) : 0
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
          <span
            className={`font-semibold tabular-nums ${size < 56 ? 'text-sm' : 'text-lg'}`}
            style={{ color }}
          >
            {Number.isFinite(value) ? value.toFixed(decimals) : '—'}
          </span>
          {unit && <span className="text-[11px] text-muted-foreground">{unit}</span>}
        </div>
      </div>
      {label && <span className="text-xs text-muted-foreground">{label}</span>}
    </div>
  )
}
