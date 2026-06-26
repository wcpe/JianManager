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

/** 批量栏所需的选中实例最小信息（含状态，用于状态感知禁用与失败明细，FR-058/FR-139）。 */
export interface BatchSelectedInstance {
  id: number
  name: string
  status: string
}

/** 实例批量操作栏的属性（FR-058）。 */
interface InstanceBatchBarProps {
  /** 当前选中的实例（含状态）。 */
  selected: BatchSelectedInstance[]
  /** 操作完成后清空选择的回调。 */
  onClear: () => void
  /** 部分失败后仅保留失败实例的选择，供重试（FR-139）。 */
  onRetainFailed: (ids: number[]) => void
}

/** 危险动作（需输入关键字二次确认）。 */
const DANGER_ACTIONS: InstanceBatchAction[] = ['kill']
/** 需要弹窗二次确认的动作（停止/强杀复述、命令复述）。 */
const CONFIRM_ACTIONS: InstanceBatchAction[] = ['stop', 'kill']

const STARTABLE = ['STOPPED', 'CRASHED']
const KILLABLE = ['RUNNING', 'STARTING', 'STOPPING']

/**
 * 实例批量操作栏：命令下发 + 启停/重启/强制关服。
 * 危险操作（kill）要求输入关键字二次确认（FR-058 / FR-059）；停止与命令下发复述确认（FR-139）。
 * 按选中集状态分布做动作禁用 + 原因 tooltip；部分失败列明细并保留失败项选择供重试（FR-139）。
 */
export default function InstanceBatchBar({ selected, onClear, onRetainFailed }: InstanceBatchBarProps) {
  const { t } = useTranslation()
  const batch = useInstanceBatch()
  const [command, setCommand] = useState('')
  const [pending, setPending] = useState<InstanceBatchAction | null>(null)
  const [keyword, setKeyword] = useState('')
  const [failures, setFailures] = useState<{ name: string; error: string }[] | null>(null)

  const selectedIds = selected.map((s) => s.id)
  const count = selectedIds.length
  const confirmKeyword = t('instanceBatch.confirmKeyword')

  // 状态感知：所选集合里有无对应可操作实例，决定各动作是否可用（FR-139）。
  const hasStartable = selected.some((s) => STARTABLE.includes(s.status))
  const hasRunning = selected.some((s) => s.status === 'RUNNING')
  const hasKillable = selected.some((s) => KILLABLE.includes(s.status))

  const run = (action: InstanceBatchAction) => {
    if (count === 0) {
      toast.error(t('instanceBatch.noTarget'))
      return
    }
    const payload =
      action === 'command' ? { action, ids: selectedIds, command } : { action, ids: selectedIds }
    batch.mutate(payload, {
      onSuccess: (res: InstanceBatchResult) => {
        const errs = res.errors ?? []
        if (errs.length > 0) {
          // 部分/全部失败：列明细 + 仅保留失败项选择供重试，不清空（FR-139）。
          toast.warning(
            t('instanceBatch.partial', { succeeded: res.succeeded, failed: res.failed, skipped: res.skipped }),
          )
          const nameOf = (id: number) => selected.find((s) => s.id === id)?.name ?? `#${id}`
          setFailures(errs.map((e) => ({ name: nameOf(e.instanceId), error: e.error })))
          onRetainFailed(errs.map((e) => e.instanceId))
        } else {
          toast.success(
            t('instanceBatch.result', { succeeded: res.succeeded, failed: res.failed, skipped: res.skipped }),
          )
          if (action === 'command') setCommand('')
          onClear()
        }
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

  /** 动作不可用的原因（状态感知禁用，返回 null 表示可用）。 */
  const disabledReason = (action: InstanceBatchAction): string | null => {
    if (count === 0) return t('instanceBatch.selectFirst')
    if (action === 'command' && !hasRunning) return t('instanceBatch.noRunning')
    if (action === 'start' && !hasStartable) return t('instanceBatch.noStartable')
    if ((action === 'stop' || action === 'restart') && !hasRunning) return t('instanceBatch.noRunning')
    if (action === 'kill' && !hasKillable) return t('instanceBatch.noKillable')
    return null
  }

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/30 p-3">
      <span className="text-sm text-muted-foreground">{t('instanceBatch.selected', { count })}</span>
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
        title={disabledReason('command') ?? undefined}
        disabled={batch.isPending || disabledReason('command') !== null}
        onClick={() => handleAction('command')}
      >
        {t('instanceBatch.command')}
      </Button>

      <div className="mx-2 h-5 w-px bg-border" />

      <Button
        variant="outline"
        size="sm"
        title={disabledReason('start') ?? undefined}
        disabled={batch.isPending || disabledReason('start') !== null}
        onClick={() => handleAction('start')}
      >
        {t('instanceBatch.start')}
      </Button>
      <Button
        variant="outline"
        size="sm"
        title={disabledReason('restart') ?? undefined}
        disabled={batch.isPending || disabledReason('restart') !== null}
        onClick={() => handleAction('restart')}
      >
        {t('instanceBatch.restart')}
      </Button>
      <Button
        variant="outline"
        size="sm"
        title={disabledReason('stop') ?? undefined}
        disabled={batch.isPending || disabledReason('stop') !== null}
        onClick={() => handleAction('stop')}
      >
        {t('instanceBatch.stop')}
      </Button>
      <Button
        variant="destructive"
        size="sm"
        title={disabledReason('kill') ?? undefined}
        disabled={batch.isPending || disabledReason('kill') !== null}
        onClick={() => handleAction('kill')}
      >
        {t('instanceBatch.kill')}
      </Button>

      {/* 停止/强杀/命令下发的二次确认。 */}
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

      {/* 部分失败明细：列出失败实例名 + 原因，已保留其选择供重试（FR-139）。 */}
      <Dialog open={failures !== null} onOpenChange={(v) => { if (!v) setFailures(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('instanceBatch.failTitle')}</DialogTitle>
            <DialogDescription>{t('instanceBatch.failDesc')}</DialogDescription>
          </DialogHeader>
          <div className="max-h-60 space-y-1 overflow-y-auto text-sm">
            {(failures ?? []).map((f, i) => (
              <div key={i} className="flex items-start justify-between gap-3 rounded border px-2 py-1">
                <span className="font-medium shrink-0">{f.name}</span>
                <span className="text-destructive text-right">{f.error}</span>
              </div>
            ))}
          </div>
          <DialogFooter>
            <Button onClick={() => setFailures(null)}>{t('instanceBatch.failClose')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
