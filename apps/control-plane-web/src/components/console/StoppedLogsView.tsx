import { Link } from 'react-router'
import { toast } from 'sonner'
import { StoppedLogsView as StoppedLogsViewImpl } from '@/components/views/console/StoppedLogsView'
import { useLogs } from '@/api/logs'

/**
 * 停机日志回放的应用接线层（ADR-097）。
 *
 * 视图本体已迁入组件库并受控；本层取最近 300 条日志、把复制回执接回 toast，
 * 并注入「完整历史」的路由链接。对外 props 与原实现一致，调用点零改动。
 */
export default function StoppedLogsView({ instanceId, status }: { instanceId: number; status: string }) {
  const { data, isLoading } = useLogs({ instanceId, source: 'instance', pageSize: 300 })

  return (
    <StoppedLogsViewImpl
      instanceId={instanceId}
      status={status}
      entries={data?.items}
      isLoading={isLoading}
      onNotify={(kind, message) => (kind === 'success' ? toast.success(message) : toast.error(message))}
      renderLink={({ to, className, children }) => (
        <Link to={to} className={className}>
          {children}
        </Link>
      )}
    />
  )
}
