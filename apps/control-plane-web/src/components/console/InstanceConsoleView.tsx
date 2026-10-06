import { useMemo } from 'react'
import { toast } from 'sonner'
import { InstanceConsoleView as InstanceConsoleViewImpl } from '@jianmanager/ui'
import type { InstanceConsoleViewProps as InstanceConsoleViewPropsFull } from '@jianmanager/ui'
import { useOnlinePlayers } from '@/api/players'
import { useConsoleHistory } from '@/lib/console-history'
import { terminalSessionManager } from '@/lib/terminal-session-manager'

/**
 * 对外 props（沿用原导出名）：接线层自己注入的字段（名册 / 回溯控制器 / 提示通道）
 * 从调用点口径里排除，调用点只传原来那套 props，零改动。
 */
export type InstanceConsoleViewProps = Omit<
  InstanceConsoleViewPropsFull,
  'onlinePlayers' | 'historyBacktrack' | 'onNotify'
>

/**
 * 实例控制台视图的应用接线层（ADR-097）。
 *
 * 视图本体已迁入组件库并受控；本层补三处注入：
 * - `onlinePlayers`：`/players` 聚合按本实例过滤后的名册（跨实例聚合的过滤属取数层职责）；
 * - `historyBacktrack`：历史回溯控制器（`useConsoleHistory` 内部走 DB 分页取数）；
 * - `onNotify`：把视图算好的复制/跳转回执接回应用侧 toast。
 * 其余 props 原样透传，调用点无需改动。
 */
export default function InstanceConsoleView({ instanceId, ...rest }: InstanceConsoleViewProps) {
  const { data: onlineData } = useOnlinePlayers()
  const onlinePlayers = useMemo(
    () => (onlineData?.players ?? []).filter((p) => p.instanceId === instanceId).map((p) => p.name),
    [onlineData, instanceId],
  )
  // 接缝取自行缓冲的最早保留时刻（每次渲染现取，与迁移前一致）。
  const historyAnchorAt = terminalSessionManager.getHistoryAnchorAt(instanceId)
  const historyBacktrack = useConsoleHistory({ instanceId, anchorTime: historyAnchorAt })

  return (
    <InstanceConsoleViewImpl
      instanceId={instanceId}
      {...rest}
      onlinePlayers={onlinePlayers}
      historyBacktrack={historyBacktrack}
      onNotify={(kind, message) => (kind === 'success' ? toast.success(message) : toast.error(message))}
    />
  )
}
