import { LayoutGrid, List } from 'lucide-react'
import { cn } from '@/lib/utils'

/** 列表呈现形态：卡片网格 / 紧凑列表（FR-136/144/147 工作台卡 ⇄ 列表切换）。 */
export type ViewMode = 'card' | 'list'

/**
 * 卡片 / 列表视图切换分段控件（FR-163 视觉，靛蓝圆角 + iOS 缓动）。
 * 运行实体三页（实例/节点/Bot）复用，激活项主色高亮。
 */
export function ViewToggle({
  value,
  onChange,
  cardLabel,
  listLabel,
  className,
}: {
  value: ViewMode
  onChange: (v: ViewMode) => void
  /** 卡片项无障碍标签。 */
  cardLabel: string
  /** 列表项无障碍标签。 */
  listLabel: string
  className?: string
}) {
  const items: { mode: ViewMode; label: string; icon: typeof LayoutGrid }[] = [
    { mode: 'card', label: cardLabel, icon: LayoutGrid },
    { mode: 'list', label: listLabel, icon: List },
  ]
  return (
    <div className={cn('inline-flex items-center gap-0.5 rounded-lg bg-muted p-0.5', className)}>
      {items.map(({ mode, label, icon: Icon }) => {
        const active = value === mode
        return (
          <button
            key={mode}
            type="button"
            onClick={() => onChange(mode)}
            aria-label={label}
            aria-pressed={active}
            title={label}
            className={cn(
              'flex size-7 items-center justify-center rounded-md transition-all duration-200 ease-ios',
              active
                ? 'bg-card text-primary shadow-soft'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            <Icon className="size-4" />
          </button>
        )
      })}
    </div>
  )
}
