/**
 * @file TasksPageView：全局任务中心页的受控视图，列表取数、筛选查询参数、增长窗口、详情懒查与取消任务由应用容器负责。
 * @input lib/task-status（终态判定 / kind 文案键 / Task 类型）、lib/virtual-list（变高虚拟窗口）、lib/download-failure、
 *        Badge/Panel/Button/Input/Select 原语、layout（PageShell/PageHeader/ListSkeleton）、views/DangerConfirm、翻译上下文
 * @output TasksPageView、TasksPageViewProps、TasksFilterState、TasksDetailState、TaskNodeOption、TasksLinkArgs、
 *         TASKS_WINDOW_STEP、TASKS_WINDOW_MAX
 * @sync apps/control-plane-web/src/pages/TasksPage.tsx、apps/control-plane-web/src/pages/TasksPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-183 任务中心 + FR-208 详情懒查 + FR-227 强制停止与筛选 +
 *        FR-279 网络失败引导 + FR-329 短轮询 + FR-337 增长窗口 + FR-496 阶段 6 视口裁剪）
 */
import { useLayoutEffect, useMemo, useRef, useState, type ReactNode, type Ref } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight, Ban, Loader2, Wifi } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { ListSkeleton, PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import DangerConfirm from '@/components/views/DangerConfirm'
import { isNetworkDownloadFailure } from '@/lib/client-dist/download-failure'
import { isTerminalTask, TASK_KIND_LABEL_KEYS } from '@/lib/tasks/task-status'
import type { Task, TaskLog, TaskState } from '@/lib/tasks/task-status'
import { useVirtualRows } from '@/lib/shared/virtual-list'
import { cn } from '@jianmanager/ui'

/** 任务状态 → Badge 变体与文案键。 */
const STATE_META: Record<TaskState, { variant: 'default' | 'secondary' | 'destructive' | 'outline'; key: string }> = {
  pending: { variant: 'outline', key: 'tasks.state.pending' },
  running: { variant: 'default', key: 'tasks.state.running' },
  succeeded: { variant: 'secondary', key: 'tasks.state.succeeded' },
  failed: { variant: 'destructive', key: 'tasks.state.failed' },
  canceled: { variant: 'outline', key: 'tasks.state.canceled' },
}

const STATE_OPTIONS: TaskState[] = ['pending', 'running', 'succeeded', 'failed', 'canceled']

/**
 * 任务列表增长窗口（FR-337）：初始/步长 100，封顶 500（到顶引导用筛选缩小范围）。
 *
 * 上限与步长由容器（请求 `limit`）与视图（「加载更多」按钮 / 到顶说明）共用，故在此导出而非各写一份——
 * 两处若各存一份，改上限时会静默错位（按钮仍可点，请求却已被服务端钳制）。
 */
export const TASKS_WINDOW_STEP = 100
export const TASKS_WINDOW_MAX = 500

/**
 * `cancelingIds` 的缺省值：模块级常量而非内联 `[]`。
 * 内联会在每次渲染新建数组，让缺省的列表行 props 引用每帧变化，白白击穿子件记忆化。
 */
const NO_CANCELING_IDS: string[] = []

/**
 * 行高估算（px，FR-496 阶段 6 视口裁剪用）：`px-3 py-2.5`(20) + 最高单元格 + `border-b`(1)，
 * 实测（Chromium 1440×900，root 16px）：有详情的行 56.7；最高单元格在「状态徽章 22」与
 * 「标题 20 / 标题+详情 35.7」之间切换 —— 单行 20+22+1=43，两行 20+35.7+1≈57。
 * 估算只用于「尚未量测到的行」撑滚动高度；两档行共用同一基准，故基准的绝对误差只会整体平移
 * 滚动区间，不会累积错位（错位只可能来自「两行/一行」判定错，故该判定与行渲染同源：detail 非空即有第二行）。
 */
const TASK_ROW_BASE_PX = 43
const TASK_ROW_EXTRA_LINE_PX = 14
/** 展开行（错误正文 + 日志）的估算高度：仅用于首帧与无布局环境，浏览器实测后以实测为准。 */
const TASK_ROW_EXPANDED_PX = 260
/** jsdom 无布局（clientHeight 恒 0）时的回退视口高，保证视口裁剪在测试里同样生效。 */
const FALLBACK_VIEWPORT_PX = 640
/** 详情里网络失败引导的两个链接入口统一样式（原页两处字面量相同，抽一处避免分叉）。 */
const DETAIL_LINK_CLASS = 'font-medium underline underline-offset-2 hover:text-amber-800 dark:hover:text-amber-300'

/**
 * 任务中心筛选条件（受控）。
 *
 * 五个维度全部进入列表查询键（`kind/state/nodeId/keyword/since`），任一项变化都触发重新取数，
 * 故整份筛选态由容器持有、逐字段注入：视图不缓存筛选草稿（保持原页「输入即生效」的语义），
 * 只把改动经 `onChangeFilters` 上报。时间快捷项与容器换算出的 `since` 分开——「时间值 → RFC3339」
 * 含 `Date.now()`，属取数口径，不属展示。
 */
export interface TasksFilterState {
  /** 状态筛选；`''` = 全部状态。 */
  state: TaskState | ''
  /** 任务种类筛选（`TASK_KIND_LABEL_KEYS` 的键）；`''` = 全部种类。 */
  kind: string
  /** 节点筛选；`''` = 全部节点。存字符串以贴合 Select 值形态，转数字由容器负责。 */
  nodeId: string
  /** 标题/详情关键词；`''` = 不过滤。 */
  keyword: string
  /** 创建时间快捷筛选；`''` = 全部时间。 */
  time: '' | '24h' | '7d'
}

/** 展开任务行的详情（容器按 `expandedId` 懒查注入）。 */
export interface TasksDetailState {
  /** 日志行（后端按 seq 升序返回）。 */
  logs?: TaskLog[]
  /** 详情取数中：仅当日志尚未到达时显示加载文案。 */
  isLoading?: boolean
}

/** 节点下拉候选（容器取数注入）：视图只用到 id 与名称。 */
export interface TaskNodeOption {
  id: number
  name: string
}

/** 路由链接渲染入参（网络失败引导的两处入口）。 */
export interface TasksLinkArgs {
  to: string
  className?: string
  children: ReactNode
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 * - 任务行、命中总数、列表三态、节点候选、展开任务的详情与取消在途 id 全部经 props 注入
 *   （容器调 `useTasks` / `useNodes` / `useTask` / `useCancelTask`）；
 * - 会触发重新取数的状态——筛选五维、增长窗口 `limit`、展开目标 `expandedId`——归容器；
 *   其中 `expandedId` 上提是因为「展开即触发 `GET /tasks/{id}` 详情懒查」，与「选中任务」同类；
 * - 纯 UI 状态留在本组件：展开行的实测高度、取消二次确认框的开合——二者都不产生请求；
 * - 「任一筛选变化即复位增长窗口」「时间值换算 `since`」「加载更多的钳制」都是取数策略，
 *   由容器决定，视图只上报意图（`onChangeFilters` / `onResetFilters` / `onLoadMore`）；
 * - 取消任务回传 `Promise`：确认框在确认瞬间关闭（与原页一致），成败反馈由容器 toast 负责。
 */
export interface TasksPageViewProps {
  /** 增长窗口内已加载的任务行；缺省按空列表渲染。 */
  tasks?: Task[]
  /** 命中总数（分页信封口径，含尚未加载的行）；`undefined` = 信封尚未到达（渲染数据区骨架）。 */
  total?: number
  /** 增长窗口已请求的行数上限（FR-337）：判定「加载更多 / 已到顶」。 */
  limit?: number
  /** 列表首屏取数中；仅当信封尚未到达时渲染骨架（改筛选时保留旧行不闪屏）。 */
  isLoading?: boolean
  /** 列表取数失败；优先于空态。 */
  isError?: boolean
  /** 节点下拉候选（容器取数注入）；缺省即只有「全部节点」一项。 */
  nodes?: TaskNodeOption[]
  /** 当前筛选条件（受控）。 */
  filters: TasksFilterState
  /** 当前展开任务 id；`null` = 全部折叠（展开会触发详情取数，故由容器持有）。 */
  expandedId?: string | null
  /** 展开任务的详情（日志 + 加载态）；缺省按「无日志、非加载中」渲染。 */
  detail?: TasksDetailState
  /** 取消在途的任务 id 集合：只禁用并转圈命中的行，其余行照常可点（多个取消可并存）。 */
  cancelingIds?: string[]
  /** 渲染路由链接；缺省渲染原生 `<a href>`（包内不依赖 react-router）。 */
  renderLink?: (args: TasksLinkArgs) => ReactNode
  /** 筛选维度变更：只上报被改动的字段；窗口复位与 `since` 换算由容器负责。 */
  onChangeFilters: (patch: Partial<TasksFilterState>) => void
  /** 清空全部筛选条件（容器同时复位增长窗口）。 */
  onResetFilters: () => void
  /** 扩大增长窗口一步（按 `TASKS_WINDOW_MAX` 钳制在容器侧）。 */
  onLoadMore: () => void
  /** 展开/折叠某任务行（容器切换 `expandedId` 并按需取详情）。 */
  onToggleExpand: (taskId: string) => void
  /**
   * 强制停止任务（二次确认已在本视图内完成）；返回是否成功。
   * 本视图确认即收起弹窗，不再消费返回值——在途与结果反馈由容器经 `cancelingIds` 与 toast 呈现。
   */
  onCancelTask: (taskId: string) => Promise<boolean>
}

/**
 * 全局任务中心页（FR-183 + FR-227 + FR-337）。
 * 列长任务（如 JDK 安装）：进度条 + 状态徽标 + 展开看滚动日志。
 * 进行中任务可「强制停止」（FR-227：经心跳真中断 Worker 操作）；列表按 kind/state/node/关键词/时间筛选。
 * 分页信封（FR-337）：顶部「共 N 条 · 已加载 M」，「加载更多」扩大 limit 的增长窗口（offset 恒 0）。
 * 存在进行中任务时由容器短轮询刷新；全部终态时停止。非平台管理员只见自己发起的任务（后端收敛）。
 */
export function TasksPageView({
  tasks = [],
  total,
  limit = TASKS_WINDOW_STEP,
  isLoading = false,
  isError = false,
  nodes,
  filters,
  expandedId = null,
  detail,
  cancelingIds = NO_CANCELING_IDS,
  renderLink,
  onChangeFilters,
  onResetFilters,
  onLoadMore,
  onToggleExpand,
  onCancelTask,
}: TasksPageViewProps) {
  const { t } = useTranslation()
  const { state, kind, nodeId, keyword, time } = filters

  // 视口裁剪（FR-496 阶段 6）：增长窗口最大 500 行，一次性铺进 DOM 是本页的主开销。
  // 展开行的错误正文 + 日志长短不定（估算误差会让其后所有行偏移错位），故实测后回填。
  const expandedRef = useRef<HTMLDivElement>(null)
  const [expandedSize, setExpandedSize] = useState<{ id: string; px: number } | null>(null)
  const expandedPx = expandedSize && expandedSize.id === expandedId ? expandedSize.px : TASK_ROW_EXPANDED_PX
  useLayoutEffect(() => {
    // jsdom 无布局：offsetHeight 恒 0 → 保留估算，测试里窗口完全确定。
    const px = expandedRef.current?.offsetHeight ?? 0
    if (expandedId !== null && px > 0 && px !== expandedPx) setExpandedSize({ id: expandedId, px })
  }, [expandedId, expandedPx, tasks])

  /** 逐行高度：折叠行按「一行/两行」估算，展开行用实测值 → 偏移前缀和贴近真实布局。 */
  const rowSizes = useMemo(
    () =>
      tasks.map((task) =>
        task.taskId === expandedId
          ? expandedPx
          : TASK_ROW_BASE_PX + (task.detail ? TASK_ROW_EXTRA_LINE_PX : 0),
      ),
    [expandedId, expandedPx, tasks],
  )
  const { containerRef, onScroll, range } = useVirtualRows({
    total: tasks.length,
    itemSize: TASK_ROW_BASE_PX,
    overscan: 6,
    fallbackViewportSize: FALLBACK_VIEWPORT_PX,
    sizes: rowSizes,
  })

  const totalCount = total ?? 0
  const hasFilters = !!(state || kind || nodeId || keyword || time)
  const canLoadMore = tasks.length < totalCount && limit < TASKS_WINDOW_MAX
  const windowCapped = tasks.length < totalCount && limit >= TASKS_WINDOW_MAX

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语（PageShell / PageHeader）。
    // 计数说明（「已加载 X / 共 Y」）是文字而非纯数字，故走 description 而不是 count。
    <PageShell data-page="tasks">
      <PageHeader
        title={t('tasks.title')}
        description={total !== undefined && !isError ? t('tasks.countSummary', { total: totalCount, loaded: tasks.length }) : undefined}
      />

      {/* 筛选条（FR-227） */}
      <div className="jm-toolbar-surface flex flex-wrap items-center gap-2 p-2">
        <Input
          value={keyword}
          onChange={(e) => onChangeFilters({ keyword: e.target.value })}
          placeholder={t('tasks.filter.keyword', '搜索标题 / 详情')}
          className="h-8 w-52"
        />
        <FilterSelect value={state} onChange={(v) => onChangeFilters({ state: v as TaskState | '' })} placeholder={t('tasks.filter.allStates', '全部状态')}
          options={STATE_OPTIONS.map((s) => ({ value: s, label: t(STATE_META[s].key) }))} />
        <FilterSelect value={kind} onChange={(v) => onChangeFilters({ kind: v })} placeholder={t('tasks.filter.allKinds', '全部种类')}
          options={Object.entries(TASK_KIND_LABEL_KEYS).map(([value, key]) => ({ value, label: t(key) }))} />
        <FilterSelect value={nodeId} onChange={(v) => onChangeFilters({ nodeId: v })} placeholder={t('tasks.filter.allNodes', '全部节点')}
          options={(nodes ?? []).map((n) => ({ value: String(n.id), label: n.name }))} />
        <FilterSelect value={time} onChange={(v) => onChangeFilters({ time: v as '' | '24h' | '7d' })} placeholder={t('tasks.filter.allTime', '全部时间')}
          options={[{ value: '24h', label: t('tasks.filter.last24h', '近 24 小时') }, { value: '7d', label: t('tasks.filter.last7d', '近 7 天') }]} />
        {hasFilters && (
          <Button variant="ghost" size="sm" onClick={onResetFilters} className="text-muted-foreground">
            {t('common.reset', '重置')}
          </Button>
        )}
      </div>

      {/* FR-496 阶段 6 补丁：数据未到时不再只给一行「加载中」。页头与筛选条先渲染，
          数据区用同壳骨架（同一张 Panel + 同一条吸附列头 + 行占位）顶上，数据到达原地替换。 */}
      {isLoading && total === undefined ? (
        <Panel bodyClassName="p-0">
          <div className="max-h-[calc(100vh-20rem)] overflow-auto">
            <TasksColumnHeader />
            <ListSkeleton rows={12} />
          </div>
        </Panel>
      ) : isError ? (
        <p className="text-destructive">{t('tasks.loadError')}</p>
      ) : tasks.length === 0 ? (
        <Panel>
          <p className="px-3 py-10 text-center text-sm text-muted-foreground">
            {hasFilters ? t('tasks.emptyFiltered', '没有匹配筛选条件的任务') : t('tasks.empty')}
          </p>
        </Panel>
      ) : (
        <>
          <Panel bodyClassName="p-0">
            {/* 虚拟窗口的滚动容器（「加载更多」留在容器外，始终可见） */}
            <div
              ref={containerRef}
              onScroll={onScroll}
              data-testid="tasks-virtual"
              className="max-h-[calc(100vh-20rem)] overflow-auto"
            >
              {/* 列头：随窗口滚动吸附在顶部（否则滚下去就看不到列名） */}
              <TasksColumnHeader />
              {/* 窗口上/下占位：窗口外的行用高度撑开，滚动条与整体高度保持真实 */}
              {range.before > 0 && <div aria-hidden="true" style={{ height: range.before }} />}
              {tasks.slice(range.start, range.end).map((task, offset) => {
                const index = range.start + offset
                return (
                  <TaskRow
                    key={task.taskId}
                    task={task}
                    open={expandedId === task.taskId}
                    isLast={index === tasks.length - 1}
                    rowRef={expandedId === task.taskId ? expandedRef : undefined}
                    detail={detail}
                    cancelingIds={cancelingIds}
                    renderLink={renderLink}
                    onToggle={() => onToggleExpand(task.taskId)}
                    onCancelTask={onCancelTask}
                  />
                )
              })}
              {range.after > 0 && <div aria-hidden="true" style={{ height: range.after }} />}
            </div>
          </Panel>

          {/* 加载更多（FR-337）：扩大 limit 的增长窗口；到顶（500）且仍有剩余时引导筛选收窄。 */}
          {(canLoadMore || windowCapped) && (
            <div className="flex items-center justify-center pb-1">
              {windowCapped ? (
                <p className="text-xs text-muted-foreground">{t('tasks.loadMoreCapped')}</p>
              ) : (
                <Button variant="outline" size="sm" onClick={onLoadMore}>
                  {t('tasks.loadMore')}
                </Button>
              )}
            </div>
          )}
        </>
      )}
    </PageShell>
  )
}

/**
 * 列表列头（FR-496 阶段 6 补丁抽出）：静态列名，不依赖数据，加载态与就绪态共用同一份，
 * 因而骨架期间列名/列宽就已就位，数据到达只是「行长出来」。
 */
function TasksColumnHeader() {
  const { t } = useTranslation()
  return (
    // 随窗口滚动吸附在顶部（否则滚下去就看不到列名）
    <div className="sticky top-0 z-10 flex items-center gap-3 border-b bg-muted/40 px-3 py-2 text-[11px] font-medium text-muted-foreground backdrop-blur-sm">
      <span className="w-4 shrink-0" />
      <span className="min-w-0 flex-1">{t('tasks.task')}</span>
      <span className="w-24 shrink-0">{t('tasks.stateLabel')}</span>
      <span className="w-40 shrink-0">{t('tasks.progress')}</span>
      <span className="w-40 shrink-0">{t('tasks.updatedAt')}</span>
      <span className="w-20 shrink-0" />
    </div>
  )
}

/** 筛选下拉：空值=占位（全部）。 */
function FilterSelect({ value, onChange, placeholder, options }: {
  value: string
  onChange: (v: string) => void
  placeholder: string
  options: { value: string; label: string }[]
}) {
  // shadcn Select 不接受空字符串 value；用哨兵 '__all__' 表示「全部」。
  const ALL = '__all__'
  return (
    <Select value={value || ALL} onValueChange={(v) => onChange(v === ALL ? '' : v)}>
      <SelectTrigger size="sm" className="h-8 w-36">
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL}>{placeholder}</SelectItem>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/** 单条任务行：进度条 + 状态徽标 + 强制停止；点击展开看日志（详情与展开态由容器注入）。 */
function TaskRow({
  task,
  open,
  isLast,
  rowRef,
  detail,
  cancelingIds,
  renderLink,
  onToggle,
  onCancelTask,
}: {
  task: Task
  open: boolean
  /** 末行不画分隔线：虚拟窗口下 `last:` 变体只看「已渲染的最后一个」，会误伤窗口末行。 */
  isLast: boolean
  /** 展开行挂给页面实测高度（仅展开时传入）。 */
  rowRef?: Ref<HTMLDivElement>
  /** 展开任务的详情（仅展开行消费）。 */
  detail?: TasksDetailState
  /** 取消在途的任务 id 集合。 */
  cancelingIds: string[]
  /** 路由链接渲染插槽（网络失败引导）。 */
  renderLink?: (args: TasksLinkArgs) => ReactNode
  onToggle: () => void
  onCancelTask: (taskId: string) => Promise<boolean>
}) {
  const { t } = useTranslation()
  // 在线 running 已请求取消 → 显「取消中」；否则按状态。
  const canceling = task.state === 'running' && task.cancelRequested
  const badge = canceling
    ? { variant: 'outline' as const, label: t('tasks.state.canceling', '取消中') }
    : { variant: STATE_META[task.state].variant, label: t(STATE_META[task.state].key) }
  return (
    <div ref={rowRef} className={cn('border-border/60', !isLast && 'border-b')}>
      <div className="flex items-center">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          className="flex min-w-0 flex-1 items-center gap-3 px-3 py-2.5 text-left text-sm transition-colors hover:bg-accent/50"
        >
          <ChevronRight
            className={cn(
              'size-4 shrink-0 text-muted-foreground transition-transform duration-200 ease-ios',
              open && 'rotate-90',
            )}
          />
          <span className="min-w-0 flex-1">
            <span className="block truncate font-medium">{task.title || task.kind}</span>
            {task.detail && <span className="block truncate text-[11px] text-muted-foreground">{task.detail}</span>}
          </span>
          <span className="w-24 shrink-0">
            <Badge variant={badge.variant}>{badge.label}</Badge>
          </span>
          <span className="w-40 shrink-0">
            <ProgressBar value={task.progress} terminal={isTerminalTask(task)} failed={task.state === 'failed'} />
          </span>
          <span className="w-40 shrink-0 font-mono text-[11px] text-muted-foreground">
            {new Date(task.updatedAt).toLocaleString()}
          </span>
        </button>
        <span className="flex w-20 shrink-0 justify-center px-2">
          {!isTerminalTask(task) && (
            <CancelTaskButton
              task={task}
              canceling={cancelingIds.includes(task.taskId)}
              onCancelTask={onCancelTask}
            />
          )}
        </span>
      </div>
      {open && <TaskDetail error={task.error} detail={detail} renderLink={renderLink} />}
    </div>
  )
}

/** 强制停止按钮（FR-227）：二次确认后上报容器取消，经心跳真中断 Worker 操作。 */
function CancelTaskButton({ task, canceling, onCancelTask }: {
  task: Task
  /** 该行取消在途：只禁用并转圈本行。 */
  canceling: boolean
  onCancelTask: (taskId: string) => Promise<boolean>
}) {
  const { t } = useTranslation()
  const [confirm, setConfirm] = useState(false)
  const label = t('tasks.forceStop', '强制停止')
  return (
    <>
      <Button
        variant="ghost"
        size="sm"
        className="h-7 px-2 text-destructive hover:bg-destructive/10 hover:text-destructive"
        disabled={canceling || task.cancelRequested}
        onClick={() => setConfirm(true)}
        aria-label={label}
        title={label}
      >
        {canceling ? <Loader2 className="size-3.5 animate-spin" /> : <Ban className="size-3.5" />}
      </Button>
      <DangerConfirm
        open={confirm}
        title={t('tasks.forceStopTitle', '强制停止任务?')}
        description={t('tasks.forceStopDesc', '将中断该任务在节点上的执行（如下载会被取消、临时文件清理），不可恢复。')}
        confirmLabel={label}
        onCancel={() => setConfirm(false)}
        onConfirm={() => {
          // 与原页一致：确认即收框，随后由容器执行取消并弹提示。
          setConfirm(false)
          void onCancelTask(task.taskId)
        }}
      />
    </>
  )
}

/** 进度条：失败时用 destructive 色，终态成功满格。 */
function ProgressBar({ value, terminal, failed }: { value: number; terminal: boolean; failed: boolean }) {
  const pct = Math.max(0, Math.min(100, value))
  return (
    <div className="flex items-center gap-2">
      <div className="h-1.5 w-28 overflow-hidden rounded-full bg-muted">
        <div
          className={cn('h-full rounded-full transition-all', failed ? 'bg-destructive' : 'bg-primary')}
          style={{ width: `${failed ? 100 : pct}%` }}
        />
      </div>
      <span className="w-9 text-right text-[11px] tabular-nums text-muted-foreground">
        {terminal && !failed ? 100 : pct}%
      </span>
    </div>
  )
}

/**
 * 任务详情：错误原因（含 FR-279 网络失败引导）+ 滚动日志；日志与加载态由容器注入。
 */
function TaskDetail({ error, detail, renderLink }: {
  error: string
  detail?: TasksDetailState
  renderLink?: (args: TasksLinkArgs) => ReactNode
}) {
  const { t } = useTranslation()
  const logs = detail?.logs ?? []
  const isLoading = detail?.isLoading ?? false
  // 两处引导入口共用同一套链接样式（随 renderLink 一并注入，缺省退化为原生 <a href>）。
  const link = (to: string, children: ReactNode) =>
    renderLink ? (
      renderLink({ to, className: DETAIL_LINK_CLASS, children })
    ) : (
      <a href={to} className={DETAIL_LINK_CLASS}>{children}</a>
    )
  return (
    <div className="space-y-2 bg-muted/30 px-3 pb-3 pl-10">
      {error && (
        <div>
          <p className="mb-1 text-[11px] font-medium text-destructive">{t('tasks.error')}</p>
          <pre className="overflow-x-auto rounded-md border border-destructive/40 bg-card p-2 font-mono text-[11px] whitespace-pre-wrap break-all text-destructive">
            {error}
          </pre>
          {isNetworkDownloadFailure(error) && (
            <div className="mt-1.5 flex flex-wrap items-center gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-2 text-[11px] text-amber-700 dark:text-amber-400">
              <Wifi className="size-3.5 shrink-0" />
              <span>{t('tasks.networkFailureHint', '出站网络不可达：下载源疑似被墙或超时。可配置出站代理或更换下载镜像源后重试。')}</span>
              {link('/settings', t('tasks.networkFailureProxyLink', '去配置出站代理'))}
              {link('/runtime-assets', t('tasks.networkFailureMirrorLink', '更换下载源/镜像'))}
            </div>
          )}
        </div>
      )}
      <div>
        <p className="mb-1 text-[11px] font-medium text-muted-foreground">{t('tasks.logs')}</p>
        {isLoading && logs.length === 0 ? (
          <p className="text-[11px] text-muted-foreground">{t('common.loading')}</p>
        ) : logs.length === 0 ? (
          <p className="text-[11px] text-muted-foreground">{t('tasks.noLogs')}</p>
        ) : (
          <pre className="max-h-64 overflow-auto rounded-md border bg-card p-2 font-mono text-[11px] whitespace-pre-wrap break-all">
            {logs.map((l) => l.line).join('\n')}
          </pre>
        )}
      </div>
    </div>
  )
}

export default TasksPageView
