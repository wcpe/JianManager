import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, Download, Loader2, Square, XCircle } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  useCancelBotLoadRun,
  useDownloadBotLoadReport,
  useStopBotLoadRun,
} from '@/api/bot-load'
import type { BotLoadRunV2, BotLoadVerdict } from '@/lib/bot-load/types'
import { isLiveRunState, isTerminalRunState } from '@/lib/bot-load/types'
import { DisclaimerBanner } from './DisclaimerBanner'

export function SessionHeader({
  run,
  streamStatus,
  reportReady,
}: {
  run: BotLoadRunV2
  streamStatus: string
  reportReady: boolean
}) {
  const { t } = useTranslation()
  const stopMut = useStopBotLoadRun()
  const cancelMut = useCancelBotLoadRun()
  const downloadMut = useDownloadBotLoadReport()
  const [confirm, setConfirm] = useState<'stop' | 'cancel' | null>(null)
  const duration = useRunDuration(run)

  const live = isLiveRunState(run.runState)
  const terminal = isTerminalRunState(run.runState)
  const stopping = run.runState === 'stopping' || run.runState === 'cancelling'
  const canReport = terminal && (reportReady || terminal)

  const onConfirm = () => {
    if (confirm === 'stop') {
      stopMut.mutate(
        { id: run.id },
        {
          onSuccess: () => toast.success(t('botLoad.stopAccepted')),
          onError: () => toast.error(t('botLoad.actionFailed')),
        },
      )
    } else if (confirm === 'cancel') {
      cancelMut.mutate(
        { id: run.id },
        {
          onSuccess: () => toast.success(t('botLoad.cancelAccepted')),
          onError: () => toast.error(t('botLoad.actionFailed')),
        },
      )
    }
    setConfirm(null)
  }

  const onDownload = (format: 'json' | 'csv') => {
    downloadMut.mutate(
      { id: run.id, runUuid: run.uuid, format },
      {
        onSuccess: () => toast.success(t('botLoad.reportDownloaded', { format })),
        onError: () => toast.error(t('botLoad.reportFailed')),
      },
    )
  }

  return (
    <header className="space-y-3 border-b pb-4" data-testid="session-header">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Link to="/bots" className="inline-flex items-center gap-1 hover:text-foreground">
              <ArrowLeft className="size-3.5" aria-hidden />
              {t('botLoad.backToBots')}
            </Link>
            <span aria-hidden>·</span>
            <span>
              {t('botLoad.stream')}: {t(`botLoad.streamStatus.${streamStatus}`, streamStatus)}
            </span>
          </div>
          <h1 className="truncate text-xl font-semibold tracking-tight">{run.name}</h1>
          <div
            className="flex flex-wrap items-center gap-2 text-sm"
            aria-live="polite"
            data-testid="session-status-live"
          >
            <StatusChip label={t('botLoad.runState')} value={t(`botLoad.runState_${run.runState}`, run.runState)} />
            <VerdictChip verdict={run.verdict} />
            <StatusChip label={t('botLoad.stage')} value={String(run.currentStage)} />
            <StatusChip label={t('botLoad.duration')} value={duration} />
            <StatusChip
              label={t('botLoad.targetInstance')}
              value={run.instanceName ?? String(run.instanceId)}
            />
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {live && (
            <>
              <Button
                size="sm"
                variant="outline"
                disabled={stopping || stopMut.isPending}
                onClick={() => setConfirm('stop')}
              >
                {stopMut.isPending ? <Loader2 className="size-3.5 animate-spin" /> : <Square className="size-3.5" />}
                {t('botLoad.stop')}
              </Button>
              <Button
                size="sm"
                variant="destructive"
                disabled={cancelMut.isPending}
                onClick={() => setConfirm('cancel')}
              >
                {cancelMut.isPending ? (
                  <Loader2 className="size-3.5 animate-spin" />
                ) : (
                  <XCircle className="size-3.5" />
                )}
                {t('botLoad.cancel')}
              </Button>
            </>
          )}
          <Button
            size="sm"
            variant="outline"
            disabled={!canReport || downloadMut.isPending}
            title={!canReport ? t('botLoad.reportNotReady') : undefined}
            onClick={() => onDownload('json')}
          >
            <Download className="size-3.5" />
            JSON
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={!canReport || downloadMut.isPending}
            title={!canReport ? t('botLoad.reportNotReady') : undefined}
            onClick={() => onDownload('csv')}
          >
            <Download className="size-3.5" />
            CSV
          </Button>
        </div>
      </div>
      <DisclaimerBanner />

      <Dialog open={confirm != null} onOpenChange={(o) => !o && setConfirm(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {confirm === 'cancel' ? t('botLoad.cancelConfirmTitle') : t('botLoad.stopConfirmTitle')}
            </DialogTitle>
            <DialogDescription>
              {confirm === 'cancel' ? t('botLoad.cancelConfirmDesc') : t('botLoad.stopConfirmDesc')}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirm(null)}>
              {t('common.cancel')}
            </Button>
            <Button
              variant={confirm === 'cancel' ? 'destructive' : 'default'}
              onClick={onConfirm}
            >
              {t('common.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </header>
  )
}

function StatusChip({ label, value }: { label: string; value: string }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-full border bg-muted/40 px-2 py-0.5 text-xs">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-medium">{value}</span>
    </span>
  )
}

function VerdictChip({ verdict }: { verdict: BotLoadVerdict }) {
  const { t } = useTranslation()
  const color =
    verdict === 'passed'
      ? 'border-emerald-500/40 text-emerald-700 dark:text-emerald-300'
      : verdict === 'failed'
        ? 'border-destructive/40 text-destructive'
        : verdict === 'aborted'
          ? 'border-amber-500/40 text-amber-800 dark:text-amber-200'
          : 'border-border text-muted-foreground'
  return (
    <span className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium ${color}`}>
      {t('botLoad.verdict')}: {t(`botLoad.verdict_${verdict}`, verdict)}
    </span>
  )
}

function useRunDuration(run: BotLoadRunV2): string {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (isTerminalRunState(run.runState) || !run.startedAt) return
    const t = window.setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [run.runState, run.startedAt])

  const start = run.startedAt ? Date.parse(run.startedAt) : NaN
  if (!Number.isFinite(start)) return '—'
  const end = isTerminalRunState(run.runState)
    ? Date.parse(run.endedAt ?? run.stoppedAt ?? '') || now
    : now
  const sec = Math.max(0, Math.floor((end - start) / 1000))
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  if (h > 0) return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
  return `${m}:${String(s).padStart(2, '0')}`
}
