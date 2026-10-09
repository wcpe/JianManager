import { useMemo } from 'react'
import { useSearchParams } from 'react-router'
import { SESSION_BOTS_PAGE_SIZE, SessionBots as SessionBotsView } from '@/components/views/bot-load/session/SessionBots'
import { useBotLoadRunBots } from '@/api/bot-load'
import { readBotFilter, writeBotFilter } from '@/lib/bot-load-filters'

/**
 * 会话 Bot 列表接线层：过滤条件与页码在 URL 上（可分享深链），展示层已回迁应用侧。
 */
export function SessionBots({ runId }: { runId: number | string }) {
  const [params, setParams] = useSearchParams()
  const filter = useMemo(() => readBotFilter(params), [params])

  const query = useBotLoadRunBots(runId, {
    page: filter.page,
    pageSize: SESSION_BOTS_PAGE_SIZE,
    q: filter.q,
    status: filter.status,
    executorNodeId: filter.node,
    stepId: filter.step,
    errorCode: filter.error,
  })

  return (
    <SessionBotsView
      filter={filter}
      onFilterChange={(partial) =>
        setParams(writeBotFilter(params, { ...filter, ...partial }), { replace: true })
      }
      data={query.data}
      isLoading={query.isLoading}
    />
  )
}
