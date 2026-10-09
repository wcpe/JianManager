import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Activity, Bot, Box, ChevronDown, ChevronRight, Server } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Badge } from '@jianmanager/ui/components/badge'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { BotHealthBar } from '@/components/views/console/BotHealthBar'
import { toneChipClass, type Tone } from '@jianmanager/ui/lib/tone'
import type { BotSummaryGroup } from '@/lib/bots/bot'
import type { InstanceBotBadge as InstanceBotBadgeData } from '@/lib/bots/bot-list'
import type { GroupByDim } from '@/lib/bots/bots-overview'

// ── 实例树行内的 Bot 聚合徽标（FR-039）────────────────────────────────

/**
 * 实例树行内的 Bot 聚合徽标：展示「在线/总数」，不展开为逐个 Bot。
 * 数据来自 `GET /bots/summary?groupBy=instance` 的单次聚合（由 InstanceTree 统一拉取）。
 * 无 Bot（total=0）时不渲染，避免空徽标干扰。
 */
export function InstanceBotChip({ badge }: { badge: InstanceBotBadgeData | undefined }) {
  const { t } = useTranslation()
  if (!badge || badge.total === 0) return null
  const allOnline = badge.online === badge.total
  return (
    <Badge
      variant="outline"
      className={cn(
        'h-5 gap-1 px-1.5 text-[10px] font-normal tabular-nums',
        badge.online === 0
          ? 'text-muted-foreground'
          : allOnline
            ? 'text-green-600 dark:text-green-500'
            : 'text-amber-600 dark:text-amber-500',
      )}
      title={t('console.botBadgeTitle', { online: badge.online, total: badge.total })}
    >
      <Bot className="size-3" />
      {badge.online}/{badge.total}
    </Badge>
  )
}

// ── Bot 分组工作台卡（FR-147）────────────────────────────────────────

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

export interface BotWorktableCardProps {
  groupBy: GroupByDim
  group: BotSummaryGroup
  checked: boolean
  onCheck: () => void
  expanded: boolean
  onToggleExpand: () => void
  /** 右下操作区（在控制台打开 / 批量菜单，由页面渲染）。 */
  actions: ReactNode
  /** 展开窥视内容（成员分页），仅 expanded 时由页面挂载。 */
  children?: ReactNode
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
}: BotWorktableCardProps) {
  const { t } = useTranslation()
  const { icon: Icon, tone } = dimVisual(groupBy)

  return (
    <div className="flex flex-col rounded-xl border bg-card text-card-foreground shadow-soft transition-[box-shadow] duration-300 ease-ios hover:shadow-lift">
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
              {expanded ? (
                <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
              ) : (
                <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
              )}
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

// ── 节点切换下拉（FR-069 瘦身）────────────────────────────────────────

/** 哨兵值：表示「全部节点」（shadcn Select 仅接受字符串 value）。 */
const ALL = 'all'

/** 节点筛选项（容器取数：个位/十位量级，无服务端搜索需求）。 */
export interface NodeSwitcherNode {
  id: number
  name: string
}

export interface NodeSwitcherProps {
  /** 可选节点列表（容器经 useNodes 取数）。 */
  nodes?: NodeSwitcherNode[]
  /** 当前选中节点；null = 全部。 */
  selectedNodeId: number | null
  onChange: (id: number | null) => void
}

/**
 * 实例树上方的节点切换下拉：「全部节点」+ 各节点。
 * 紧凑控件——矮行高 + 小字号 + 前置节点图标，避免占用过多侧栏垂直空间。
 */
export function NodeSwitcher({ nodes, selectedNodeId, onChange }: NodeSwitcherProps) {
  const { t } = useTranslation()

  return (
    <Select
      value={selectedNodeId === null ? ALL : String(selectedNodeId)}
      onValueChange={(v: string) => onChange(v === ALL ? null : Number(v))}
    >
      <SelectTrigger
        size="sm"
        className="h-7 w-full gap-1.5 px-2 text-xs [&_svg]:size-3.5"
        aria-label={t('console.nodeSwitcher')}
      >
        <Server className="shrink-0 text-muted-foreground" />
        <SelectValue placeholder={t('console.allNodes')} />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL}>{t('console.allNodes')}</SelectItem>
        {nodes?.map((node) => (
          <SelectItem key={node.id} value={String(node.id)}>
            {node.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
