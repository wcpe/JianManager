import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import { Ban, Loader2 } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Progress } from '@jianmanager/ui/components/progress'
import { TASK_KIND_LABEL_KEYS, type Task } from '@/lib/tasks/task-status'
import { activeTaskLabel, canForceStop } from '@/lib/nodes/node-active-tasks'

/**
 * 节点运行时页签顶部的「当前在跑什么」条。
 *
 * 【为什么必须有】改造前这里是空白：用户点了安装，页面什么都不显示，只能自己去
 * 任务中心翻。于是出现两种真实故障——① 以为没下发，反复点；② 想停掉重来，但找不到入口。
 * 这条把「有什么在跑 + 到哪了 + 直接停掉」放在**发起安装的同一屏**，不必跳转。
 *
 * 【为什么可以就地停而不只给链接】强制停止走的是既有 `/tasks/:id/cancel`
 * （running 在线 → CancelRequested → 心跳下发 cancel_task_ids 真中断 Worker 下载）。
 * 让用户为了停一个自己刚点错的任务而离开当前页去任务中心，是一次没有必要的跳转。
 */
export interface RuntimeActiveTaskBarProps {
  /** 本节点当前非终态的重型任务（jdk/runtime/pkg）。 */
  tasks: readonly Task[]
  /** 正在请求取消的任务 id 集合（按钮转圈用）。 */
  cancelingIds: readonly string[]
  onForceStop: (task: Task) => void
}

export function RuntimeActiveTaskBar({ tasks, cancelingIds, onForceStop }: RuntimeActiveTaskBarProps) {
  const { t } = useTranslation()
  if (tasks.length === 0) return null

  return (
    <div
      className="rounded-lg border border-status-info/30 bg-status-info/6 px-3 py-2"
      data-testid="runtime-active-task-bar"
    >
      {tasks.map((task) => {
        const canceling = cancelingIds.includes(task.taskId)
        const kindKey = TASK_KIND_LABEL_KEYS[task.kind]
        const kindLabel = kindKey ? t(kindKey, task.kind) : task.kind
        return (
          <div key={task.taskId} className="flex items-center gap-3 py-0.5" data-testid="runtime-active-task">
            <Loader2
              className={cn('size-3.5 shrink-0 text-status-info', !task.cancelRequested && 'animate-spin')}
              aria-hidden
            />
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline gap-2">
                <span className="truncate text-xs font-medium" title={task.title}>
                  {task.title}
                </span>
                <span className="shrink-0 text-[11px] text-muted-foreground">{kindLabel}</span>
              </div>
              <div className="mt-1 flex items-center gap-2">
                <Progress value={task.cancelRequested ? 100 : task.progress} className="h-1 flex-1" />
                <span
                  className="w-12 shrink-0 text-right text-[11px] tabular-nums text-muted-foreground"
                  data-testid={`runtime-active-pct-${task.taskId}`}
                >
                  {activeTaskLabel(task)}
                </span>
              </div>
              {task.detail && (
                <div className="mt-0.5 truncate text-[11px] text-muted-foreground" title={task.detail}>
                  {task.detail}
                </div>
              )}
            </div>
            {canForceStop(task) && (
              <Button
                variant="ghost"
                size="sm"
                className="h-7 shrink-0 px-2 text-[11px]"
                disabled={canceling}
                onClick={() => onForceStop(task)}
                data-testid={`runtime-active-stop-${task.taskId}`}
                title={t('nodes.runtime.forceStopHint', '中断正在下载/安装的任务，本节点随后可重新发起')}
              >
                {canceling ? <Loader2 className="mr-1 size-3 animate-spin" aria-hidden /> : <Ban className="mr-1 size-3" aria-hidden />}
                {canceling ? t('nodes.runtime.stopping', '停止中…') : t('tasks.forceStop', '强制停止')}
              </Button>
            )}
            {task.cancelRequested && !canceling && (
              <span className="shrink-0 text-[11px] text-muted-foreground">
                {t('nodes.runtime.waitingWorker', '等待节点确认')}
              </span>
            )}
          </div>
        )
      })}
    </div>
  )
}
