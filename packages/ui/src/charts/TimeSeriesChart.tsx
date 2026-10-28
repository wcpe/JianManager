import { startTransition, useCallback, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ReferenceLine,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import { chartAccessibleName } from '../lib/chart-a11y'

/** 一条曲线序列（FR-060 消费 /metrics/series 的点；value=avg，null 为缺测断点）。 */
export interface ChartSeries {
  key: string
  name: string
  /** 线色，默认按序取 --chart-1..5。 */
  color?: string
  /**
   * 初始是否隐藏该线。
   *
   * recharts 的 `hide` 语义：线不绘制，但**仍留图例**（点一下即显示），
   * 且其数据仍参与纵轴域计算。用于「默认收起、需要时可看」的次要序列，
   * 例如与主指标不同量级的共享进程占用。
   */
  hidden?: boolean
  points: { ts: string; value: number | null }[]
}

export interface ChartReferenceLine {
  value: number
  label: string
  color?: string
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
  emptyHint,
  yDomain = ['auto', 'auto'],
  referenceLines = [],
  ariaLabel,
  width: fixedWidth,
  onHoverTs,
  tooltipExtra,
}: {
  series: ChartSeries[]
  height?: number
  /**
   * 已知确切像素宽时传入，**跳过 ResizeObserver 测宽**。
   *
   * 【为什么需要】自测宽路径有两处代价：首帧宽度为 0 不渲染图，且实测宽度要经 2000ms 防抖
   * （为侧栏过渡动画做的取舍）才落进 state。对「悬停即出现、且宽度本就知道」的浮层
   * （如实例页的健康度悬浮面板，宽度固定 520/2 列），这两点表现为「面板出来了但图空白 2 秒」。
   * 传本参数即直接用给定宽度渲染，无延迟、无空白帧。
   */
  width?: number
  /**
   * 悬浮时刻回调：进入时给该点的时间戳，离开时给 null。
   *
   * 【为什么需要】图表上只有聚合值（如「CPU 32%」），而运维常问「那一刻是谁在吃 CPU」。
   * 调用方据此去查该时刻的归属明细，再通过 `tooltipExtra` 塞回悬浮框。
   */
  onHoverTs?: (ts: string | null) => void
  /**
   * 悬浮框的附加内容（渲染在指标值下方）。参数是当前悬浮点的时间戳。
   * 省略时悬浮框与改造前完全一致。
   */
  tooltipExtra?: (ts: string) => React.ReactNode
  /** 纵轴与 tooltip 的数值格式。第二参数是序列名，便于调用方按序列还原量纲
   *  （如多序列归一化图：图上画百分比、tooltip 显示原始值）。 */
  valueFormatter?: (v: number, seriesName?: string) => string
  emptyHint?: string
  /** Y 轴范围。默认 ['auto','auto'] 自适应数据，使 TPS/MSPT 等窄幅指标不被 0 基线压成直线（FR-148）；占比类指标可传 [0, 100]。 */
  yDomain?: [number | 'auto' | 'dataMin' | 'dataMax', number | 'auto' | 'dataMin' | 'dataMax']
  /** 可选阈值线；仅调用方明确传入时渲染，避免影响其它图表。 */
  referenceLines?: ChartReferenceLine[]
  /**
   * 图表的替代文本（可访问名）。省略时由序列名推导——见 {@link chartAccessibleName}。
   * 调用方若有更贴切的描述（如「近 24 小时 TPS 趋势」），传进来即可覆盖推导值。
   */
  ariaLabel?: string
}) {
  const { t } = useTranslation()
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
    // rows.length > 1 时首末两行必存在；判空仅为类型收窄
    const lastRow = rows[rows.length - 1]
    const firstRow = rows[0]
    const span =
      rows.length > 1 && lastRow !== undefined && firstRow !== undefined
        ? new Date(String(lastRow.ts)).getTime() - new Date(String(firstRow.ts)).getTime()
        : 0
    return { data: rows, spanMs: span }
  }, [series])

  // 自测容器宽度并把实测宽度直接喂给 LineChart，不用 ResponsiveContainer：后者在隐藏/
  // 未激活分段或折叠面板（0 尺寸容器）内、以及自身首帧测量完成前都会以 width(-1)/
  // height(-1) 告警（BUG-007）。用 callback ref 建 ResizeObserver 实时测宽，宽度为 0 时
  // 不渲染图（切回分段/展开面板后自动恢复），>0 时以确切像素宽渲染，彻底无 -1 告警。
  const [width, setWidth] = useState(0)
  const observerRef = useRef<ResizeObserver | null>(null)
  const debounceRef = useRef<number | undefined>(undefined)
  const containerRef = useCallback((el: HTMLDivElement | null) => {
    observerRef.current?.disconnect()
    window.clearTimeout(debounceRef.current)
    if (!el) {
      observerRef.current = null
      return
    }
    // 同 MonitorChart：宽度逐帧变化时若立即重绘，recharts 会占满整个侧栏过渡期。
    // 防抖 + startTransition 让过渡期间不重绘、结束后的那一次也不阻塞动画帧。
    /*
      首测立即生效，之后的变更才防抖。
      之前统一防抖 2000ms：图表挂载后要空白整整两秒才出现——用户会以为图画不出来。
      而防抖的本意只是「侧栏拖动/折叠过渡期间别逐帧重绘」，首次测量并不需要它。
    */
    let measured = false
    const observer = new ResizeObserver((entries) => {
      const next = Math.floor(entries[0]?.contentRect.width ?? 0)
      if (!measured) {
        measured = true
        setWidth(next)
        return
      }
      window.clearTimeout(debounceRef.current)
      debounceRef.current = window.setTimeout(() => {
        startTransition(() => setWidth(next))
      }, 2000)
    })
    observer.observe(el)
    observerRef.current = observer
  }, [])

  /** 外显宽度优先：调用方已知宽度时不再自测（见 `width` prop 说明）。 */
  const effectiveWidth = fixedWidth && fixedWidth > 0 ? fixedWidth : width

  const hasData = data.some((row) => series.some((s) => row[s.key] != null))
  if (!hasData) {
    return (
      <div
        className="flex items-center justify-center text-xs text-muted-foreground"
        style={{ height }}
      >
        {emptyHint ?? t('common.noData')}
      </div>
    )
  }

  const fmt = valueFormatter ?? ((v: number) => String(v))
  const tickFormatter = makeTickFormatter(spanMs)

  return (
    <div className="space-y-1">
      {/* role="img" + aria-label：recharts 画出来的是裸 SVG（一堆 <path>/<text>），读屏既读不出
          趋势也读不出「这是什么图」，等于对读屏用户完全不可见。整块标成图像并给替代文本，
          是把图形内容传达给非视觉用户的标准做法（与 Sparkline 同一约定）。
          用 role="img" 还顺带屏蔽了内部坐标轴/图例的碎文本噪声——它们已在替代文本里概括。 */}
      <div
        ref={containerRef}
        role="img"
        aria-label={chartAccessibleName(series, ariaLabel, emptyHint)}
        style={{ height, width: '100%' }}
      >
        {effectiveWidth > 0 && (
          <LineChart
            width={effectiveWidth}
            height={height}
            data={data}
            margin={{ top: 6, right: 12, bottom: 0, left: 4 }}
            {...(onHoverTs
              ? {
                  onMouseMove: (state: { activeLabel?: string | number }) => {
                    // recharts 把当前激活的 x 值放在 activeLabel；离开时给 null。
                    onHoverTs(state.activeLabel != null ? String(state.activeLabel) : null)
                  },
                  onMouseLeave: () => onHoverTs(null),
                }
              : {})}
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
                return [Number.isFinite(num) ? fmt(num, String(name)) : '—', name]
              }}
              /*
                用 `content` 包一层而不是替换：recharts 的默认渲染（label + 各序列值）保持不变，
                只在下方追加调用方给的归属明细。这样没传 `tooltipExtra` 的图表行为逐字不变。
              */
              content={
                tooltipExtra
                  ? ({ active, payload, label }) => {
                      if (!active || !payload || payload.length === 0) return null
                      return (
                        <div
                          style={{
                            background: 'var(--popover)',
                            border: '1px solid var(--border)',
                            borderRadius: 8,
                            fontSize: 12,
                            color: 'var(--popover-foreground)',
                            padding: '6px 8px',
                            maxWidth: 320,
                          }}
                        >
                          <div style={{ marginBottom: 4, opacity: 0.75 }}>
                            {new Date(String(label)).toLocaleString()}
                          </div>
                          {payload.map((entry) => {
                            const num = typeof entry.value === 'number' ? entry.value : Number(entry.value)
                            return (
                              <div key={String(entry.name)} style={{ display: 'flex', gap: 8, justifyContent: 'space-between' }}>
                                <span>{String(entry.name)}</span>
                                <span style={{ fontVariantNumeric: 'tabular-nums' }}>
                                  {Number.isFinite(num) ? fmt(num, String(entry.name)) : '—'}
                                </span>
                              </div>
                            )
                          })}
                          <div style={{ marginTop: 6 }}>{tooltipExtra(String(label))}</div>
                        </div>
                      )
                    }
                  : undefined
              }
            />
            {series.length > 1 && (
              <Legend wrapperStyle={{ fontSize: 11 }} iconType="plainline" iconSize={12} />
            )}
            {referenceLines.map((line) => (
              <ReferenceLine
                key={`${line.label}-${line.value}`}
                y={line.value}
                stroke={line.color ?? 'var(--muted-foreground)'}
                strokeDasharray="4 4"
                ifOverflow="extendDomain"
                label={{
                  value: line.label,
                  position: 'insideTopRight',
                  fill: line.color ?? 'var(--muted-foreground)',
                  fontSize: 11,
                }}
              />
            ))}
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
                hide={s.hidden}
              />
            ))}
          </LineChart>
        )}
      </div>
      {referenceLines.length > 0 && (
        <div className="flex flex-wrap gap-x-3 gap-y-1 px-1 text-[10px] text-muted-foreground">
          {referenceLines.map((line) => (
            <span key={`${line.label}-${line.value}`} className="inline-flex items-center gap-1">
              <span
                className="h-px w-4 border-t border-dashed"
                style={{ borderColor: line.color ?? 'var(--muted-foreground)' }}
              />
              {line.label}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}
