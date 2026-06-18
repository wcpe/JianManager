import { useTranslation } from 'react-i18next'
import { Bot } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { InstanceBotBadge as BadgeData } from './bot-list'

/**
 * 实例树行内的 Bot 聚合徽标（FR-039）：展示「在线/总数」，不展开为逐个 Bot。
 * 数据来自 `GET /bots/summary?groupBy=instance` 的单次聚合（由 InstanceTree 统一拉取）。
 * 无 Bot（total=0）时不渲染，避免空徽标干扰。
 */
interface InstanceBotBadgeProps {
  badge: BadgeData | undefined
}

export default function InstanceBotBadge({ badge }: InstanceBotBadgeProps) {
  const { t } = useTranslation()
  if (!badge || badge.total === 0) return null
  const allOnline = badge.online === badge.total
  return (
    <Badge
      variant="outline"
      className={cn(
        'h-5 gap-1 px-1.5 text-[10px] font-normal tabular-nums',
        badge.online === 0 ? 'text-muted-foreground' : allOnline ? 'text-green-600 dark:text-green-500' : 'text-amber-600 dark:text-amber-500',
      )}
      title={t('console.botBadgeTitle', { online: badge.online, total: badge.total })}
    >
      <Bot className="size-3" />
      {badge.online}/{badge.total}
    </Badge>
  )
}
