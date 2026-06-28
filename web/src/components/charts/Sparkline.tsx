/* eslint-disable react-refresh/only-export-components -- 与组件同文件导出纯函数 sparkPath（可测），仅影响 Fast Refresh */
/**
 * 迷你趋势线（FR-221「关键指标概览」）：用纯 SVG 画一条无坐标轴的缩略折线，
 * 供「当前值 + 趋势缩略」一屏概览。不用 recharts/ResizeObserver——固定 viewBox + 百分比铺满，
 * 在 jsdom 下也可渲染与断言（无需实测宽度）。缺测（null）断开不连线。
 */

/** 一个缩略点；value 为 null 表示缺测（断点）。 */
export interface SparkPoint {
  value: number | null
}

/** 把序列点映射成 SVG path 的 d（缺测处断开成多段 M…L…）。viewBox 固定 100×28。 */
export function sparkPath(points: SparkPoint[]): string {
  const vals = points.map((p) => p.value)
  const nums = vals.filter((v): v is number => v != null && Number.isFinite(v))
  if (nums.length === 0) return ''
  const min = Math.min(...nums)
  const max = Math.max(...nums)
  const span = max - min || 1
  const w = 100
  const h = 28
  const pad = 2
  const n = points.length
  const x = (i: number) => (n <= 1 ? w / 2 : pad + (i / (n - 1)) * (w - pad * 2))
  // 值越大越靠上：y 反向映射，留 pad 边距。
  const y = (v: number) => pad + (1 - (v - min) / span) * (h - pad * 2)

  let d = ''
  let penDown = false
  for (let i = 0; i < n; i++) {
    const v = vals[i]
    if (v == null || !Number.isFinite(v)) {
      penDown = false
      continue
    }
    d += `${penDown ? 'L' : 'M'}${x(i).toFixed(1)} ${y(v).toFixed(1)} `
    penDown = true
  }
  return d.trim()
}

/**
 * 迷你趋势线。color 默认取主色变量；空数据渲染为一条淡基线，不报错。
 */
export function Sparkline({
  points,
  color = 'var(--chart-1)',
  className,
  ariaLabel,
}: {
  points: SparkPoint[]
  color?: string
  className?: string
  ariaLabel?: string
}) {
  const d = sparkPath(points)
  return (
    <svg
      viewBox="0 0 100 28"
      preserveAspectRatio="none"
      className={className}
      role="img"
      aria-label={ariaLabel}
      style={{ width: '100%', height: '100%' }}
    >
      {d ? (
        <path d={d} fill="none" stroke={color} strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
      ) : (
        <line x1="0" y1="14" x2="100" y2="14" stroke="var(--border)" strokeWidth={1} vectorEffect="non-scaling-stroke" />
      )}
    </svg>
  )
}
