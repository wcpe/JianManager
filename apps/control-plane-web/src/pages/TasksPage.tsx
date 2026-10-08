// 视图已迁至 @jianmanager/ui（ADR-097）；本层只做列表/详情取数、筛选与窗口状态、取消任务与 toast 接线。
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { useNodes } from '@/api/nodes'
import { useTasks, useTask, useCancelTask } from '@/api/tasks'
import {
  TASKS_WINDOW_MAX,
  TASKS_WINDOW_STEP,
  TasksPageView,
  type TasksFilterState,
} from '@jianmanager/ui/components/views/tasks/TasksPageView'

/** 全部筛选清空（重置按钮与初次进入共用同一份初值）。 */
const EMPTY_FILTERS: TasksFilterState = { state: '', kind: '', nodeId: '', keyword: '', time: '' }

/** 时间快捷筛选 → 创建时间下界（RFC3339）。在事件处理器中调用（避免 render 里的不纯 Date.now）。 */
function sinceFromFilter(v: '' | '24h' | '7d'): string | undefined {
  if (v === '24h') return new Date(Date.now() - 86_400_000).toISOString()
  if (v === '7d') return new Date(Date.now() - 7 * 86_400_000).toISOString()
  return undefined
}

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 全局任务中心页容器（ADR-097 b 范式）：列表与详情取数、短轮询、筛选/增长窗口状态、
 * 取消任务的 mutation 与 toast 文案都在这里决定，行渲染与二次确认框交共享视图。
 *
 * 受控状态归属：会触发重新取数的——筛选五维、`limit` 增长窗口、`expandedId`（展开即拉详情）——归本层；
 * 展开行实测高度、确认框开合等纯 UI 状态留包内。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function TasksPage() {
  const { t } = useTranslation()
  // 通知中心/页眉跳转深链（FR-226）：?task=<taskId> 进页即自动展开该任务，定位上下文。
  const [searchParams] = useSearchParams()
  const [expandedId, setExpandedId] = useState<string | null>(() => searchParams.get('task'))

  // 筛选条件（FR-227）。
  const [filters, setFilters] = useState<TasksFilterState>(EMPTY_FILTERS)
  const [since, setSince] = useState<string | undefined>(undefined)
  // 增长窗口（FR-337）：「加载更多」+100 封顶 500；筛选变化复位。
  const [limit, setLimit] = useState(TASKS_WINDOW_STEP)

  const { data: nodes } = useNodes()
  const { data: page, isLoading, isError } = useTasks({
    state: filters.state,
    kind: filters.kind,
    nodeId: filters.nodeId ? Number(filters.nodeId) : undefined,
    keyword: filters.keyword,
    since,
    limit,
  })
  const tasks = useMemo(() => page?.items ?? [], [page])
  // 详情懒查（FR-208）：仅展开时取；进行中 2s 短轮询（规则在 api/tasks 内）。
  const { data: detail, isLoading: detailLoading } = useTask(expandedId ?? undefined)
  const cancel = useCancelTask()
  // 在途取消的任务 id 集合：原实现每行各自一个 useCancelTask，多行同时取消时「取消中」并存；
  // 共享单个 mutation 实例后 isPending 只反映最后一次，故在此按任务累计，保住逐行独立反馈。
  const [cancelingIds, setCancelingIds] = useState<string[]>([])

  /**
   * 筛选变更：任一维度变化即复位增长窗口（FR-337），避免停留在放大的窗口；
   * 时间快捷项在此换算成查询用的 `since`（含 Date.now()，故只在事件处理器里算）。
   */
  const changeFilters = (patch: Partial<TasksFilterState>) => {
    setLimit(TASKS_WINDOW_STEP)
    if (patch.time !== undefined) setSince(sinceFromFilter(patch.time))
    setFilters((f) => ({ ...f, ...patch }))
  }

  return (
    <TasksPageView
      tasks={tasks}
      total={page?.total}
      limit={limit}
      isLoading={isLoading}
      isError={isError}
      nodes={nodes}
      filters={filters}
      expandedId={expandedId}
      detail={{ logs: detail?.logs, isLoading: detailLoading }}
      // 只有容器知道在取消哪些任务；集合为空时所有行都可点。
      cancelingIds={cancelingIds}
      renderLink={({ to, className, children }) => <Link to={to} className={className}>{children}</Link>}
      onChangeFilters={changeFilters}
      onResetFilters={() => {
        setFilters(EMPTY_FILTERS)
        setSince(undefined)
        setLimit(TASKS_WINDOW_STEP)
      }}
      onLoadMore={() => setLimit((l) => Math.min(l + TASKS_WINDOW_STEP, TASKS_WINDOW_MAX))}
      onToggleExpand={(taskId) => setExpandedId((id) => (id === taskId ? null : taskId))}
      onCancelTask={async (taskId) => {
        setCancelingIds((ids) => (ids.includes(taskId) ? ids : [...ids, taskId]))
        try {
          await cancel.mutateAsync(taskId)
          toast.success(t('tasks.cancelRequested', '已请求停止'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('tasks.cancelFailed', '停止失败')))
          return false
        } finally {
          // 无论成败都出集合：成功由列表刷新体现「取消中」，失败要恢复可点。
          setCancelingIds((ids) => ids.filter((id) => id !== taskId))
        }
      }}
    />
  )
}
