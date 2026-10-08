import { useEffect, useState } from 'react'
import {
  SESSION_EVENTS_PAGE_SIZE,
  SessionEvents as SessionEventsView,
} from '@jianmanager/ui/components/views/bot-load/session/SessionEvents'
import { useBotLoadEvents } from '@/api/bot-load'
import { useSessionEvents } from './SessionEventProvider'

/**
 * 会话事件接线层：分页 / 类型过滤状态与 snapshot 锚点留在容器（它们决定服务端取数），
 * 表格与合并去重逻辑已归包。
 */
export function SessionEvents({ runId }: { runId: number | string }) {
  const { live } = useSessionEvents()
  const [page, setPage] = useState(1)
  const [typeFilter, setTypeFilter] = useState('')
  const [snapshotEventId, setSnapshotEventId] = useState<string | undefined>()

  const query = useBotLoadEvents(runId, {
    page,
    pageSize: SESSION_EVENTS_PAGE_SIZE,
    type: typeFilter || undefined,
    snapshotEventId: page > 1 ? snapshotEventId : undefined,
  })

  useEffect(() => {
    if (page === 1 && query.data?.snapshotEventId) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- 首屏分页锚点随查询结果固定一次
      setSnapshotEventId(query.data.snapshotEventId)
    }
  }, [page, query.data?.snapshotEventId])

  return (
    <SessionEventsView
      page={page}
      onPageChange={(next) => {
        if (query.data?.snapshotEventId) setSnapshotEventId(query.data.snapshotEventId)
        setPage(next)
      }}
      typeFilter={typeFilter}
      onTypeFilterChange={(next) => {
        setTypeFilter(next)
        setPage(1)
        setSnapshotEventId(undefined)
      }}
      data={query.data}
      isLoading={query.isLoading}
      isError={query.isError}
      liveHead={live.historyHead}
    />
  )
}
