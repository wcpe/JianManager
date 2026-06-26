import { useCallback, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Brush, CartesianGrid, Legend, Line, LineChart, XAxis, YAxis } from 'recharts'
import type { PlotSeries } from '@/lib/monitor-metrics'
import { brushSelectionToWindow } from '@/lib/brush'
import { hoverSnapshotAt, type SampleRow } from '@/lib/chart-hover'

const CHART_COLORS = ['var(--chart-1)', 'var(--chart-2)', 'var(--chart-3)', 'var(--chart-4)', 'var(--chart-5)']

/** 据跨度选 X 轴时间格式：≤24h 显示时:分，更长显示月-日（与 TimeSeriesChart 一致）。 */
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
 * 监控图（FR-169）：在折线基础上加 **底部 brush 拖拽轴**（拖选时间窗，recharts Brush 原生缩放主图）
 * 与 **hover 浮窗**（该时刻各序列值）。每图独立时间窗为本地视图状态，不回写父级 range。
 *
 * 设计参见 design §4.2。纯逻辑（选区→窗、x→最近点）下沉 lib/brush.ts、lib/chart-hover.ts，
 * 已 vitest 覆盖；本组件只做装配与渲染。
 */
export function MonitorChart({
  series,
  height = 200,
  valueFormatter,
  emptyHint,
  yDomain = ['auto', 'auto'],
  showBrush = true,
}: {
  series: PlotSeries[]
  height?: number
  valueFormatter?: (v: number) => string
  emptyHint?: string
  yDomain?: [number | 'auto' | 'dataMin' | 'dataMax', number | 'auto' | 'dataMin' | 'dataMax']
  /** 数据点过少时可关闭 brush（拖拽无意义）。 */
  showBrush?: boolean
}) {
  const { t } = useTranslation()

  // 按时间戳对齐合并所有序列为行集合（recharts data）。始终喂全量，由 Brush 原生缩放主图。
  const rows = useMemo<SampleRow[]>(() => {
    const byTs = new Map<string, SampleRow>()
    for (const s of series) {
      for (const p of s.points) {
        const row = byTs.get(p.ts) ?? ({ ts: p.ts } as SampleRow)
        row[s.key] = p.value
        byTs.set(p.ts, row)
      }
    }
    return [...byTs.values()].sort((a, b) => (String(a.ts) < String(b.ts) ? -1 : 1))
  }, [series])

  const timestamps = useMemo(() => rows.map((r) => String(r.ts)), [rows])
  const fullSpanMs = useMemo(() => {
    if (rows.length < 2) return 0
    return new Date(String(rows[rows.length - 1].ts)).getTime() - new Date(String(rows[0].ts)).getTime()
  }, [rows])

  // brush 选区下标（受控）：默认全段。数据行数随轮询变化时，渲染期把下标夹到当前边界，
  // 既避免越界又不必用 effect 重置（实时前移时保留用户已选窗口的相对位置）。
  const [brushIdx, setBrushIdx] = useState<{ start: number; end: number } | null>(null)
  const lastIdx = Math.max(rows.length - 1, 0)
  const startIndex = Math.min(Math.max(brushIdx?.start ?? 0, 0), lastIdx)
  const endIndex = Math.min(Math.max(brushIdx?.end ?? lastIdx, 0), lastIdx)

  const onBrushChange = useCallback((range: { startIndex?: number; endIndex?: number }) => {
    if (range.startIndex == null || range.endIndex == null) return
    setBrushIdx({ start: range.startIndex, end: range.endIndex })
  }, [])

  // 当前 brush 选中的时间窗（供浮窗显示范围 / 调用方扩展用），经纯函数夹取。
  const selectedWindow = useMemo(
    () => brushSelectionToWindow(timestamps, startIndex, endIndex),
    [timestamps, startIndex, endIndex],
  )
  const visibleSpanMs = useMemo(() => {
    if (!selectedWindow) return fullSpanMs
    return new Date(selectedWindow.to).getTime() - new Date(selectedWindow.from).getTime()
  }, [selectedWindow, fullSpanMs])

  // hover：记录激活时间戳，经纯函数取最近行的各序列值（在全量 rows 上 lookup）。
  const [hoverTs, setHoverTs] = useState<string | null>(null)
  const snapshot = useMemo(
    () => (hoverTs ? hoverSnapshotAt(rows, series, hoverTs) : null),
    [hoverTs, rows, series],
  )

  // 容器实测宽度直喂 LineChart，规避 ResponsiveContainer 在 0 尺寸容器内 width(-1) 告警（BUG-007）。
  const [width, setWidth] = useState(0)
  const observerRef = useRef<ResizeObserver | null>(null)
  const containerRef = useCallback((el: HTMLDivElement | null) => {
    observerRef.current?.disconnect()
    if (!el) {
      observerRef.current = null
      return
    }
    const observer = new ResizeObserver((entries) => {
      setWidth(Math.floor(entries[0]?.contentRect.width ?? 0))
    })
    observer.observe(el)
    observerRef.current = observer
  }, [])

  const hasData = rows.some((row) => series.some((s) => row[s.key] != null))
  if (!hasData) {
    return (
      <div className="flex items-center justify-center text-xs text-muted-foreground" style={{ height }}>
        {emptyHint ?? t('common.noData')}
      </div>
    )
  }

  const fmt = valueFormatter ?? ((v: number) => String(v))
  const tickFormatter = makeTickFormatter(visibleSpanMs)
  const fullTickFormatter = makeTickFormatter(fullSpanMs)
  const canBrush = showBrush && rows.length > 4

  return (
    <div>
      {/* hover 浮窗：固定高度占位，避免悬浮时图表抖动 */}
      <div className="mb-1 flex min-h-[20px] flex-wrap items-center gap-x-3 gap-y-0.5 text-[11px]">
        {snapshot ? (
          <>
            <span className="text-muted-foreground">{new Date(snapshot.ts).toLocaleString()}</span>
            {snapshot.entries.map((e, i) => (
              <span key={e.key} className="inline-flex items-center gap-1">
                <span
                  className="inline-block size-2 rounded-full"
                  style={{ background: series[i]?.color ?? CHART_COLORS[i % CHART_COLORS.length] }}
                />
                <span className="text-muted-foreground">{e.name}</span>
                <span className="font-medium tabular-nums text-foreground">
                  {e.value != null ? fmt(e.value) : '—'}
                </span>
              </span>
            ))}
          </>
        ) : (
          <span className="text-muted-foreground/60">{t('monitor.hoverHint')}</span>
        )}
      </div>

      <div ref={containerRef} style={{ height, width: '100%' }}>
        {width > 0 && (
          <LineChart
            width={width}
            height={height}
            data={rows}
            margin={{ top: 6, right: 12, bottom: 0, left: 4 }}
            onMouseMove={(s) => {
              const label = (s as { activeLabel?: string | number }).activeLabel
              setHoverTs(label != null ? String(label) : null)
            }}
            onMouseLeave={() => setHoverTs(null)}
          >
            <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" vertical={false} />
            <XAxis
              dataKey="ts"
              tickFormatter={tickFormatter}
              tick={{ fontSize: 11, fill: 'var(--muted-foreground)' }}
              stroke="var(--border)"
              minTickGap={32}
            />
            <YAxis
              domain={yDomain}
              tickFormatter={(v: number) => fmt(v)}
              tick={{ fontSize: 11, fill: 'var(--muted-foreground)' }}
              stroke="var(--border)"
              width={48}
            />
            {series.length > 1 && <Legend wrapperStyle={{ fontSize: 11 }} iconType="plainline" iconSize={12} />}
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
            {canBrush && (
              <Brush
                dataKey="ts"
                height={22}
                travellerWidth={8}
                gap={2}
                stroke="var(--primary)"
                fill="var(--muted)"
                startIndex={startIndex}
                endIndex={endIndex}
                tickFormatter={fullTickFormatter}
                onChange={onBrushChange}
              />
            )}
          </LineChart>
        )}
      </div>
    </div>
  )
}
