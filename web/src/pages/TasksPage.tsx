import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight } from 'lucide-react'
import { useTasks, useTask, isTerminalTask, type Task, type TaskState } from '@/api/tasks'
import { Badge } from '@/components/ui/badge'
import { Panel } from '@/components/ui/panel'
import { cn } from '@/lib/utils'

/** 任务状态 → Badge 变体与文案键。 */
const STATE_META: Record<TaskState, { variant: 'default' | 'secondary' | 'destructive' | 'outline'; key: string }> = {
  pending: { variant: 'outline', key: 'tasks.state.pending' },
  running: { variant: 'default', key: 'tasks.state.running' },
  succeeded: { variant: 'secondary', key: 'tasks.state.succeeded' },
  failed: { variant: 'destructive', key: 'tasks.state.failed' },
}

/**
 * 全局任务中心页（FR-183，见 ADR-040）。
 * 轮询 `/tasks` 列长任务（如 JDK 安装）：进度条 + 状态徽标 + 展开看滚动日志。
 * 存在进行中任务时自动短轮询刷新；全部终态时停止。非平台管理员只见自己发起的任务（后端收敛）。
 */
export default function TasksPage() {
  const { t } = useTranslation()
  const { data: tasks, isLoading, isError } = useTasks()
  const [expanded, setExpanded] = useState<string | null>(null)

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">{t('tasks.title')}</h1>
      </div>

      {isLoading && !tasks ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        <p className="text-destructive">{t('tasks.loadError')}</p>
      ) : !tasks || tasks.length === 0 ? (
        <Panel>
          <p className="px-3 py-10 text-center text-sm text-muted-foreground">{t('tasks.empty')}</p>
        </Panel>
      ) : (
        <Panel bodyClassName="p-0">
          <div className="flex items-center gap-3 border-b bg-muted/40 px-3 py-2 text-[11px] font-medium text-muted-foreground">
            <span className="w-4 shrink-0" />
            <span className="min-w-0 flex-1">{t('tasks.task')}</span>
            <span className="w-24 shrink-0">{t('tasks.stateLabel')}</span>
            <span className="w-40 shrink-0">{t('tasks.progress')}</span>
            <span className="w-40 shrink-0">{t('tasks.updatedAt')}</span>
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

/** 单条任务行：进度条 + 状态徽标；点击展开看日志（详情懒查）。 */
function TaskRow({ task, open, onToggle }: { task: Task; open: boolean; onToggle: () => void }) {
  const { t } = useTranslation()
  const meta = STATE_META[task.state]
  return (
    <div className="border-b border-border/60 last:border-b-0">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-center gap-3 px-3 py-2.5 text-left text-sm transition-colors hover:bg-accent/50"
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
          <Badge variant={meta.variant}>{t(meta.key)}</Badge>
        </span>
        <span className="w-40 shrink-0">
          <ProgressBar value={task.progress} terminal={isTerminalTask(task)} failed={task.state === 'failed'} />
        </span>
        <span className="w-40 shrink-0 font-mono text-[11px] text-muted-foreground">
          {new Date(task.updatedAt).toLocaleString()}
        </span>
      </button>
      {open && <TaskDetail taskId={task.taskId} error={task.error} />}
    </div>
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
