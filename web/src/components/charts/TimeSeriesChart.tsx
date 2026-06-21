import { useCallback, useMemo, useRef, useState } from 'react'
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

/** 一条曲线序列（FR-060 消费 /metrics/series 的点；value=avg，null 为缺测断点）。 */
export interface ChartSeries {
  key: string
  name: string
  /** 线色，默认按序取 --chart-1..5。 */
  color?: string
  points: { ts: string; value: number | null }[]
}

const CHART_COLORS = [
  'var(--chart-1)',
  'var(--chart-2)',
  'var(--chart-3)',
  'var(--chart-4)',
  'var(--chart-5)',
]

/** 据跨度选 X 轴时间格式：≤24h 显示时:分，更长显示月-日。 */
function makeTickFormatter(spanMs: number): (ts: string) => string {
  const dayMs = 24 * 60 * 60 * 1000
  return (ts: string) => {
    const d = new Date(ts)
    if (spanMs <= dayMs) {
      return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
    }
    return `${d.getMonth() + 1}-${d.getDate()}`
  }
}

/**
 * 历史曲线（FR-061）：多序列折线，按时间戳对齐合并，null 渲染为断点（不连线）。
 * 各页传入已映射好的 ChartSeries；纵轴单位与数值格式由调用方经 valueFormatter 控制。
 */
export function TimeSeriesChart({
  series,
  height = 220,
  valueFormatter,
  emptyHint = '暂无数据',
}: {
  series: ChartSeries[]
  height?: number
  valueFormatter?: (v: number) => string
  emptyHint?: string
}) {
  const { data, spanMs } = useMemo(() => {
    const byTs = new Map<string, Record<string, number | null | string>>()
    for (const s of series) {
      for (const p of s.points) {
        const row = byTs.get(p.ts) ?? { ts: p.ts }
        row[s.key] = p.value
        byTs.set(p.ts, row)
      }
    }
    const rows = [...byTs.values()].sort((a, b) => (String(a.ts) < String(b.ts) ? -1 : 1))
    const span =
      rows.length > 1 ? new Date(String(rows[rows.length - 1].ts)).getTime() - new Date(String(rows[0].ts)).getTime() : 0
    return { data: rows, spanMs: span }
  }, [series])

  // 仅当容器具备非 0 宽度时才挂载 ResponsiveContainer：在隐藏/未激活分段或折叠面板
  // （0 尺寸容器）内渲染时，recharts 会以 width(-1)/height(-1) 告警（BUG-007）。
  // 用 callback ref 在容器挂载时建 ResizeObserver；容器获得尺寸（切回分段/展开面板）后
  // 再次触发，自动恢复，不留持续空白。容器跨「无数据/有图」分支重挂载也能正确重接。
  const [hasWidth, setHasWidth] = useState(false)
  const observerRef = useRef<ResizeObserver | null>(null)
  const containerRef = useCallback((el: HTMLDivElement | null) => {
    observerRef.current?.disconnect()
    if (!el) {
      observerRef.current = null
      return
    }
    const observer = new ResizeObserver((entries) => {
      setHasWidth((entries[0]?.contentRect.width ?? 0) > 0)
    })
    observer.observe(el)
    observerRef.current = observer
  }, [])

  const hasData = data.some((row) => series.some((s) => row[s.key] != null))
  if (!hasData) {
    return (
      <div
        className="flex items-center justify-center text-xs text-muted-foreground"
        style={{ height }}
      >
        {emptyHint}
      </div>
    )
  }

  const fmt = valueFormatter ?? ((v: number) => String(v))
  const tickFormatter = makeTickFormatter(spanMs)

  return (
    <div ref={containerRef} style={{ height, width: '100%' }}>
      {hasWidth && (
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data} margin={{ top: 6, right: 12, bottom: 0, left: 4 }}>
          <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" vertical={false} />
          <XAxis
            dataKey="ts"
            tickFormatter={tickFormatter}
            tick={{ fontSize: 11, fill: 'var(--muted-foreground)' }}
            stroke="var(--border)"
            minTickGap={32}
          />
          <YAxis
            tickFormatter={(v: number) => fmt(v)}
            tick={{ fontSize: 11, fill: 'var(--muted-foreground)' }}
            stroke="var(--border)"
            width={48}
          />
          <Tooltip
            contentStyle={{
              background: 'var(--popover)',
              border: '1px solid var(--border)',
              borderRadius: 8,
              fontSize: 12,
              color: 'var(--popover-foreground)',
            }}
            labelFormatter={(ts) => new Date(String(ts)).toLocaleString()}
            formatter={(value, name) => {
              const num = typeof value === 'number' ? value : Number(value)
              return [Number.isFinite(num) ? fmt(num) : '—', name]
            }}
          />
          {series.map((s, i) => (
            <Line
              key={s.key}
              type="monotone"
              dataKey={s.key}
              name={s.name}
              stroke={s.color ?? CHART_COLORS[i % CHART_COLORS.length]}
              strokeWidth={1.5}
              dot={false}
              connectNulls={false}
              isAnimationActive={false}
            />
          ))}
        </LineChart>
      </ResponsiveContainer>
      )}
    </div>
  )
}
