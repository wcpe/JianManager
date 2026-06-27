import * as React from 'react'
import { LayoutGrid, List } from 'lucide-react'

import { cn } from '@/lib/utils'
import { toneChipClass, type Tone } from '@/lib/tone'

/**
 * 配置/记录类页面的「配置行范式」原语（FR-149/151/153/156，设计 §4.5）。
 *
 * 与运行实体的工作台卡相对：配置行不含资源条/呼吸灯，承载属性字段 + 启用开关 toggle +
 * code 条件 + 级别/状态 pill，支持卡片 ⇄ 列表视图切换。视觉复用 FR-163 token（靛蓝圆角、
 * 柔和阴影、iOS 缓动），不硬编码品牌色。这些原语仅供本批次「配置记录」四页复用。
 */

/** 启用开关（受控）：复用告警页既有 toggle 样式，统一 role=switch + a11y。 */
export function ConfigSwitch({
  checked,
  onChange,
  disabled,
  label,
  onLabel,
  offLabel,
}: {
  checked: boolean
  onChange: (next: boolean) => void
  disabled?: boolean
  /** 无障碍标签（aria-label）。 */
  label: string
  /** 开/关状态的 title 文案（hover 提示）。 */
  onLabel?: string
  offLabel?: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      title={checked ? onLabel : offLabel}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors duration-300 ease-ios disabled:opacity-50',
        checked ? 'bg-primary' : 'bg-muted-foreground/30',
      )}
    >
      <span
        className={cn(
          'inline-block size-4 transform rounded-full bg-background shadow transition-transform duration-300 ease-ios',
          checked ? 'translate-x-4' : 'translate-x-0.5',
        )}
      />
    </button>
  )
}

/** 视图模式：卡片或列表。 */
export type ConfigView = 'card' | 'list'

/** 卡片 ⇄ 列表切换分段控件（圆角 pill 分段）。 */
export function ConfigViewToggle({
  view,
  onChange,
  cardLabel,
  listLabel,
}: {
  view: ConfigView
  onChange: (next: ConfigView) => void
  cardLabel: string
  listLabel: string
}) {
  return (
    <div className="inline-flex items-center gap-0.5 rounded-full border bg-muted/40 p-0.5">
      <ToggleSeg active={view === 'card'} onClick={() => onChange('card')} label={cardLabel}>
        <LayoutGrid className="size-3.5" />
      </ToggleSeg>
      <ToggleSeg active={view === 'list'} onClick={() => onChange('list')} label={listLabel}>
        <List className="size-3.5" />
      </ToggleSeg>
    </div>
  )
}

function ToggleSeg({
  active,
  onClick,
  label,
  children,
}: {
  active: boolean
  onClick: () => void
  label: string
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      title={label}
      className={cn(
        'flex items-center gap-1 rounded-full px-2.5 py-1 text-xs font-medium transition-colors duration-200 ease-ios',
        active ? 'bg-card text-foreground shadow-soft' : 'text-muted-foreground hover:text-foreground',
      )}
    >
      {children}
    </button>
  )
}

/** 汇总 chip 单项。 */
export interface SummaryChip {
  /** 文案（已本地化）。 */
  label: string
  /** 数值。 */
  value: number | string
  /** 选中态（点击设筛选时高亮当前）。 */
  active?: boolean
  /** 前导状态色点；neutral 不显点。 */
  tone?: Tone
  onClick?: () => void
}

/** 列表顶部汇总条：圆角 pill chip，可点设筛选（FR-149 全部/启用/告警中）。 */
export function ConfigSummaryChips({ chips }: { chips: SummaryChip[] }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      {chips.map((c, i) => {
        const dotClass =
          c.tone && c.tone !== 'primary' ? toneDotClass(c.tone) : undefined
        const clickable = !!c.onClick
        return (
          <button
            key={i}
            type="button"
            onClick={c.onClick}
            disabled={!clickable}
            aria-pressed={clickable ? !!c.active : undefined}
            className={cn(
              'inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium transition-colors duration-200 ease-ios',
              c.active
                ? 'border-primary/40 bg-accent text-primary'
                : 'bg-card text-muted-foreground',
              clickable && 'hover:border-primary/30 hover:text-foreground',
              !clickable && 'cursor-default',
            )}
          >
            {dotClass && <span className={cn('size-1.5 rounded-full', dotClass)} />}
            <span>{c.label}</span>
            <span className="font-semibold text-foreground">{c.value}</span>
          </button>
        )
      })}
    </div>
  )
}

function toneDotClass(tone: Tone): string {
  switch (tone) {
    case 'success':
      return 'bg-status-success'
    case 'warning':
      return 'bg-status-warning'
    case 'danger':
      return 'bg-status-danger'
    case 'info':
      return 'bg-status-info'
    default:
      return 'bg-muted-foreground'
  }
}

/**
 * 配置行卡片（灵动行）：左语义图标块 + 主体（标题 + 属性/code 条件）+ 右侧 meta/pill/toggle/操作。
 * hover 换阴影反馈（iOS 缓动，FR-176 去位移不抬位）。作卡片视图时一行一卡；列表视图请直接用 shadcn Table（本原语专供卡片视图）。
 */
export function ConfigRow({
  icon,
  tone = 'primary',
  title,
  subtitle,
  code,
  meta,
  trailing,
  className,
}: {
  /** 左侧语义图标元素。 */
  icon?: React.ReactNode
  /** 图标块色调。 */
  tone?: Tone
  title: React.ReactNode
  /** 标题下方副信息（实例名/存储位置等）。 */
  subtitle?: React.ReactNode
  /** 等宽 code 条件（告警条件/cron 表达式），自动套 mono + soft 底。 */
  code?: React.ReactNode
  /** 中部 meta 区（最近触发/下次执行等次要文字）。 */
  meta?: React.ReactNode
  /** 右侧操作/pill/toggle 区。 */
  trailing?: React.ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        'flex items-center gap-3 rounded-xl border bg-card px-4 py-3 text-card-foreground shadow-soft',
        'transition-[box-shadow] duration-300 ease-ios hover:shadow-lift',
        className,
      )}
    >
      {icon && (
        <span
          className={cn(
            'flex size-9 shrink-0 items-center justify-center rounded-xl',
            toneChipClass(tone),
          )}
        >
          {icon}
        </span>
      )}
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <div className="truncate text-sm font-semibold text-foreground">{title}</div>
        {(subtitle || code) && (
          <div className="flex flex-wrap items-center gap-2">
            {code && (
              <span className="inline-block rounded-md bg-muted/60 px-2 py-0.5 font-mono text-xs text-muted-foreground">
                {code}
              </span>
            )}
            {subtitle && <span className="truncate text-xs text-muted-foreground">{subtitle}</span>}
          </div>
        )}
      </div>
      {meta && <div className="hidden shrink-0 text-right text-xs text-muted-foreground sm:block">{meta}</div>}
      {trailing && <div className="flex shrink-0 items-center gap-2">{trailing}</div>}
    </div>
  )
}
