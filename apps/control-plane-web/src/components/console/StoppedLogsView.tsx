import { useState } from 'react'
import { Link } from 'react-router'
import { toast } from 'sonner'
import { StoppedLogsView as StoppedLogsViewImpl } from '@/components/views/console/StoppedLogsView'
import { useLogsFederation } from '@/api/logFederation'
import type { LogEntry } from '@/lib/console/console-log-types'

/**
 * 停机日志回放的应用接线层。
 *
 * 视图本体是受控视图（见 components/views）；本层取数、把复制回执接回 toast，
 * 并注入「完整历史」的路由链接。对外 props 与原实现一致，调用点零改动。
 *
 * **数据源（2026-10-29）**：CP `logs` 表已下线，改为 VL 联邦 `GET /logs/federation`
 * （按 instanceId 收敛目标）。`useLogs` 走的是已删除的 `/logs` 接口，留在这里会永远空。
 */
export default function StoppedLogsView({ instanceId, status }: { instanceId: number; status: string }) {
  // limit=300 与旧实现的 pageSize=300 对齐，保持「一屏能看到的内容量」不变。
  const history = useLogsFederation({ instanceId, limit: 300 })
  const { data, isLoading, isError, error, refetch } = history
  /**
   * 失败态判定。
   *
   * `useLogsFederation` 会把 403/500/超时/连接失败**不抛错**而是返回一个 `degraded` 的降级信袋
   * （它服务的是日志中心：那里降级后还有平台/历史日志可看）。但本视图拿降级信封没有任何意义——
   * 它没有别的数据源可退，只能得到 `items=[]`。
   *
   * 旧实现只看 `isError`，于是 403/500/超时全被渲染成「暂无历史日志」——把「无权读取」
   * 和「服务挂了」谎报成「这台实例从来没输出过」。
   */
  const degraded = data?.degraded === true
  const failed = isError || degraded
  const failure = isError ? (error as unknown) : data?.failure
  const statusCode = (failure as { response?: { status?: number } } | null)?.response?.status
  const errorCode = (failure as { code?: string } | null)?.code
  const errorKey = !failed ? null
    : statusCode === 403 ? 'instanceDetail.historyForbidden'
      : errorCode === 'ECONNABORTED' || errorCode === 'ETIMEDOUT' ? 'instanceDetail.historyTimeout'
        : 'instanceDetail.historyLoadError'
  /**
   * 刷新失败时保留上一次成功的行。
   *
   * 失败响应里 `items` 恒为空，若直接拿它渲染，一次瞬时 500 就会把刚看到的关服现场抹成空白。
   */
  const [lastGoodEntries, setLastGoodEntries] = useState<LogEntry[]>([])
  const [lastGoodKey, setLastGoodKey] = useState<string | null>(null)
  const items = Array.isArray(data?.response.items) ? (data.response.items as LogEntry[]) : []
  const successEntries = !failed && !isLoading ? items : null
  // 幂等比较：`items` 每次渲染都是新数组，不能拿引用比（那样每渲一次都 setState ⇒ 无限重渲）。
  const successKey = successEntries && successEntries.length > 0
    ? `${successEntries.length}|${successEntries[successEntries.length - 1]?.time}|${successEntries[successEntries.length - 1]?.message}`
    : null
  if (successKey !== null && successKey !== lastGoodKey) {
    setLastGoodKey(successKey)
    setLastGoodEntries(successEntries ?? [])
  }
  const entries = successEntries ?? lastGoodEntries

  return (
    <StoppedLogsViewImpl
      instanceId={instanceId}
      status={status}
      entries={entries}
      isLoading={isLoading}
      errorKey={errorKey}
      onRetry={() => void refetch()}
      onNotify={(kind, message) => (kind === 'success' ? toast.success(message) : toast.error(message))}
      renderLink={({ to, className, children }) => (
        <Link to={to} className={className}>
          {children}
        </Link>
      )}
    />
  )
}
