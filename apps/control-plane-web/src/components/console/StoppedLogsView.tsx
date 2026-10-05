import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Copy, History } from 'lucide-react'
import { useLogsFederation } from '@/api/logFederation'
import type { LogEntry } from '@/api/logs'
import { copyToClipboard } from '@jianmanager/ui/lib/clipboard'
import { cn } from '@jianmanager/ui'

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

/**
 * 停机/未运行实例的历史日志回放（FR-345）：拉该实例最近日志只读展示，替代空白「实例未运行」占位。
 * 令关服过程/崩溃现场在停机态仍可见，并链到完整历史（搜索/时间筛选/导出）。
 *
 * 数据源（2026-10-29 变更）：原先读 CP `logs` 表（GET /logs），现改为读 VL 联邦
 * （GET /logs/federation，按 instanceId 收敛目标）。原因：CP `logs` 表正在废弃——
 * 它把日志正文与业务数据混在同一个库，且关键词检索是 `LIKE` 全表扫。
 *
 * 重要语义：**实例停机 ≠ Worker 停机**。MC 进程停了，Worker 仍在跑、VL 数据仍在，
 * 所以停机态完全能拿到刚才的输出。这也是本视图能在改数据源后继续工作的前提。
 *
 * FR-417 补复制按钮：这里正是「服务器起不来，把关服/崩溃日志发给别人看」的现场，
 * 而原先只能鼠标拖选——300 行日志靠拖选是最难受的场景之一。
 */
