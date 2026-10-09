import { useTranslation } from 'react-i18next'
import { Download } from 'lucide-react'
import type { ClientMachineEvent } from '@/lib/client-dist-machines-contracts'
import { fmtBytes, fmtTime, resultBadge } from '@/components/views/client-dist/machine-format'

/**
 * 机器更新事件时间线（FR-426）。
 * 取数（`useClientMachineEvents`）由应用侧容器负责，此处只做三态渲染。
 */
export function MachineTimelineView({ items, isLoading }: { items: ClientMachineEvent[]; isLoading: boolean }) {
  const { t } = useTranslation()

  if (isLoading) {
    return <p className="py-6 text-center text-sm text-muted-foreground">{t('common.loading', '加载中…')}</p>
  }
  if (items.length === 0) {
    return <p className="py-6 text-center text-sm text-muted-foreground">{t('clientDistObs.timelineEmpty', '该时间段内无更新事件')}</p>
  }

  return (
    <div className="mt-2 space-y-0" data-testid="machine-timeline">
      {items.map((ev, i) => (
        <div key={`${ev.ts}-${i}`} className="relative flex gap-3 pb-4 pl-1">
          {i < items.length - 1 && <div className="absolute left-[7px] top-5 h-full w-px bg-border" />}
          <div className="z-10 mt-1 size-3.5 shrink-0 rounded-full border-2 border-background"
            style={{ backgroundColor: ev.result === 'success' ? '#059669' : ev.result === 'error' ? '#dc2626' : '#d97706' }}
          />
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              {resultBadge(t, ev.result)}
              <span className="text-xs tabular-nums text-muted-foreground">{fmtTime(ev.ts)}</span>
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              {t('clientDistObs.timelineMeta', '目标 v{{version}} · {{bytes}} · {{sec}}s', {
                version: ev.version,
                bytes: fmtBytes(ev.bytes),
                sec: (ev.durationMs / 1000).toFixed(1),
              })}
            </div>
          </div>
        </div>
      ))}
      <div className="flex items-center gap-1.5 pt-1 text-xs text-muted-foreground">
        <Download className="size-3" />
        {t('clientDistObs.timelineTotal', '共 {{n}} 次更新记录', { n: items.length })}
      </div>
    </div>
  )
}
