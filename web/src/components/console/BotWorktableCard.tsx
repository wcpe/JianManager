import { useTranslation } from 'react-i18next'
import { Bot, Server, Activity, Box, ChevronDown, ChevronRight } from 'lucide-react'
import type { BotSummaryGroup } from '@/api/bots'
import { Checkbox } from '@/components/ui/checkbox'
import { Badge } from '@/components/ui/badge'
import { BotHealthBar } from './BotHealthBar'
import type { GroupByDim } from '@/pages/bots-overview'
import { toneChipClass, type Tone } from '@/lib/tone'
import { cn } from '@/lib/utils'

/** 分组维度 → 图标 + 图标块色调。 */
function dimVisual(dim: GroupByDim): { icon: typeof Bot; tone: Tone } {
  switch (dim) {
    case 'instance':
      return { icon: Box, tone: 'primary' }
    case 'node':
      return { icon: Server, tone: 'info' }
    case 'status':
      return { icon: Activity, tone: 'success' }
    case 'behavior':
      return { icon: Bot, tone: 'primary' }
  }
}

/**
 * Bot 分组工作台卡（FR-147，§4.5 运行实体范式）。
 * 每个分组一张卡：图标块 + 标签 + 多段健康条 + 总数 + 勾选（批量）+ 展开窥视 + 操作区。
 * 健康条对 status 维度卡（单一状态）天然单色；其余维度用分组 online/total 两段。
 */
export function BotWorktableCard({
  groupBy,
  group,
  checked,
  onCheck,
  expanded,
  onToggleExpand,
  actions,
  children,
}: {
  groupBy: GroupByDim
  group: BotSummaryGroup
  checked: boolean
  onCheck: () => void
  expanded: boolean
  onToggleExpand: () => void
  /** 右下操作区（在控制台打开 / 批量菜单，由页面渲染）。 */
  actions: React.ReactNode
  /** 展开窥视内容（成员分页），仅 expanded 时由页面挂载。 */
  children?: React.ReactNode
}) {
  const { t } = useTranslation()
  const { icon: Icon, tone } = dimVisual(groupBy)

  return (
    <div className="flex flex-col rounded-xl border bg-card text-card-foreground shadow-soft transition-[transform,box-shadow] duration-300 ease-ios hover:-translate-y-0.5 hover:shadow-lift">
      <div className="flex flex-col gap-3 p-4">
        <div className="flex items-center gap-3">
          <Checkbox checked={checked} onCheckedChange={onCheck} aria-label={t('bots.select')} />
          <span className={cn('flex size-9 shrink-0 items-center justify-center rounded-xl', toneChipClass(tone))}>
            <Icon className="size-5" />
          </span>
          <div className="min-w-0 flex-1">
            <button
              type="button"
              onClick={onToggleExpand}
              className="flex max-w-full items-center gap-1 text-left text-sm font-semibold hover:text-primary"
              title={group.label || group.key}
            >
              {expanded ? <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" /> : <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />}
              <span className="truncate">{group.label || group.key}</span>
            </button>
            <div className="mt-0.5 text-xs text-muted-foreground">
              {t('bots.healthTooltip', { online: group.online, total: group.total })}
            </div>
          </div>
          <Badge variant="outline" className="shrink-0 font-normal tabular-nums">
            {group.total}
          </Badge>
        </div>

        <BotHealthBar total={group.total} online={group.online} />

        <div className="flex items-center justify-end gap-1 border-t pt-3">{actions}</div>
      </div>
      {expanded && children && <div className="border-t bg-muted/30">{children}</div>}
    </div>
  )
}
