import { useLayoutEffect, useMemo, useRef, useState, type Ref } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useSearchParams } from 'react-router'
import { ChevronRight, Ban, Loader2, Wifi } from 'lucide-react'
import { isNetworkDownloadFailure } from '@/lib/download-failure'
import { useVirtualRows } from '@jianmanager/biz-views/lib/virtual-list'
import { useTasks, useTask, useCancelTask, isTerminalTask, TASK_KIND_LABEL_KEYS, type Task, type TaskState } from '@/api/tasks'
import { useNodes } from '@/api/nodes'
import { Badge } from '@jianmanager/ui/components/badge'
import { Panel } from '@jianmanager/ui/components/panel'
import { ListSkeleton, PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import DangerConfirm from '@/components/DangerConfirm'
import { toast } from 'sonner'
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

/** 时间快捷筛选 → 创建时间下界（RFC3339）。在事件处理器中调用（避免 render 里的不纯 Date.now）。 */
function sinceFromFilter(v: string): string | undefined {
  if (v === '24h') return new Date(Date.now() - 86_400_000).toISOString()
  if (v === '7d') return new Date(Date.now() - 7 * 86_400_000).toISOString()
  return undefined
}

/** 任务列表增长窗口（FR-337）：初始/步长 100，封顶 500（到顶引导用筛选缩小范围）。 */
const TASKS_WINDOW_STEP = 100
const TASKS_WINDOW_MAX = 500

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

/**
 * 全局任务中心页（FR-183 + FR-227 + FR-337）。
 * 轮询 `/tasks` 列长任务（如 JDK 安装）：进度条 + 状态徽标 + 展开看滚动日志。
 * 进行中任务可「强制停止」（FR-227：经心跳真中断 Worker 操作）；列表按 kind/state/node/关键词/时间筛选。
 * 分页信封（FR-337）：顶部「共 N 条 · 已加载 M」，「加载更多」扩大 limit 的增长窗口（offset 恒 0），
 * 任一筛选变化时窗口复位；轮询重取整个已加载窗口，进度与总数同步刷新。
 * 存在进行中任务时自动短轮询刷新；全部终态时停止。非平台管理员只见自己发起的任务（后端收敛）。
 */
export default function TasksPage() {
  const { t } = useTranslation()
  // 通知中心/页眉跳转深链（FR-226）：?task=<taskId> 进页即自动展开该任务，定位上下文。
  const [searchParams] = useSearchParams()
  const [expanded, setExpanded] = useState<string | null>(() => searchParams.get('task'))

  // 筛选条件（FR-227）。
  const [stateF, setStateF] = useState('')
  const [kindF, setKindF] = useState('')
  const [nodeF, setNodeF] = useState('')
  const [keyword, setKeyword] = useState('')
  const [timeF, setTimeF] = useState('') // ''=全部 | 24h | 7d
  const [since, setSince] = useState<string | undefined>(undefined)
  // 增长窗口（FR-337）：「加载更多」+100 封顶 500；筛选变化复位。
  const [limit, setLimit] = useState(TASKS_WINDOW_STEP)

  /** 包装筛选 setter：任一筛选变化时窗口复位（FR-337），避免停留在放大的窗口。 */
  const withWindowReset = <T,>(set: (v: T) => void) => (v: T) => {
    setLimit(TASKS_WINDOW_STEP)
    set(v)
  }

  const { data: nodes } = useNodes()
  const { data: page, isLoading, isError } = useTasks({
    state: (stateF as TaskState) || '',
    kind: kindF,
    nodeId: nodeF ? Number(nodeF) : undefined,
    keyword,
    since,
    limit,
  })
  const tasks = useMemo(() => page?.items ?? [], [page])
  const total = page?.total ?? 0
  const canLoadMore = tasks.length < total && limit < TASKS_WINDOW_MAX
  const windowCapped = tasks.length < total && limit >= TASKS_WINDOW_MAX

  // 视口裁剪（FR-496 阶段 6）：增长窗口最大 500 行，一次性铺进 DOM 是本页的主开销。
  // 展开行的错误正文 + 日志长短不定（估算误差会让其后所有行偏移错位），故实测后回填。
  const expandedRef = useRef<HTMLDivElement>(null)
  const [expandedSize, setExpandedSize] = useState<{ id: string; px: number } | null>(null)
  const expandedPx = expandedSize && expandedSize.id === expanded ? expandedSize.px : TASK_ROW_EXPANDED_PX
  useLayoutEffect(() => {
    // jsdom 无布局：offsetHeight 恒 0 → 保留估算，测试里窗口完全确定。
    const px = expandedRef.current?.offsetHeight ?? 0
    if (expanded !== null && px > 0 && px !== expandedPx) setExpandedSize({ id: expanded, px })
  }, [expanded, expandedPx, tasks])

  /** 逐行高度：折叠行按「一行/两行」估算，展开行用实测值 → 偏移前缀和贴近真实布局。 */
  const rowSizes = useMemo(
    () =>
      tasks.map((task) =>
        task.taskId === expanded
          ? expandedPx
          : TASK_ROW_BASE_PX + (task.detail ? TASK_ROW_EXTRA_LINE_PX : 0),
      ),
    [expanded, expandedPx, tasks],
  )
  const { containerRef, onScroll, range } = useVirtualRows({
    total: tasks.length,
    itemSize: TASK_ROW_BASE_PX,
    overscan: 6,
    fallbackViewportSize: FALLBACK_VIEWPORT_PX,
    sizes: rowSizes,
  })

  const hasFilters = !!(stateF || kindF || nodeF || keyword || timeF)
  const resetFilters = () => {
    setStateF(''); setKindF(''); setNodeF(''); setKeyword(''); setTimeF(''); setSince(undefined)
    setLimit(TASKS_WINDOW_STEP)
  }

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语（PageShell / PageHeader）。
    // 计数说明（「已加载 X / 共 Y」）是文字而非纯数字，故走 description 而不是 count。
    <PageShell data-page="tasks">
      <PageHeader
        title={t('tasks.title')}
        description={page != null && !isError ? t('tasks.countSummary', { total, loaded: tasks.length }) : undefined}
      />

      {/* 筛选条（FR-227） */}
      <div className="jm-toolbar-surface flex flex-wrap items-center gap-2 p-2">
        <Input
          value={keyword}
          onChange={(e) => withWindowReset(setKeyword)(e.target.value)}
          placeholder={t('tasks.filter.keyword', '搜索标题 / 详情')}
          className="h-8 w-52"
        />
        <FilterSelect value={stateF} onChange={withWindowReset(setStateF)} placeholder={t('tasks.filter.allStates', '全部状态')}
          options={STATE_OPTIONS.map((s) => ({ value: s, label: t(STATE_META[s].key) }))} />
        <FilterSelect value={kindF} onChange={withWindowReset(setKindF)} placeholder={t('tasks.filter.allKinds', '全部种类')}
          options={Object.entries(TASK_KIND_LABEL_KEYS).map(([value, key]) => ({ value, label: t(key) }))} />
        <FilterSelect value={nodeF} onChange={withWindowReset(setNodeF)} placeholder={t('tasks.filter.allNodes', '全部节点')}
          options={(nodes ?? []).map((n) => ({ value: String(n.id), label: n.name }))} />
        <FilterSelect value={timeF} onChange={withWindowReset((v: string) => { setTimeF(v); setSince(sinceFromFilter(v)) })} placeholder={t('tasks.filter.allTime', '全部时间')}
          options={[{ value: '24h', label: t('tasks.filter.last24h', '近 24 小时') }, { value: '7d', label: t('tasks.filter.last7d', '近 7 天') }]} />
        {hasFilters && (
          <Button variant="ghost" size="sm" onClick={resetFilters} className="text-muted-foreground">
            {t('common.reset', '重置')}
          </Button>
        )}
      </div>

      {/* FR-496 阶段 6 补丁：数据未到时不再只给一行「加载中」。页头与筛选条先渲染，
          数据区用同壳骨架（同一张 Panel + 同一条吸附列头 + 行占位）顶上，数据到达原地替换。 */}
      {isLoading && !page ? (
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
                    open={expanded === task.taskId}
                    isLast={index === tasks.length - 1}
                    rowRef={expanded === task.taskId ? expandedRef : undefined}
                    onToggle={() => setExpanded((id) => (id === task.taskId ? null : task.taskId))}
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
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setLimit((l) => Math.min(l + TASKS_WINDOW_STEP, TASKS_WINDOW_MAX))}
                >
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

/** 单条任务行：进度条 + 状态徽标 + 强制停止；点击展开看日志（详情懒查）。 */
function TaskRow({
  task,
  open,
  isLast,
  rowRef,
  onToggle,
}: {
  task: Task
  open: boolean
  /** 末行不画分隔线：虚拟窗口下 `last:` 变体只看「已渲染的最后一个」，会误伤窗口末行。 */
  isLast: boolean
  /** 展开行挂给页面实测高度（仅展开时传入）。 */
  rowRef?: Ref<HTMLDivElement>
  onToggle: () => void
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
          {!isTerminalTask(task) && <CancelTaskButton task={task} />}
        </span>
      </div>
      {open && <TaskDetail taskId={task.taskId} error={task.error} />}
    </div>
  )
}

/** 强制停止按钮（FR-227）：二次确认后调取消端点，经心跳真中断 Worker 操作。 */
function CancelTaskButton({ task }: { task: Task }) {
  const { t } = useTranslation()
  const cancel = useCancelTask()
  const [confirm, setConfirm] = useState(false)
  return (
    <>
      <Button
        variant="ghost"
        size="sm"
        className="h-7 px-2 text-destructive hover:bg-destructive/10 hover:text-destructive"
        disabled={cancel.isPending || task.cancelRequested}
        onClick={() => setConfirm(true)}
        title={t('tasks.forceStop', '强制停止')}
      >
        {cancel.isPending ? <Loader2 className="size-3.5 animate-spin" /> : <Ban className="size-3.5" />}
      </Button>
      <DangerConfirm
        open={confirm}
        title={t('tasks.forceStopTitle', '强制停止任务?')}
        description={t('tasks.forceStopDesc', '将中断该任务在节点上的执行（如下载会被取消、临时文件清理），不可恢复。')}
        confirmLabel={t('tasks.forceStop', '强制停止')}
        onCancel={() => setConfirm(false)}
        onConfirm={() => {
          setConfirm(false)
          cancel.mutate(task.taskId, {
            onSuccess: () => toast.success(t('tasks.cancelRequested', '已请求停止')),
            onError: (err: Error & { response?: { data?: { message?: string } } }) =>
              toast.error(err.response?.data?.message || t('tasks.cancelFailed', '停止失败')),
          })
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

/** 任务详情：懒查日志 + 错误（行展开时才拉，进行中短轮询）。 */
function TaskDetail({ taskId, error }: { taskId: string; error: string }) {
  const { t } = useTranslation()
  const { data, isLoading } = useTask(taskId)
  const logs = data?.logs ?? []
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
              <Link to="/settings" className="font-medium underline underline-offset-2 hover:text-amber-800 dark:hover:text-amber-300">
                {t('tasks.networkFailureProxyLink', '去配置出站代理')}
              </Link>
              <Link to="/runtime-assets" className="font-medium underline underline-offset-2 hover:text-amber-800 dark:hover:text-amber-300">
                {t('tasks.networkFailureMirrorLink', '更换下载源/镜像')}
              </Link>
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
