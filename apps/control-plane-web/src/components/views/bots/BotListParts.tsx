import { Activity } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { FieldLabel } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'
import { BotHealthBar } from '@/components/views/console/BotHealthBar'
import type { BotInfo } from '@/lib/bot'
import type { BotStatusCounts } from '@/lib/bot-list'
import type { Distribution } from '@/lib/bots-overview'
import type { BotRealtimeEvent } from '@/lib/bot-realtime-types'
export function SummaryCards({
  counts,
  dist,
  loading,
  fleetTotal,
  byStatus,
}: {
  counts: BotStatusCounts
  dist: Distribution
  loading: boolean
  /** 舰队 Bot 总数（健康条分母）。 */
  fleetTotal: number
  /** 各状态精确计数（健康条多段着色）。 */
  byStatus?: Record<string, number>
}) {
  const { t } = useTranslation()
  const cards = [
    { key: 'total', label: t('bots.total'), value: counts.total, color: 'text-foreground' },
    { key: 'online', label: t('bots.online'), value: counts.online, color: 'text-status-success' },
    { key: 'connecting', label: t('bots.connecting'), value: counts.connecting, color: 'text-status-warning' },
    { key: 'error', label: t('bots.abnormal'), value: counts.error, color: 'text-status-danger' },
  ]
  return (
    <div className="mb-4 space-y-3">
      <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
        {cards.map((card) => (
          <div key={card.key} className="rounded-xl border bg-card p-4 shadow-soft">
            <p className="text-sm text-muted-foreground">{card.label}</p>
            <p className={cn('mt-1 text-2xl font-bold tabular-nums', card.color)}>{loading ? '—' : card.value}</p>
            {card.key === 'total' && (
              <p className="mt-1 text-xs text-muted-foreground">
                {t('bots.distribution', { instances: dist.instances, nodes: dist.nodes })}
              </p>
            )}
          </div>
        ))}
      </div>
      {/* 舰队健康条（FR-147）：按全局 byStatus 多段着色 connected/connecting/error/stopped */}
      {fleetTotal > 0 && (
        <div className="rounded-xl border bg-card p-3 shadow-soft">
          <div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground">
            <span>{t('bots.fleetHealth')}</span>
            <LegendDot className="bg-status-success" label={t('bots.statusKind.online')} />
            <LegendDot className="bg-status-warning" label={t('bots.statusKind.connecting')} />
            <LegendDot className="bg-status-danger" label={t('bots.statusKind.error')} />
            <LegendDot className="bg-muted-foreground/40" label={t('bots.status_stopped')} />
          </div>
          <BotHealthBar total={fleetTotal} online={counts.online} byStatus={byStatus} />
        </div>
      )}
    </div>
  )
}

/** 图例点：色块 + 文案，用于舰队健康条说明各段语义。 */
export function LegendDot({ className, label }: { className: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <span className={cn('size-2 rounded-full', className)} />
      {label}
    </span>
  )
}
export function BehaviorConfigDialog({
  open,
  behavior,
  target,
  onApply,
  onClose,
}: {
  open: boolean
  behavior: string
  target: string
  onApply: (target: string) => void
  onClose: () => void
}) {
  const { t } = useTranslation()
  const [value, setValue] = useState(target)
  const [prevOpen, setPrevOpen] = useState(open)
  // 打开时以当前 target 回填（渲染期同步，避免 effect）。
  if (open !== prevOpen) {
    setPrevOpen(open)
    if (open) setValue(target)
  }

  const isFollow = behavior === 'follow'
  const label = isFollow ? t('bots.followTarget') : t('bots.patrolPath')
  const placeholder = isFollow ? t('bots.followTargetPlaceholder') : t('bots.patrolPathPlaceholder')

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t('bots.behaviorConfigTitle', { behavior: t(`bots.${behavior}`, behavior) })}</DialogTitle>
        </DialogHeader>
        <div className="space-y-2 py-1">
          <FieldLabel>{label}</FieldLabel>
          <Input value={value} onChange={(e) => setValue(e.target.value)} placeholder={placeholder} autoFocus />
          <p className="text-xs text-muted-foreground">
            {isFollow ? t('bots.followTargetHint') : t('bots.patrolPathHint')}
          </p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => onApply(value.trim())}>{t('common.save')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
const STATUS_COLOR: Record<string, string> = {
  connected: 'text-status-success',
  connecting: 'text-status-warning',
  error: 'text-status-danger',
  stopped: 'text-muted-foreground',
  pending: 'text-muted-foreground',
}

/** 窥视行：单个 Bot 的名称 / 状态 / 行为，并可打开实时详情。 */
export function PeekRow({ bot, onOpen }: { bot: BotInfo; onOpen: () => void }) {
  const { t } = useTranslation()
  return (
    <li className="flex items-center justify-between py-1.5">
      <button type="button" onClick={onOpen} className="truncate text-left font-medium hover:underline">
        {bot.name}
      </button>
      <div className="flex items-center gap-4">
        <span className={cn('text-xs', STATUS_COLOR[bot.status] ?? 'text-muted-foreground')}>
          {t(`bots.status_${bot.status}`, bot.status)}
        </span>
        <span className="w-16 text-right text-xs text-muted-foreground">
          {t(`bots.${bot.behavior}`, bot.behavior)}
        </span>
        <Button size="xs" variant="ghost" onClick={onOpen}>
          <Activity className="size-3" />
          {t('bots.detail')}
        </Button>
      </div>
    </li>
  )
}
export function BotMetric({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border px-3 py-2">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="mt-1 truncate font-medium">{value}</p>
    </div>
  )
}

export function formatPosition(pos: { x: number; y: number; z: number }) {
  return `${pos.x.toFixed(1)}, ${pos.y.toFixed(1)}, ${pos.z.toFixed(1)}`
}

export function formatEventTime(timestamp: number) {
  const value = timestamp > 10_000_000_000 ? timestamp : timestamp * 1000
  return new Date(value).toLocaleTimeString()
}

export function formatBotEvent(event: BotRealtimeEvent) {
  const data = event.data
  if (event.type === 'chat') {
    return `${data.username ?? ''}: ${data.message ?? ''}`.trim()
  }
  if (event.type === 'error') {
    return String(data.error ?? '')
  }
  if (event.type === 'behavior-changed') {
    return String(data.behavior ?? '')
  }
  if (event.type === 'command-sent') {
    return String(data.command ?? '')
  }
  return JSON.stringify(data)
}
