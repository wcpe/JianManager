import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Database } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'

/**
 * 迁移日志分区（HOT → COLD）。
 *
 * 【为什么必须是独立对话框而不是一个内联按钮】分区迁移**不可逆**：它把某个物理 UTC 日
 * 从热存储搬到 COLD，搬错日期/节点没有回退路径。改造前这个危险动作用和其它按钮
 * 同样尺寸的 outline 按钮挂在页面底部，靠「点了再弹确认」兜底——用户是先点、后看到后果。
 *
 * 这里的三个设计：
 *  ① 目标（节点 + UTC 日）先固定成快照再确认，确认框里显示的是**将要执行的**目标，
 *    不会因为用户切了节点而漂移；
 *  ② 失败原因**留在框内**而不是只在 toast 里闪一下——409（目标已迁移）/502（上游挂了）
 *    处置完全不同，用户需要对着错误重试；
 *  ③ 提交在途禁止重复点击（migrationInFlight），避免双击搬两次。
 */
export interface LogMigrateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  nodeId: number
  pending?: boolean
  canMigrate?: boolean
  /** 成功返回 null；失败返回可展示的错误文案（保留在框内供重试）。 */
  onConfirm: (utcDay: string) => Promise<string | null>
}

export default function LogMigrateDialog({
  open,
  onOpenChange,
  nodeId,
  pending = false,
  canMigrate = true,
  onConfirm,
}: LogMigrateDialogProps) {
  const { t } = useTranslation()
  const [day, setDay] = useState('')
  const [error, setError] = useState('')
  /*
    在途标记用 state 而不是 ref：它要被渲染读取（禁用按钮、拦住重复提交），
    而渲染期读 ref 会拿不到最新值、也违反 react-hooks/refs 规则。
    真正的防重入仍靠 `submitting` 判断——state 更新是异步的，所以在事件处理里
    先同步置位再 await 也足够（同 tick 内的第二次点击走不到这里）。
  */
  const [submitting, setSubmitting] = useState(false)

  /*
    错误与日期都随「每次新开一次对话框」重置：调用方用 key={open ? 'open' : 'closed'} 重新挂载，
    比「在 effect 里清 state」少一次渲染，也不会在打开瞬间闪一下上一次的失败原因。
  */

  const submit = async () => {
    if (!day || submitting) return
    setSubmitting(true)
    setError('')
    try {
      const err = await onConfirm(day)
      if (err === null) onOpenChange(false)
      else setError(err)
    } finally {
      setSubmitting(false)
    }
  }

  // 默认取「昨天（UTC）」：当天分区通常还在写入，迁它是反常操作，不该做默认值。
  const yesterdayUtc = (() => {
    const d = new Date()
    d.setUTCDate(d.getUTCDate() - 1)
    return d.toISOString().slice(0, 10)
  })()

  return (
    <Dialog open={open} onOpenChange={(o) => (submitting ? undefined : onOpenChange(o))}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Database className="size-4" />
            {t('logsRuntime.migration.title', '确认迁移日志分区')}
          </DialogTitle>
          <DialogDescription>
            {t(
              'logsRuntime.migration.description',
              '节点 {{node}} · 存储命名空间 {{namespace}} · UTC 日 {{day}}。将把该物理日的 HOT 日志迁至 COLD，过程可能影响查询，请核对目标。',
              { node: nodeId, namespace: `node:${nodeId}`, day: day || '—' },
            )}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          <label className="block space-y-1">
            <span className="text-xs font-medium">{t('logsRuntime.date.label', '迁移日期（该日分区）')}</span>
            <Input
              type="date"
              value={day}
              max={yesterdayUtc}
              onChange={(e) => setDay(e.target.value)}
              aria-label={t('logsRuntime.date.label', '迁移日期（该日分区）')}
            />
            <span className="block text-[10px] text-muted-foreground">
              {t('logsRuntime.date.help', '要迁移的分区归属日（UTC）。日志按 UTC 日期分区存储。')}
            </span>
          </label>
          {!canMigrate && (
            <p className="text-[11px] text-status-warning">{t('logsRuntime.migration.denied', '整日跨命名空间迁移仅平台管理员可用')}</p>
          )}
          {error && (
            <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-2 py-1.5 text-[11px] text-destructive">
              {error}
            </p>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={submitting || pending}>
            {t('common.cancel', '取消')}
          </Button>
          <Button onClick={() => void submit()} disabled={!day || !canMigrate || pending || submitting}>
            {t('logsRuntime.migration.confirm', '确认迁移')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
