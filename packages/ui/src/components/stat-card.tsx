import * as React from 'react'

import { cn } from '../lib/utils'
import { MiniBar } from './mini-bar'
import { statusTextClass, type StatusLevel } from '../lib/threshold'
import { toneChipClass, type Tone } from '../lib/tone'
import { deltaTone } from '../lib/stat-card'
import { cardSurface, cardSurfaceTone } from '../lib/interaction-overlay'

/** 
 * KPI 统计卡：图标块 + 标签 + 大数值 + 「按指标混搭」右侧视觉。
 * 占比型传 `bar`、计数型传 `delta`/`sub`、走势型传 `trend`——映射规则见 `lib/stat-card.pickStatVisual`。
 */
export interface StatCardProps {
  /** 指标名（小标签）。 */
  label: React.ReactNode
  /** 主数值（大字号 tabular）。 */
  value: React.ReactNode
  /** 副信息，如 `/38`、`在线`。 */
  sub?: React.ReactNode
  /** 左上语义图标。 */
  icon?: React.ReactNode
  /** 图标块色调，默认主色。 */
  tone?: Tone
  /** 
   * 数值本身的语义色。缺省沿用前景色（中性）。
   *
   * 【为什么与 `tone` 分开】`tone` 管的是左上图标块的底色，而数值要表达的常常是
   * 「这个指标健不健康」（在线数绿、异常数红）。两者可以不一致——例如异常数的图标块
   * 用主色、数值用危险红。合成一个属性会逼调用方二选一。
   */
  valueTone?: StatusLevel | 'default'
  /** 占比条（占比型指标）：value/max → 阈值变色 MiniBar。 */
  bar?: { value: number; max?: number; level?: StatusLevel }
  /** 增量（计数型走势）：±数字 + 方向语义着色。 */
  delta?: number
  /** 增量方向语义，缺省「升为好」（绿）；越低越好的指标传 `down`。 */
  deltaGoodWhen?: 'up' | 'down'
  /** 走势型自定义视觉（如趋势线）。 */
  trend?: React.ReactNode
  /** 悬停提示（口径说明），经原生 title 呈现。 */
  title?: string
  /** 
   * 点击回调。传入后卡片变成**可点的筛选项**（语义与外观同步变化）：
   * 渲染成真 `<button>`、有 hover/focus 态、`aria-pressed` 反映激活。
   *
   * 【为什么用 prop 而不是外面包一层 button】包一层会让 `<div>` 留在按钮里，
   * 可访问名与点击目标分离；而这里卡片整体就是那个筛选项，直接渲染成 button 才是对的语义。
   */
  onClick?: () => void
  /** 是否处于激活（已选）态；仅在传了 `onClick` 时有意义。 */
  active?: boolean
  className?: string
}

export function StatCard({
  label,
  value,
  sub,
  icon,
  tone = 'primary',
  valueTone = 'default',
  bar,
  delta,
  deltaGoodWhen,
  trend,
  title,
  onClick,
  active,
  className,
}: StatCardProps) {
  const dt = delta !== undefined ? deltaTone(delta, deltaGoodWhen) : null
  const clickable = !!onClick
  // 可点态渲染真 `<button>`（见 props 注释）；否则保持纯展示的 `<div>`。
  const Root = (clickable ? 'button' : 'div') as 'button'
  return (
    <Root
      data-slot="stat-card"
      title={title}
      type={clickable ? 'button' : undefined}
      onClick={onClick}
      aria-pressed={clickable ? !!active : undefined}
      className={cn(
        // 基础表面与 Card/Panel 共用同一常量（含 bg-card/95；其下模糊只有 5% 可见度，
        // 收益远小于每个卡片各占一个合成层的成本，故默认不带 backdrop-blur）。
        `flex flex-col px-3 py-2.5 ${cardSurface} ${cardSurfaceTone.solid}`,
        clickable &&
          'text-left transition-colors hover:border-primary/40 hover:shadow-soft-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40',
        clickable && (active ? 'border-primary bg-primary/[0.04] ring-1 ring-primary/25' : 'border-border/55'),
        className,
      )}
    >
      {/*
        【横向：图标在左、数字与标签在右】原先是纵向（上面一行图标+标签、下面一行大数字）。
        纵向在「筛选卡」场景下不合适：一排四五张卡时，标签与数字被拆到两行，
        横向扫读时视线要上下跳；而且卡的宽度大都浪费在数字右侧的空白上。
        改成横向后，一行的宽度被竖向内容填满，眼睛横扫一次就能读完一组数字。
        纵向形态保留给需要 bar/trend 的分析型卡（它们的视觉在下方，横向会挤）。
      */}
      <div className="flex min-w-0 items-center gap-2.5">
        {icon && (
          <span className={cn('flex size-7 shrink-0 items-center justify-center rounded-md', toneChipClass(tone))}>
            {icon}
          </span>
        )}
        <div className="flex min-w-0 flex-col">
          <span
            className={cn(
              // 数值是卡片的视觉重心：字号提到 text-2xl、字重加粗，与全站「大数字」口径一致。
              'flex items-baseline gap-1 text-2xl font-bold leading-none tabular-nums',
              valueTone !== 'default' && statusTextClass(valueTone),
            )}
          >
            {value}
            {dt && delta !== undefined && (
              <span className={cn('text-xs font-medium', statusTextClass(dt.level))}>
                {dt.arrow}
                {Math.abs(delta)}
              </span>
            )}
          </span>
          <span className="mt-1 truncate text-[11px] text-muted-foreground">{label}</span>
          {/*
            【副信息为什么独立成行】原先它作为数值的后缀排在 `items-baseline` 那一行里，
            数值一长（如 `1,234`）副文案就被挤到换行、或与数值挤在同一行显得局促。
            挪到标签下方后：数值永远独占一行、副信息有自己的呼吸空间，
            与「标签在上、数值居中、副信息在下」的纵向扫读顺序一致。
          */}
          {sub && <span className="mt-0.5 truncate text-[11px] text-muted-foreground">{sub}</span>}
        </div>
      </div>
      {bar && <MiniBar value={bar.value} max={bar.max} level={bar.level} className="mt-2" />}
      {trend && <div className="mt-2">{trend}</div>}
    </Root>
  )
}
