import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'
import { ChevronRight, Ban, Loader2 } from 'lucide-react'
import { useTasks, useTask, useCancelTask, isTerminalTask, type Task, type TaskState } from '@/api/tasks'
import { useNodes } from '@/api/nodes'
import { Badge } from '@jianmanager/ui/components/badge'
import { Panel } from '@jianmanager/ui/components/panel'
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

/**
 * 全局任务中心页（FR-183 + FR-227）。
 * 轮询 `/tasks` 列长任务（如 JDK 安装）：进度条 + 状态徽标 + 展开看滚动日志。
 * 进行中任务可「强制停止」（FR-227：经心跳真中断 Worker 操作）；列表按 kind/state/node/关键词/时间筛选。
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

  const { data: nodes } = useNodes()
  const { data: tasks, isLoading, isError } = useTasks({
    state: (stateF as TaskState) || '',
    kind: kindF,
    nodeId: nodeF ? Number(nodeF) : undefined,
    keyword,
    since,
  })

  const hasFilters = !!(stateF || kindF || nodeF || keyword || timeF)
  const resetFilters = () => { setStateF(''); setKindF(''); setNodeF(''); setKeyword(''); setTimeF(''); setSince(undefined) }

  return (
    <div data-page="tasks" className="jm-page-stack space-y-4">
      <div className="jm-page-header">
        <h1 className="jm-page-title">{t('tasks.title')}</h1>
      </div>

      {/* 筛选条（FR-227） */}
      <div className="jm-toolbar-surface flex flex-wrap items-center gap-2 p-2">
        <Input
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          placeholder={t('tasks.filter.keyword', '搜索标题 / 详情')}
          className="h-8 w-52"
        />
        <FilterSelect value={stateF} onChange={setStateF} placeholder={t('tasks.filter.allStates', '全部状态')}
          options={STATE_OPTIONS.map((s) => ({ value: s, label: t(STATE_META[s].key) }))} />
        <FilterSelect value={kindF} onChange={setKindF} placeholder={t('tasks.filter.allKinds', '全部种类')}
          options={[{ value: 'jdk_install', label: t('tasks.kind.jdkInstall', 'JDK 安装') }]} />
        <FilterSelect value={nodeF} onChange={setNodeF} placeholder={t('tasks.filter.allNodes', '全部节点')}
          options={(nodes ?? []).map((n) => ({ value: String(n.id), label: n.name }))} />
        <FilterSelect value={timeF} onChange={(v) => { setTimeF(v); setSince(sinceFromFilter(v)) }} placeholder={t('tasks.filter.allTime', '全部时间')}
          options={[{ value: '24h', label: t('tasks.filter.last24h', '近 24 小时') }, { value: '7d', label: t('tasks.filter.last7d', '近 7 天') }]} />
        {hasFilters && (
          <Button variant="ghost" size="sm" onClick={resetFilters} className="text-muted-foreground">
            {t('common.reset', '重置')}
          </Button>
        )}
      </div>

      {isLoading && !tasks ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        <p className="text-destructive">{t('tasks.loadError')}</p>
      ) : !tasks || tasks.length === 0 ? (
        <Panel>
          <p className="px-3 py-10 text-center text-sm text-muted-foreground">
            {hasFilters ? t('tasks.emptyFiltered', '没有匹配筛选条件的任务') : t('tasks.empty')}
          </p>
        </Panel>
      ) : (
        <Panel bodyClassName="p-0">
          <div className="flex items-center gap-3 border-b bg-muted/40 px-3 py-2 text-[11px] font-medium text-muted-foreground">
            <span className="w-4 shrink-0" />
            <span className="min-w-0 flex-1">{t('tasks.task')}</span>
            <span className="w-24 shrink-0">{t('tasks.stateLabel')}</span>
            <span className="w-40 shrink-0">{t('tasks.progress')}</span>
            <span className="w-40 shrink-0">{t('tasks.updatedAt')}</span>
            <span className="w-20 shrink-0" />
          </div>
          {tasks.map((task) => (
            <TaskRow
              key={task.taskId}
              task={task}
              open={expanded === task.taskId}
              onToggle={() => setExpanded((id) => (id === task.taskId ? null : task.taskId))}
            />
          ))}
        </Panel>
      )}
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
function TaskRow({ task, open, onToggle }: { task: Task; open: boolean; onToggle: () => void }) {
  const { t } = useTranslation()
  // 在线 running 已请求取消 → 显「取消中」；否则按状态。
  const canceling = task.state === 'running' && task.cancelRequested
  const badge = canceling
    ? { variant: 'outline' as const, label: t('tasks.state.canceling', '取消中') }
    : { variant: STATE_META[task.state].variant, label: t(STATE_META[task.state].key) }
  return (
    <div className="border-b border-border/60 last:border-b-0">
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
