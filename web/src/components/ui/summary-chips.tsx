import * as React from 'react'
import { cn } from '@/lib/utils'
import { statusColorVar, type StatusLevel } from '@/lib/threshold'

/** 单个汇总 chip 定义（FR-136/144 汇总头）。 */
export interface SummaryChip {
  /** 唯一键。 */
  key: string
  /** 显示文案（含计数，如「运行 32」）。 */
  label: React.ReactNode
  /** 计数值；与 label 二选一灵活组合，决定是否显示。 */
  count?: number
  /** 状态等级，决定前导色点颜色；不传不显示色点。 */
  level?: StatusLevel
  /** 是否处于激活（已选）态，主色描边高亮。 */
  active?: boolean
  /** 点击回调（如设状态筛选）；不传则为纯展示 chip。 */
  onClick?: () => void
  /** 运行类 chip 设呼吸灯（如「运行中」），脉动表达活动。 */
  breathing?: boolean
}

/**
 * 汇总头 chip 组（FR-136/144）：sticky 一排 pill，可点设筛选。
 * 长表滚动时吸顶常驻（sticky）。视觉用 FR-163 圆角 pill + 柔和阴影 + iOS 缓动。
 */
export function SummaryChips({
  chips,
  className,
}: {
  chips: SummaryChip[]
  className?: string
}) {
  return (
    <div
      className={cn(
        'sticky top-0 z-10 -mx-1 flex flex-wrap items-center gap-2 bg-background/80 px-1 py-2 backdrop-blur supports-[backdrop-filter]:bg-background/60',
        className,
      )}
    >
      {chips.map((chip) => {
        const clickable = !!chip.onClick
        const dotColor = chip.level ? statusColorVar(chip.level) : undefined
        return (
          <button
            key={chip.key}
            type="button"
            onClick={chip.onClick}
            disabled={!clickable}
            aria-pressed={clickable ? !!chip.active : undefined}
            className={cn(
              'inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium shadow-soft transition-all duration-200 ease-ios',
              clickable && 'cursor-pointer hover:shadow-lift',
              !clickable && 'cursor-default',
              chip.active
                ? 'border-primary/50 bg-accent text-primary'
                : 'border-transparent bg-card text-muted-foreground hover:text-foreground',
            )}
          >
            {dotColor && (
              <span
                className={cn('size-1.5 shrink-0 rounded-full', chip.breathing && 'animate-breathing')}
                style={{ backgroundColor: dotColor, color: dotColor }}
              />
            )}
            <span>{chip.label}</span>
            {chip.count !== undefined && (
              <span className="tabular-nums font-semibold text-foreground">{chip.count}</span>
            )}
          </button>
        )
      })}
    </div>
  )
}