export default function StoppedLogsView({ instanceId, status }: { instanceId: number; status: string }) {
  const { t } = useTranslation()
  // limit=300 与该视图改造前的 pageSize=300 对齐，保持「一屏能看到的内容量」不变。
  const history = useLogsFederation({ instanceId, limit: 300 })
  const { data, isLoading, isError, error } = history
  const items = toEntries(data?.response.items)
  /**
   * 失败态判定（2026-10-29 修，随数据源切换到联邦）。
   *
   * `useLogsFederation` 会把 403/500/超时/连接失败**不抛错**而是返回一个 `degraded` 的降级信袋
   * （它服务的是日志中心：那里降级后还有平台/历史日志可看）。但本视图拿降级信封没有任何意义——
   * 它没有别的数据源可退，只能得到 `items=[]`。
   *
   * 旧实现只看 `isError`，于是 403/500/超时全被渲染成「暂无历史日志」——把「无权读取」
   * 和「服务挂了」谎报成「这台实例从来没输出过」。那正是本视图要防止的误报。
   * 现在把「降级信封」也归入失败，并沿用降级前的区分：403 → 无权限；
   * ECONNABORTED/ETIMEDOUT → 超时；其余 → 加载失败。
   */
  const degraded = data?.degraded === true
  const failed = isError || degraded
  // 降级信袋里原错误由 `buildFederationResult(failure:)` 保留（见 LogsFederationResult.failure）。
  const failure = isError ? (error as unknown) : data?.failure
  const statusCode = (failure as { response?: { status?: number } } | null)?.response?.status
  const errorCode = (failure as { code?: string } | null)?.code
  const errorKey = statusCode === 403 ? 'instanceDetail.historyForbidden'
    : errorCode === 'ECONNABORTED' || errorCode === 'ETIMEDOUT' ? 'instanceDetail.historyTimeout'
      : 'instanceDetail.historyLoadError'
  /**
   * 刷新失败时保留上一次成功的行（FR-417 的「不谎报」语义，随数据源切换重做）。
   *
   * 为何需要：失败响应里 `items` 恒为空，若直接拿它渲染，一次瞬时 500 就会把刚看到的
   * 关服现场抹成空白——用户会以为日志没了。而「加载失败」与「从未有过日志」是两件事。
   *
   * 用 state 而非 ref：渲染期要**读**它来决定列表内容（ref 读值不触发重渲染，
   * 且 react-hooks/refs 会报「Cannot access refs during render」）。
   * 写入时机用「渲染期按需调整 state」（React 官方推荐的派生 state 写法），
   * 也避免先渲一帧空白再把旧行补回来的闪烁。
   */
  const [lastGoodEntries, setLastGoodEntries] = useState<LogEntry[]>([])
  const [lastGoodKey, setLastGoodKey] = useState<string | null>(null)
  // 联邦返回按时间倒序（最新在前）；终端语义要最新在底部，故反转。
  const successEntries = !failed && !isLoading ? items.slice().reverse() : null
  // 幂等比较：`items` 每次渲染都是新数组，不能拿引用比（那样每渲一次都 setState ⇒ 无限重渲）。
  // 用「行数 + 最后一条的 time/message」做指纹，足以覆盖本视图的刷新语义。
  const successKey = successEntries && successEntries.length > 0
    ? `${successEntries.length}|${successEntries[successEntries.length - 1].time}|${successEntries[successEntries.length - 1].message}`
    : null
  if (successKey !== null && successKey !== lastGoodKey) {
    setLastGoodKey(successKey)
    setLastGoodEntries(successEntries ?? [])
  }
  const entries = successEntries ?? lastGoodEntries
  const bodyRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    // 加载后滚到底部（最新日志），贴近终端体验。
    if (bodyRef.current) bodyRef.current.scrollTop = bodyRef.current.scrollHeight
  }, [entries.length])

  // 复制走统一入口（含 HTTP 非安全上下文的 execCommand 兜底），成功/失败都给回执。
  const copyAll = async () => {
    if (entries.length === 0) {
      toast.error(t('instanceDetail.consoleCopyNothing'))
      return
    }
    const ok = await copyToClipboard(entries.map((entry) => entry.message).join('\n'))
    if (ok) toast.success(t('common.copied'))
    else toast.error(t('common.copyFailed'))
  }

  return (
    // h-full：FR-415 起本视图与命令栏同处一个 flex 列中，不撑满就会在自己下方留一大片空白。
    <div className="flex h-full min-h-[400px] flex-col rounded-lg bg-[#1a1b26]">
      <div className="flex items-center justify-between gap-2 border-b border-white/10 px-3 py-1.5 text-xs">
        <span className="truncate text-gray-300">{t('instanceDetail.stoppedShowingHistory', { status })}</span>
        <div className="flex shrink-0 items-center gap-3">
          <button
            type="button"
            onClick={() => void copyAll()}
            className="flex items-center gap-1 text-gray-400 hover:text-gray-100"
          >
            <Copy className="size-3" />
            {t('instanceDetail.consoleCopyLogs')}
          </button>
          <a href={`/logs?instanceId=${instanceId}`} className="text-blue-400 hover:underline">
            {t('instanceDetail.viewFullHistory')}
          </a>
        </div>
      </div>
      {failed && (
        <div role="alert" className="flex items-center justify-between gap-2 border-b border-white/10 px-3 py-2 text-xs text-status-warning">
          <span>{t(errorKey)}{entries.length > 0 && ` · ${t('instanceDetail.historyStale')}`}</span>
          <button type="button" className="shrink-0 underline" disabled={history.isFetching} onClick={() => void history.refetch()}>{t('logsFederation.action.retry')}</button>
        </div>
      )}
      <div ref={bodyRef} className="min-h-0 flex-1 overflow-auto p-2 font-mono text-xs leading-relaxed text-gray-300">
        {isLoading ? (
          <p className="text-gray-500">{t('common.loading')}</p>
        ) : failed && entries.length === 0 ? (
          <p className="text-gray-500">{t('instanceDetail.historyNotLoaded')}</p>
        ) : entries.length === 0 ? (
          // 空态居中卡片：原先一行灰字吊在左上角， pane 大半空白显得像坏了。
          <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
            <div className="grid size-10 place-items-center rounded-full bg-white/[0.04] text-gray-500">
              <History className="size-4.5" />
            </div>
            <p className="max-w-72 text-xs leading-relaxed text-gray-500">{t('instanceDetail.noHistoryLogs')}</p>
          </div>
        ) : (
          entries.map((e) => (
            <div
              key={e.id}
              className={cn(
                'whitespace-pre-wrap break-all',
                e.level === 'error' && 'text-red-400',
                e.level === 'warn' && 'text-amber-400',
              )}
            >
              {e.message}
            </div>
          ))
        )}
      </div>
    </div>
  )
}
