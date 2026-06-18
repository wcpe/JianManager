import { botStatusKind } from './bot-list'
import { cn } from '@/lib/utils'

/**
 * Bot 状态点（FR-039）：在线绿 / 连接中琥珀(脉冲) / 异常红 / 离线空心灰。
 * 语义分桶见 {@link botStatusKind}。
 */
interface BotStatusDotProps {
  /** 后端 Bot status 字符串（BotInfo.status） */
  status: string
}

export default function BotStatusDot({ status }: BotStatusDotProps) {
  const kind = botStatusKind(status)
  return (
    <span
      aria-label={status}
      title={status}
      className={cn(
        'inline-block size-2 shrink-0 rounded-full',
        kind === 'online' && 'bg-green-500',
        kind === 'connecting' && 'bg-amber-500 animate-pulse',
        kind === 'error' && 'bg-red-500',
        kind === 'offline' && 'border border-muted-foreground/50 bg-transparent',
      )}
    />
  )
}
