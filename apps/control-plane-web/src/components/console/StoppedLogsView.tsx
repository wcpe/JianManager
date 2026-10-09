import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { toast } from 'sonner'
import { StoppedLogsView as StoppedLogsViewImpl } from '@/components/views/console/StoppedLogsView'
import { useLogsFederation } from '@/api/logFederation'
import type { LogEntry } from '@/api/logs'

/**
 * 停机日志回放的应用接线层（ADR-097）。
 *
 * 视图本体已迁入组件库并受控（不触达 api/路由）；本层负责取数、把复制回执接回 toast、
 * 并注入「完整历史」的路由链接。对外 props 与原实现一致，调用点零改动。
 *
 * 数据源（2026-10-29 变更）：原先读 CP `logs` 表（GET /logs），现改为读 VL 联邦
 * （GET /logs/federation，按 instanceId 收敛目标）。原因：CP `logs` 表正在废弃——
 * 它把日志正文与业务数据混在同一个库，且关键词检索是 `LIKE` 全表扫。
 *
 * 重要语义：**实例停机 ≠ Worker 停机**。MC 进程停了，Worker 仍在跑、VL 数据仍在，
 * 所以停机态完全能拿到刚才的输出。这也是本视图能在改数据源后继续工作的前提。
 */

/** 联邦事件 → LogEntry 行（与 LogsPage 的 mapFederationItems 同口径，只保留这里用到的字段）。 */
function toEntries(items: unknown[] | null | undefined): LogEntry[] {
  if (!Array.isArray(items)) return []
  return items.map((raw, index) => {
    const o = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>
    const message = typeof o.message === 'string' ? o.message : typeof o._msg === 'string' ? o._msg : ''
    const time = typeof o.event_time_utc === 'string'
      ? o.event_time_utc
      : typeof o.time === 'string'
        ? o.time
        : ''
    return {
      id: typeof o.id === 'number' ? o.id : -(index + 1),
      source: 'instance',
      level: typeof o.level === 'string' ? o.level.toLowerCase() : 'info',
      instanceId: typeof o.instance_id === 'number' ? o.instance_id : 0,
      instanceUuid: typeof o.instance_uuid === 'string' ? o.instance_uuid : '',
      nodeId: typeof o.node_id === 'number' ? o.node_id : 0,
      message,
      time,
    } satisfies LogEntry
  })
}

export default function StoppedLogsView({ instanceId, status }: { instanceId: number; status: string }) {
  // limit=300 与该视图改造前的 pageSize=300 对齐，保持「一屏能看到的内容量」不变。
  const history = useLogsFederation({ instanceId, limit: 300 })
  const { data, isLoading, isError } = history
  const items = toEntries(data?.response.items)

  /**
   * 失败态判定（2026-10-29 修，随数据源切换到联邦）。
   *
   * `useLogsFederation` 会把 403/500/超时/连接失败**不抛错**而是返回一个 `degraded` 的降级信袋
   * （它服务的是日志中心：那里降级后还有平台/历史日志可看）。但本视图拿降级信封没有任何意义——
   * 它没有别的数据源可退，只能得到 `items=[]`。旧实现只看 `isError`，于是 403/500/超时
   * 全被渲染成「暂无历史日志」——把「无权读取」和「服务挂了」谎报成「这台实例从来没输出过」。
   */
  const degraded = data?.degraded === true
  const failed = isError || degraded

  // 成功取到过内容后，后续刷新失败时保留上一次的行，避免整块闪成空。
  const [lastGoodEntries, setLastGoodEntries] = useState<LogEntry[]>([])
  const lastGoodKey = useRef<string | null>(null)
  const successEntries = !failed && !isLoading ? items.slice().reverse() : null
  // 幂等比较：`items` 每次渲染都是新数组，不能拿引用比（那样每渲一次都 setState ⇒ 无限重渲）。
  // 用「行数 + 最后一条的 time/message」做指纹，足以覆盖本视图的刷新语义。
  const successKey = successEntries && successEntries.length > 0
    ? `${successEntries.length}|${successEntries[successEntries.length - 1].time}|${successEntries[successEntries.length - 1].message}`
    : null
  useEffect(() => {
    if (successKey !== null && successKey !== lastGoodKey.current) {
      lastGoodKey.current = successKey
      setLastGoodEntries(successEntries ?? [])
    }
  }, [successKey, successEntries])

  return (
    <StoppedLogsViewImpl
      instanceId={instanceId}
      status={status}
      entries={successEntries ?? lastGoodEntries}
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
