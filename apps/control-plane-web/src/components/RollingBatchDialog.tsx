import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useCreateRollingOp, useRollingControl, useRollingOp, isRollingActive, type RollingOp } from '@/api/instanceRolling'
import type { InstanceBatchAction } from '@/api/instances'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { cn } from '@jianmanager/ui'
import type { BatchSelectedInstance } from './InstanceBatchBar'

/**
 * 集群级滚动/分批/灰度编排（FR-457）。
 *
 * 相比一次性扇出的批量操作，本对话框把目标按批大小推进、批间停留、失败即停、可按比例灰度，
 * 并在编排推进期间提供暂停/继续/取消与实时进度。逐台仍复用后端既有 per-instance RPC。
 */

interface RollingBatchDialogProps {
  selected: BatchSelectedInstance[]
  onClose: () => void
}

/** 灰度比例预设（百分比）。 */
const RATIO_PRESETS = [100, 50, 20, 10] as const

export default function RollingBatchDialog({ selected, onClose }: RollingBatchDialogProps) {
  const { t } = useTranslation()
  const create = useCreateRollingOp()
  const [opId, setOpId] = useState<number | null>(null)

  const [action, setAction] = useState<InstanceBatchAction>('restart')
  const [command, setCommand] = useState('')
  const [batchSize, setBatchSize] = useState(5)
  const [batchIntervalSec, setBatchIntervalSec] = useState(30)
  const [failFast, setFailFast] = useState(true)
  const [ratioPercent, setRatioPercent] = useState<number>(100)

  const submit = () => {
    create.mutate(
      {
        action,
        ids: selected.map((s) => s.id),
        ...(action === 'command' ? { command } : {}),
        batchSize: batchSize > 0 ? batchSize : 0,
        batchIntervalSec: batchIntervalSec > 0 ? batchIntervalSec : 0,
        failFast,
        // 100% 视作全量（ratio=0）；其余传入 (0,1) 触发灰度抽样。
        ratio: ratioPercent >= 100 ? 0 : ratioPercent / 100,
      },
      { onSuccess: (op) => setOpId(op.id) },
    )
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('rolling.title')}</DialogTitle>
          <DialogDescription>
            {opId ? t('rolling.progressDesc') : t('rolling.formDesc', { count: selected.length })}
          </DialogDescription>
        </DialogHeader>

        {opId ? (
          <RollingProgress opId={opId} />
        ) : (
          <div className="space-y-3">
            <Field label={t('rolling.action')}>
              <select
                aria-label={t('rolling.action')}
                className="h-8 w-full rounded-md border bg-background px-2 text-sm"
                value={action}
                onChange={(e) => setAction(e.target.value as InstanceBatchAction)}
              >
                <option value="restart">{t('instanceBatch.restart')}</option>
                <option value="start">{t('instanceBatch.start')}</option>
                <option value="stop">{t('instanceBatch.stop')}</option>
                <option value="kill">{t('instanceBatch.kill')}</option>
                <option value="command">{t('instanceBatch.command')}</option>
              </select>
            </Field>

            {action === 'command' && (
              <Field label={t('rolling.command')}>
                <Input
                  aria-label={t('rolling.command')}
                  className="h-8"
                  value={command}
                  onChange={(e) => setCommand(e.target.value)}
                  placeholder={t('instanceBatch.commandPlaceholder')}
                />
              </Field>
            )}

            <div className="grid grid-cols-2 gap-3">
              <Field label={t('rolling.batchSize')}>
                <Input
                  aria-label={t('rolling.batchSize')}
                  type="number"
                  min={0}
                  className="h-8"
                  value={batchSize}
                  onChange={(e) => setBatchSize(Number(e.target.value))}
                />
              </Field>
              <Field label={t('rolling.batchInterval')}>
                <Input
                  aria-label={t('rolling.batchInterval')}
                  type="number"
                  min={0}
                  className="h-8"
                  value={batchIntervalSec}
                  onChange={(e) => setBatchIntervalSec(Number(e.target.value))}
                />
              </Field>
            </div>

            <Field label={t('rolling.ratio')}>
              <div className="flex flex-wrap gap-1">
                {RATIO_PRESETS.map((p) => (
                  <button
                    key={p}
                    type="button"
                    aria-pressed={ratioPercent === p}
                    onClick={() => setRatioPercent(p)}
                    className={cn(
                      'rounded-full border px-2.5 py-1 text-xs transition-colors',
                      ratioPercent === p ? 'border-primary bg-primary/10 font-semibold text-primary' : 'text-muted-foreground hover:text-foreground',
                    )}
                  >
                    {p === 100 ? t('rolling.ratioAll') : `${p}%`}
                  </button>
                ))}
              </div>
            </Field>

            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={failFast} onChange={(e) => setFailFast(e.target.checked)} />
              {t('rolling.failFast')}
            </label>
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {opId ? t('rolling.close') : t('common.cancel')}
          </Button>
          {!opId && (
            <Button onClick={submit} disabled={create.isPending || selected.length === 0 || (action === 'command' && command.trim() === '')}>
              {create.isPending ? t('rolling.starting') : t('rolling.start')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 编排进度视图：计数 + 批进度 + 暂停/继续/取消。 */
export function RollingProgress({ opId }: { opId: number }) {
  const { t } = useTranslation()
  const { data: op } = useRollingOp(opId)
  const control = useRollingControl()

  if (!op) return <p className="text-xs text-muted-foreground">{t('common.loading')}</p>

  return (
    <div className="space-y-3" data-testid="rolling-progress">
      <div className="flex items-center gap-2">
        <StatusBadge level={rollingStateLevel(op.state)} label={t(`rolling.state.${op.state}`)} />
        <span className="text-xs text-muted-foreground">
          {t('rolling.opSummary', { action: op.action, batches: totalBatches(op), batchSize: op.batchSize })}
        </span>
      </div>

      <ProgressBar op={op} />

      <div className="grid grid-cols-3 gap-2 text-center">
        <Stat label={t('rolling.succeeded')} value={op.succeeded} tone="success" />
        <Stat label={t('rolling.failed')} value={op.failed} tone={op.failed > 0 ? 'danger' : undefined} />
        <Stat label={t('rolling.skipped')} value={op.skipped} />
      </div>

      {op.errors.length > 0 && (
        <div className="max-h-32 space-y-1 overflow-auto rounded-md border px-2 py-1 text-xs">
          {op.errors.map((e) => (
            <div key={e.instanceId} className="flex items-start justify-between gap-2">
              <span className="font-mono shrink-0">#{e.instanceId}</span>
              <span className="text-status-danger text-right">{e.error}</span>
            </div>
          ))}
        </div>
      )}

      <div className="flex flex-wrap gap-2">
        {(op.state === 'running' || op.state === 'pending') && (
          <Button size="sm" variant="outline" disabled={control.isPending} onClick={() => control.mutate({ opId, action: 'pause' })} data-testid="rolling-pause">
            {t('rolling.pause')}
          </Button>
        )}
        {op.state === 'paused' && (
          <Button size="sm" variant="outline" disabled={control.isPending} onClick={() => control.mutate({ opId, action: 'resume' })} data-testid="rolling-resume">
            {t('rolling.resume')}
          </Button>
        )}
        {isRollingActive(op.state) && (
          <Button size="sm" variant="destructive" disabled={control.isPending} onClick={() => control.mutate({ opId, action: 'cancel' })} data-testid="rolling-cancel">
            {t('rolling.cancel')}
          </Button>
        )}
      </div>
    </div>
  )
}

function ProgressBar({ op }: { op: RollingOp }) {
  const { t } = useTranslation()
  const done = op.succeeded + op.failed
  const pct = op.requested > 0 ? Math.round((done / op.requested) * 100) : 0
  return (
    <div>
      <div className="mb-1 flex justify-between text-[11px] text-muted-foreground">
        <span>{totalBatches(op)} {t('rolling.batches')}</span>
        <span className="font-mono">{done}/{op.requested}</span>
      </div>
      <div className="h-2 overflow-hidden rounded bg-muted">
        <div className={cn('h-full', op.failed > 0 ? 'bg-status-warning' : 'bg-primary')} style={{ width: `${Math.max(2, pct)}%` }} />
      </div>
    </div>
  )
}

function Stat({ label, value, tone }: { label: string; value: number; tone?: 'success' | 'danger' }) {
  return (
    <div className="rounded-md border px-2 py-1.5">
      <p className="text-[11px] text-muted-foreground">{label}</p>
      <p className={cn('font-mono text-base font-semibold', tone === 'success' && 'text-status-success', tone === 'danger' && 'text-status-danger')}>{value}</p>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="mb-1 block text-xs font-medium text-muted-foreground">{label}</label>
      {children}
    </div>
  )
}

/** 批数：batchSize=0 时单批全量。 */
function totalBatches(op: Pick<RollingOp, 'requested' | 'batchSize'>): number {
  return op.batchSize > 0 ? Math.max(1, Math.ceil(op.requested / op.batchSize)) : 1
}

function rollingStateLevel(state: RollingOp['state']) {
  switch (state) {
    case 'running':
    case 'pending':
      return 'info' as const
    case 'paused':
      return 'warning' as const
    case 'done':
      return 'success' as const
    default:
      return 'danger' as const
  }
}
