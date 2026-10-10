// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做平台管理员门禁、筛选取数与跳转接线。
import { useMemo, useState } from 'react'
import { useDebounced } from '@/lib/hooks/use-debounced'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { useAuthStore } from '@/stores/auth'
import { useAgentCallLogs } from '@/api/agentObservability'
import type { AgentCallLogFilter } from '@/api/agentObservability'
import { useAgentTokens } from '@/api/agentTokens'
import { Button } from '@jianmanager/ui/components/button'
import { AgentCallLogsPageView } from '@/components/views/agent/AgentCallLogsPageView'
import type { AgentCallLogsQuery } from '@/components/views/agent/AgentCallLogsPageView'

const ROLE_PLATFORM_ADMIN = 10

/** 每页条数：后端默认值，同时用于换算总页数。 */
const PAGE_SIZE = 50

/** 初始筛选：全维度不过滤、第一页（空串即「该维度不过滤」，与视图的对外契约一致）。 */
const DEFAULT_QUERY: AgentCallLogsQuery = {
  tokenId: '',
  action: '',
  client: '',
  success: '',
  page: 1,
}

/**
 * Agent 调用流水页（FR-391 / FR-390）容器：平台管理员门禁、筛选/分页状态、Token 候选取数、
 * 按筛选构造请求与页头跳转入口都在这里，筛选条、表格与三态展示交共享视图（ADR-097）。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function AgentCallLogsPage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const isAdmin = role === ROLE_PLATFORM_ADMIN

  // 筛选与分页都会进入查询键，属「取数时机」决策，故 state 归容器；视图只受控展示并逐字段上报改动。
  const [query, setQuery] = useState<AgentCallLogsQuery>(DEFAULT_QUERY)
  /**
   * action 是自由文本筛选取 300ms 防抖：输入框即时回显（`query.action` 原样喂给视图），
   * 但请求键只用停止键入后的值——否则每敲一个字都重取一页流水（本页还带翻页游标）。
   * 其余维度是离散选择（下拉 / 页码），不需要防抖。
   */
  const debouncedAction = useDebounced(query.action, 300)

  const filter = useMemo((): AgentCallLogFilter => {
    const f: AgentCallLogFilter = { page: query.page, pageSize: PAGE_SIZE }
    const tid = Number(query.tokenId)
    if (Number.isInteger(tid) && tid > 0) f.tokenId = tid
    if (debouncedAction.trim()) f.action = debouncedAction.trim()
    if (query.client.trim()) f.client = query.client.trim()
    if (query.success === 'true') f.success = true
    if (query.success === 'false') f.success = false
    return f
  }, [debouncedAction, query])

  const { data: tokens } = useAgentTokens({ enabled: isAdmin })
  const { data, isLoading, isError, isFetching } = useAgentCallLogs(filter, { enabled: isAdmin })

  // 非管理员不渲染页面、也不发请求（两个查询的 enabled 已收敛，此处只是不再挂视图）。
  if (!isAdmin) {
    return <p className="text-sm text-muted-foreground">{t('agentCallLogs.forbidden')}</p>
  }

  return (
    <AgentCallLogsPageView
      query={query}
      // 改动筛选维度一律回到第 1 页（原页每个筛选 handler 都重置了页码），只有翻页才带 page。
      onQueryChange={(patch) =>
        setQuery((q) => ({ ...q, ...patch, page: patch.page ?? 1 }))
      }
      onClear={() => setQuery(DEFAULT_QUERY)}
      tokens={tokens}
      items={data?.items ?? []}
      total={data?.total ?? 0}
      pageSize={data?.pageSize ?? PAGE_SIZE}
      isLoading={isLoading}
      isError={isError}
      isFetching={isFetching}
      headerLinks={
        <>
          <Button variant="outline" size="sm" asChild>
            <Link to="/mcp-activity">{t('agentCallLogs.openSessions')}</Link>
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link to="/agent-tokens">{t('agentCallLogs.openTokens')}</Link>
          </Button>
        </>
      }
    />
  )
}
