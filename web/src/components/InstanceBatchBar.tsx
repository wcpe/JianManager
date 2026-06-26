import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useInstanceBatch, type InstanceBatchAction, type InstanceBatchResult } from '@/api/instances'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

/** 实例批量操作栏的属性（FR-058）。 */
interface InstanceBatchBarProps {
  /** 当前选中的实例 ID 列表。 */
  selectedIds: number[]
  /** 操作完成后清空选择的回调。 */
  onClear: () => void
}

/** 危险动作（需输入关键字二次确认）。 */
const DANGER_ACTIONS: InstanceBatchAction[] = ['kill']
/** 需要普通二次确认的动作。 */
const CONFIRM_ACTIONS: InstanceBatchAction[] = ['stop', 'kill']

/**
 * 实例批量操作栏：命令下发 + 启停/重启/强制关服。
 * 危险操作（kill）要求输入关键字二次确认（FR-058 / FR-059）。
 */
export default function InstanceBatchBar({ selectedIds, onClear }: InstanceBatchBarProps) {
  const { t } = useTranslation()
  const batch = useInstanceBatch()
  const [command, setCommand] = useState('')
  const [pending, setPending] = useState<InstanceBatchAction | null>(null)
  const [keyword, setKeyword] = useState('')

  const count = selectedIds.length
  const confirmKeyword = t('instanceBatch.confirmKeyword')

  const run = (action: InstanceBatchAction) => {
    if (count === 0) {
      toast.error(t('instanceBatch.noTarget'))
      return
    }
    const payload =
      action === 'command'
        ? { action, ids: selectedIds, command }
        : { action, ids: selectedIds }
    batch.mutate(payload, {
      onSuccess: (res: InstanceBatchResult) => {
        toast.success(
          t('instanceBatch.result', {
            succeeded: res.succeeded,
            failed: res.failed,
            skipped: res.skipped,
          }),
        )
        if (action === 'command') setCommand('')
        onClear()
      },
      onError: (err: Error & { response?: { data?: { message?: string } } }) => {
        toast.error(err.response?.data?.message || t('instanceBatch.failed'))
      },
    })
  }

  /** 点击动作：危险/需确认的弹窗确认，否则直接执行。 */
  const handleAction = (action: InstanceBatchAction) => {
    if (action === 'command' && command.trim() === '') {
      toast.error(t('instanceBatch.needCommand'))
      return
    }
    // 命令下发与停止/强杀一律先复述确认（FR-139）：避免误向多实例下发或批量强停。
    if (action === 'command' || CONFIRM_ACTIONS.includes(action)) {
      setKeyword('')
      setPending(action)
      return
    }
    run(action)
  }

  const isDanger = pending !== null && DANGER_ACTIONS.includes(pending)
  // 危险操作要求输入关键字匹配后才放行（kill）。
  const confirmDisabled = isDanger && keyword !== confirmKeyword

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/30 p-3">
      <span className="text-sm text-muted-foreground">
        {t('instanceBatch.selected', { count })}
      </span>
      <Button variant="ghost" size="sm" onClick={onClear} disabled={count === 0}>
        {t('instanceBatch.clear')}
      </Button>

      <div className="mx-2 h-5 w-px bg-border" />

      <Input
        value={command}
        onChange={(e) => setCommand(e.target.value)}
        placeholder={t('instanceBatch.commandPlaceholder')}
        className="h-8 w-72 max-w-full"
      />
      <Button
        variant="secondary"
        size="sm"
        onClick={() => handleAction('command')}
        disabled={batch.isPending || count === 0}
      >
        {t('instanceBatch.command')}
      </Button>

      <div className="mx-2 h-5 w-px bg-border" />

      <Button variant="outline" size="sm" onClick={() => handleAction('start')} disabled={batch.isPending || count === 0}>
        {t('instanceBatch.start')}
      </Button>
      <Button variant="outline" size="sm" onClick={() => handleAction('restart')} disabled={batch.isPending || count === 0}>
        {t('instanceBatch.restart')}
      </Button>
      <Button variant="outline" size="sm" onClick={() => handleAction('stop')} disabled={batch.isPending || count === 0}>
        {t('instanceBatch.stop')}
      </Button>
      <Button variant="destructive" size="sm" onClick={() => handleAction('kill')} disabled={batch.isPending || count === 0}>
        {t('instanceBatch.kill')}
      </Button>

      <Dialog open={pending !== null} onOpenChange={(v) => { if (!v) setPending(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {pending === 'kill'
                ? t('instanceBatch.confirmKillTitle')
                : pending === 'command'
                  ? t('instanceBatch.confirmCommandTitle')
                  : t('instanceBatch.confirmStopTitle')}
            </DialogTitle>
            <DialogDescription>
              {pending === 'kill'
                ? t('instanceBatch.confirmKillDesc', { count })
                : pending === 'command'
                  ? t('instanceBatch.confirmCommandDesc', { count, command })
                  : t('instanceBatch.confirmStopDesc', { count })}
            </DialogDescription>
          </DialogHeader>
          {isDanger && (
            <div className="space-y-1">
              <p className="text-sm text-muted-foreground">
                {t('instanceBatch.confirmKillTypeHint', { keyword: confirmKeyword })}
              </p>
              <Input value={keyword} onChange={(e) => setKeyword(e.target.value)} placeholder={confirmKeyword} />
            </div>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setPending(null)}>
              {t('instanceBatch.cancel')}
            </Button>
            <Button
              variant="destructive"
              disabled={confirmDisabled}
              onClick={() => {
                const action = pending
                setPending(null)
                if (action) run(action)
              }}
            >
              {t('instanceBatch.run')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
