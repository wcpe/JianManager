import { useTranslation } from 'react-i18next'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import type { BotLoadFailure } from '@/lib/bot-load/types'

export function FailureTraceDrawer({
  failure,
  open,
  onOpenChange,
}: {
  failure: BotLoadFailure | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  if (!failure) return null

  const chain = [
    { label: t('botLoad.trace.bot'), value: failure.botUuid ?? '—' },
    { label: t('botLoad.trace.worker'), value: failure.executorNodeId != null ? String(failure.executorNodeId) : '—' },
    { label: t('botLoad.trace.step'), value: failure.stepId ?? failure.commandId ?? '—' },
    { label: t('botLoad.trace.action'), value: failure.actionRunId ?? '—' },
    { label: t('botLoad.trace.error'), value: `${failure.errorCode}: ${failure.message}` },
    {
      label: t('botLoad.trace.retryable'),
      value: failure.retryable ? t('common.yes') : t('common.no'),
    },
  ]

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg" data-testid="failure-trace-drawer">
        <DialogHeader>
          <DialogTitle>{t('botLoad.failureTrace')}</DialogTitle>
        </DialogHeader>
        <ol className="space-y-2 text-sm">
          {chain.map((c) => (
            <li key={c.label} className="rounded border px-3 py-2">
              <div className="text-xs text-muted-foreground">{c.label}</div>
              <div className="break-all font-mono text-xs">{c.value}</div>
            </li>
          ))}
        </ol>
        <p className="text-xs text-muted-foreground">{t('botLoad.failureTraceNoProbe')}</p>
        {failure.legacyCategory === 'probe' && (
          <p className="text-xs text-amber-700 dark:text-amber-300">{t('botLoad.legacyProbeBadge')}</p>
        )}
      </DialogContent>
    </Dialog>
  )
}
