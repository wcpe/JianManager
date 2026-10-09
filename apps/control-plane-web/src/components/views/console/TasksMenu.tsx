import { useTranslation } from 'react-i18next'
import { Loader2, ListChecks } from 'lucide-react'

import { Badge } from '@jianmanager/ui/components/badge'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { cn } from '@jianmanager/ui'
import { isTerminalTask, TASK_KIND_LABEL_KEYS } from '@/lib/task-status'
import type { Task } from '@/lib/task-status'

/** 页眉任务下拉的行数上限（FR-327）：最近 N 条，看全量进任务中心页。 */
export const TASKS_MENU_MAX_ROWS = 8

/** 终态任务 → 徽章变体与文案键（tasks.state.*，与任务中心页同款语义）。 */
const TASK_BADGE_META: Record<string, { variant: 'secondary' | 'destructive' | 'outline'; key: string }> = {
  succeeded: { variant: 'secondary', key: 'tasks.state.succeeded' },
  failed: { variant: 'destructive', key: 'tasks.state.failed' },
  canceled: { variant: 'outline', key: 'tasks.state.canceled' },
}

export interface TasksMenuProps {
  /** 任务窗口（外壳取数；未加载传 undefined）。 */
  tasks?: Task[]
  /** 打开任务中心；带 taskId 时深链定位该任务（FR-226 `?task=`）。 */
  onOpenTask: (taskId?: string) => void
}

/**
 * 页眉任务中心入口 + 下拉面板（FR-327，下拉化 FR-226 的「点击直跳任务中心」）：
 * 入口常驻——有在跑任务（pending/running）时显示数量 + 平均进度（转圈反馈），空闲时静态图标；
 * 点击弹下拉面板列最近 N 条任务（kind 徽标/名称/stage 进度/终态徽章），点条目跳任务中心定位该任务
 * （FR-226 `?task=` 深链），底部「进入任务中心」看全量。面板外点击关闭为 DropdownMenu 缺省行为。
 * 数据/轮询由外壳复用 useTasks（FR-329：活跃 2s 短轮询、空闲停），本视图不取数、下拉不额外发请求。
 *
 * 受控视图（ADR-097 c 范式）：任务窗口与跳转由外壳注入。
 */
export function TasksMenu({ tasks: input, onOpenTask }: TasksMenuProps) {
  const { t } = useTranslation()
  // FR-337 分页信封：消费 items（首窗 ≤100，active 计数/最近 N 条口径与改前一致）。
  const tasks = input ?? []
  const active = tasks.filter((tk) => !isTerminalTask(tk))
  const recent = tasks.slice(0, TASKS_MENU_MAX_ROWS)
  const avg = active.length > 0 ? Math.round(active.reduce((s, tk) => s + tk.progress, 0) / active.length) : 0

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          title={active.length > 0 ? t('header.tasksRunning', { count: active.length, progress: avg }) : t('header.tasks')}
          aria-label={t('header.tasks')}
          className={cn(
            'flex items-center gap-1.5 rounded-md px-1.5 py-1 text-xs transition-colors hover:bg-accent/60',
            active.length > 0 ? 'text-primary' : 'text-muted-foreground hover:text-foreground',
          )}
        >
          {active.length > 0 ? (
            <>
              <Loader2 className="size-3.5 animate-spin" />
              <span className="font-medium tabular-nums">{active.length}</span>
              <span className="tabular-nums text-muted-foreground">{avg}%</span>
            </>
          ) : (
            <ListChecks className="size-4" />
          )}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <div className="flex items-center justify-between px-2 py-1.5 text-xs font-medium">
          <span>{t('header.tasks')}</span>
          {active.length > 0 && (
            <span className="text-muted-foreground">{t('header.tasksActiveCount', { count: active.length })}</span>
          )}
        </div>
        <DropdownMenuSeparator />
        {recent.length === 0 ? (
          <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('tasks.empty')}</div>
        ) : (
          // 内容自适应 + 超高内部滚动（ui-modals 纪律：禁固定尺寸溢出）。
          <div className="max-h-72 overflow-y-auto">
            {recent.map((task) => (
              <TaskMenuRow key={task.taskId} task={task} onOpen={() => onOpenTask(task.taskId)} />
            ))}
          </div>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={() => onOpenTask()} className="justify-center text-xs text-muted-foreground">
          {t('header.viewAllTasks')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * 页眉任务下拉单行（FR-327）：kind 徽标 + 标题 + 进行中进度条（% 数值）/终态徽章 + stage 详情。
 * 点击跳任务中心并深链定位该任务（`/tasks?task=<taskId>`，进页自动展开，FR-226）。
 */
function TaskMenuRow({ task, onOpen }: { task: Task; onOpen: () => void }) {
  const { t } = useTranslation()
  const kindKey = TASK_KIND_LABEL_KEYS[task.kind]
  // 取消中（已请求停止但 Worker 未确认）优先于状态徽章，语义与任务中心页一致（FR-227）。
  const canceling = task.state === 'running' && task.cancelRequested
  const badge = TASK_BADGE_META[task.state]
  const pct = Math.max(0, Math.min(100, task.progress))
  return (
    <DropdownMenuItem onClick={onOpen} className="items-start text-xs">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span className="shrink-0 rounded bg-primary/10 px-1 py-px text-[10px] font-medium text-primary">
            {kindKey ? t(kindKey) : task.kind}
          </span>
          <span className="min-w-0 flex-1 truncate text-foreground">{task.title || task.kind}</span>
          {canceling ? (
            <Badge variant="outline">{t('tasks.state.canceling', '取消中')}</Badge>
          ) : (
            badge && <Badge variant={badge.variant}>{t(badge.key)}</Badge>
          )}
        </div>
        {!isTerminalTask(task) && !canceling && (
          <div className="mt-1 flex items-center gap-2">
            <div className="h-1 min-w-0 flex-1 overflow-hidden rounded-full bg-muted">
              <div className="h-full rounded-full bg-primary transition-all" style={{ width: `${pct}%` }} />
            </div>
            <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground">{pct}%</span>
          </div>
        )}
        {task.detail && <p className="mt-0.5 truncate text-[11px] text-muted-foreground">{task.detail}</p>}
      </div>
    </DropdownMenuItem>
  )
}
