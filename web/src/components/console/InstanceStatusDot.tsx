import { statusDotKind } from './instance-tree'
import { cn } from '@/lib/utils'

/** 实例状态点：RUNNING 绿 / STARTING·STOPPING 琥珀 / CRASHED 红 / STOPPED 空心灰。 */
interface InstanceStatusDotProps {
  /** 实例状态字符串（InstanceInfo.status） */
  status: string
}

export default function InstanceStatusDot({ status }: InstanceStatusDotProps) {
  const kind = statusDotKind(status)
  return (
    <span
      aria-label={status}
      title={status}
      className={cn(
        'inline-block size-2 shrink-0 rounded-full',
        kind === 'running' && 'bg-green-500 text-green-500 animate-breathing',
        kind === 'transitioning' && 'bg-amber-500 animate-pulse',
        kind === 'crashed' && 'bg-red-500',
        kind === 'stopped' && 'border border-muted-foreground/50 bg-transparent',
      )}
    />
  )
}
