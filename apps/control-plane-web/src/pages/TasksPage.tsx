// 视图已回迁应用侧（原 ADR-102 迁包已撤销）；本层只做列表/详情取数、筛选与窗口状态、取消任务与 toast 接线。
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useLocation } from 'react-router'
import { usePageContext } from '@/lib/assistant/page-context'
import { toast } from 'sonner'
import { useNodes } from '@/api/nodes'
import { useTasks, useTask, useCancelTask } from '@/api/tasks'
import { useDebounced } from '@/lib/hooks/use-debounced'
import { TASKS_WINDOW_MAX, TASKS_WINDOW_STEP, TasksPageView } from '@/components/views/tasks/TasksPageView'
import type { TasksFilterState } from '@/components/views/tasks/TasksPageView'

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
 * 全局任务中心页容器（ADR-102 b 范式）：列表与详情取数、短轮询、筛选/增长窗口状态、
 * 取消任务的 mutation 与 toast 文案都在这里决定，行渲染与二次确认框交共享视图。
 *
 * 受控状态归属：会触发重新取数的——筛选五维、`limit` 增长窗口、`expandedId`（展开即拉详情）——归本层；
 * 展开行实测高度、确认框开合等纯 UI 状态留包内。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function TasksPage() {
  const { t } = useTranslation()
  // 通知中心/页眉跳转深链（FR-226）：?task=<taskId> 进页即自动展开该任务，定位上下文。
  const location = useLocation()
  const [expandedId, setExpandedId] = useState<string | null>(
    () => new URLSearchParams(location.search).get('task'),
  )

  /**
   * 深链同步：只在挂载时读一次 `?task=` 是不够的——页眉任务菜单 / 通知中心在**本页已挂载**时再跳
   * `/tasks?task=<id>`，路由只换查询串、组件不重挂，初值不再生效，展开的仍是上一个任务（甚至什么都不展开）。
   * 故按「每次导航」（location.key 每次都变，重复点同一条菜单也算）回灌 URL 里的 task。
   * 反向不回写：用户手动展开/折叠不写 URL——否则折叠一行就会把深链抹掉，刷新后定位丢失。
   * 无 `task` 参数时不动展开态（一次无参导航不该收起用户手动展开的行）。
   */
  const deepLinkTask = new URLSearchParams(location.search).get('task')

  // 页面上报（FR-518 §4.2）：任务页只发「正在看哪个任务」，不发任务详情正文
  // （详情可能含命令与环境信息，由工具按权限取）。
  const contextTaskId = useMemo(
    () => new URLSearchParams(location.search).get('task'),
    [location.search],
  )
  usePageContext(
    useMemo(
      () => ({
        pageKey: 'tasks',
        ...(contextTaskId ? { entities: [{ type: 'task' as const, id: contextTaskId }] } : {}),
      }),
      [contextTaskId],
    ),
  )
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- URL（外部输入）变化时回灌展开态，属该规则的合法例外
    if (deepLinkTask) setExpandedId(deepLinkTask)
  }, [deepLinkTask, location.key])

  // 筛选条件（FR-227）。
  const [filters, setFilters] = useState<TasksFilterState>(EMPTY_FILTERS)
  const [since, setSince] = useState<string | undefined>(undefined)
  // 增长窗口（FR-337）：「加载更多」+100 封顶 500；筛选变化复位。
  const [limit, setLimit] = useState(TASKS_WINDOW_STEP)

  /**
   * 关键字防抖（300ms）：输入框仍即时回显（`filters.keyword` 原样喂给视图），但请求键只用停止键入后的值。
   * 本页 `useTasks` 是 `staleTime: 0` + 2 秒短轮询，不防抖的话每敲一个字都要发一次全量列表请求，
   * 还会与在途请求、轮询请求叠在一起（叠加越滚越大）。取数口径（含防抖）归容器，与通知中心同款。
   */
  const debouncedKeyword = useDebounced(filters.keyword, 300).trim()

  const { data: nodes } = useNodes()
  const { data: page, isLoading, isError } = useTasks({
    state: filters.state,
    kind: filters.kind,
    nodeId: filters.nodeId ? Number(filters.nodeId) : undefined,
    keyword: debouncedKeyword || undefined,
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
