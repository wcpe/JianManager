import { useCallback } from 'react'
import { toast } from 'sonner'
import { TerminalPane as TerminalPaneImpl } from '@/components/views/console/TerminalPane'
import type { TerminalPaneProps as TerminalPanePropsFull } from '@/components/views/console/TerminalPane'
import { useInstance, useStartInstance } from '@/api/instances'
import { useTerminalToken } from '@/api/terminal'
import { usePermissionsStore } from '@/stores/permissions'
import { useConsoleHistory } from '@/lib/console/console-history'
import { terminalSessionManager } from '@/lib/console/terminal-session-manager'
import StoppedLogsView from './StoppedLogsView'

/**
 * 对外 props（沿用原导出名）：接线层自己注入的字段从调用点口径里排除，
 * 调用点（WorkspaceCard / 控制台页面 / 沉浸 pane）只传原来那套 props，零改动。
 */
export type TerminalPaneProps = Omit<
  TerminalPanePropsFull,
  | 'status'
  | 'instanceName'
  | 'fetchToken'
  | 'onStartInstance'
  | 'isStarting'
  | 'isTokenLoading'
  | 'tokenError'
  | 'canAccessTerminal'
  | 'historyBacktrack'
  | 'renderStoppedLogs'
>

/**
 * 终端面板的应用接线层（ADR-097）。
 *
 * 视图本体是受控视图（见 components/views）；本层注入：实例状态与名称、终端凭据的取数回调（每次连接前现取，
 * 一次性 token 复用会 401，见 FR-140）、启动动作与其 pending、`terminal.access` 权限，
 * 以及历史回溯控制器（内部走 DB 分页取数）。其余 props 原样透传。
 */
export default function TerminalPane({ instanceId, ...rest }: TerminalPaneProps) {
  const { data: instance } = useInstance(instanceId)
  const status = instance?.status ?? ''
  const isStopped = status === 'STOPPED'
  // 仅在状态已知且非完全停机时请求 token：STOPPED 与状态未知都不发起 WS。
  const canAttach = !!status && !isStopped
  const { isLoading, error, refetch } = useTerminalToken(instanceId, 'write', canAttach)
  // 一次性 token 首连即被 CP 消费失效，重连必须现取新 token——故暴露拉取回调而非静态 token。
  const fetchToken = useCallback(async () => {
    const result = await refetch()
    const data = result.data
    if (!data?.wsUrl || !data.token) {
      throw new Error(result.error instanceof Error ? result.error.message : '获取终端令牌失败')
    }
    return { wsUrl: data.wsUrl, token: data.token }
  }, [refetch])
  const startInstance = useStartInstance()
  const canAccessTerminal = usePermissionsStore((s) => s.hasPerm('terminal.access'))
  // 历史回溯接缝取自行缓冲的最早保留时刻（每次渲染现取）。
  const historyAnchorAt = terminalSessionManager.getHistoryAnchorAt(instanceId)
  const historyBacktrack = useConsoleHistory({ instanceId, anchorTime: historyAnchorAt })

  return (
    <TerminalPaneImpl
      instanceId={instanceId}
      {...rest}
      status={status}
      instanceName={instance?.name}
      fetchToken={fetchToken}
      isTokenLoading={isLoading}
      tokenError={error ? (error as Error).message : undefined}
      onStartInstance={() =>
        startInstance.mutate(instanceId, {
          onError: (err) => toast.error(err instanceof Error ? err.message : '操作失败'),
        })
      }
      isStarting={startInstance.isPending}
      canAccessTerminal={canAccessTerminal}
      historyBacktrack={historyBacktrack}
      renderStoppedLogs={({ instanceId: stoppedId, status: stoppedStatus }) => (
        <StoppedLogsView instanceId={stoppedId} status={stoppedStatus} />
      )}
    />
  )
}
