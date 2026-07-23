import { useTranslation } from 'react-i18next'
import type { BotLoadLoadCounts } from '@/lib/bot-load/types'

export function ConnectionFunnel({ counts }: { counts: BotLoadLoadCounts }) {
  const { t } = useTranslation()
  const layers = [
    { key: 'planned', value: counts.planned },
    { key: 'accepted', value: counts.accepted },
    { key: 'connecting', value: counts.connecting + counts.connected },
    { key: 'connected', value: counts.connected },
  ] as const
  return (
    <div data-testid="connection-funnel" className="space-y-2">
      <h3 className="text-sm font-semibold">{t('botLoad.connectionFunnel')}</h3>
      <ol className="space-y-1.5">
        {layers.map((layer, i) => {
          const prev = i === 0 ? layer.value : layers[i - 1]!.value
          const rate = prev > 0 ? ((layer.value / prev) * 100).toFixed(1) : '—'
          return (
            <li key={layer.key} className="flex items-center gap-2 text-sm">
              <span className="w-24 text-muted-foreground">
                {t(`botLoad.funnel.${layer.key}`, layer.key)}
              </span>
              <div className="h-2 flex-1 overflow-hidden rounded bg-muted">
                <div
                  className="h-full bg-primary/70"
                  style={{
                    width: `${Math.min(100, prev > 0 ? (layer.value / layers[0]!.value) * 100 : 0)}%`,
                  }}
                />
              </div>
              <span className="w-20 text-right tabular-nums">{layer.value}</span>
              <span className="w-14 text-right text-xs text-muted-foreground">{rate}%</span>
            </li>
          )
        })}
      </ol>
    </div>
  )
}
