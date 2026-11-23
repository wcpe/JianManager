import { useTranslation } from 'react-i18next'
import { CheckCircle2, CircleDashed, MinusCircle, XCircle } from 'lucide-react'
import { cardSurface, cardSurfaceTone, cn } from '@jianmanager/ui'
import type { BotLoadVerdictReason } from '@/lib/bot-load/bot-load-types'
import { formatLatencyMs, formatRatio } from '@/lib/bot-load/bot-load-metrics'

export function ThresholdVerdict({ reasons }: { reasons: BotLoadVerdictReason[] }) {
  const { t } = useTranslation()
  if (!reasons?.length) {
    return <p className="text-sm text-muted-foreground">{t('botLoad.verdictEmpty')}</p>
  }
  return (
    <ul className="space-y-2" data-testid="threshold-verdict">
      {reasons.map((r) => (
        // 列表项本身就是 <li>（<ul> 的直接子元素必须是 li，换成 Card 的 div 会破坏列表语义），
        // 故保留元素、改取 Card 同源的表面常量：圆角/描边/底色/阴影由 packages/ui 一处决定
        // （原 bg-card → faint 档，成排出现的内联块）。
        <li
          key={`${r.key}-${r.stageIndex ?? 'run'}`}
          className={cn(cardSurface, cardSurfaceTone.faint, 'flex items-start gap-2 px-3 py-2 text-sm')}
        >
          <VerdictIcon state={r.state} />
          <div className="min-w-0 flex-1">
            <div className="font-medium">
              {t(`botLoad.verdictKey.${r.key}`, r.key)}
              <span className="ml-2 text-xs font-normal text-muted-foreground">
                {t(`botLoad.verdictState.${r.state}`, r.state)}
              </span>
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              {t('botLoad.expected')}: {formatValue(r.expected, r.unit)} · {t('botLoad.actual')}:{' '}
              {formatValue(r.actual, r.unit)}
            </div>
          </div>
        </li>
      ))}
    </ul>
  )
}

function VerdictIcon({ state }: { state: string }) {
  if (state === 'pass') return <CheckCircle2 className="mt-0.5 size-4 text-emerald-600" aria-hidden />
  if (state === 'fail') return <XCircle className="mt-0.5 size-4 text-destructive" aria-hidden />
  if (state === 'not_applicable')
    return <MinusCircle className="mt-0.5 size-4 text-muted-foreground" aria-hidden />
  return <CircleDashed className="mt-0.5 size-4 text-amber-600" aria-hidden />
}

function formatValue(v: number | string | undefined, unit?: string): string {
  if (v == null) return '—'
  if (typeof v === 'string') return v
  if (unit === 'ratio') return formatRatio(v)
  if (unit === 'ms') return formatLatencyMs(v)
  return String(v)
}
