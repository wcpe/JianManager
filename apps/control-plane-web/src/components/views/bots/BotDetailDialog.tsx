import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Send } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Input } from '@jianmanager/ui/components/input'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import { BotMetric, formatBotEvent, formatEventTime, formatPosition } from '@/components/views/bots/BotListParts'
import type { BotInfo } from '@jianmanager/ui/lib/bot'
import type { BotRealtimeState } from '@jianmanager/ui/lib/bot-realtime-types'

export interface BotDetailDialogProps {
  /** 目标 Bot；null 表示关闭。 */
  botId: number | null
  onOpenChange: (open: boolean) => void
  /** Bot 元数据（容器经 useBot 取数）。 */
  bot?: BotInfo
  /** 实时状态（容器经 useBotEvents 取数；WS 流由容器持有）。 */
  realtime: BotRealtimeState
  /** 发送控制台命令（容器注入 mutation）。 */
  onSendCommand: (command: string) => Promise<void>
  /** 发送中（禁用按钮）。 */
  sending?: boolean
  /** 结果回执（容器注入 toast；组件库不依赖 toast 实现）。 */
  onNotify?: (level: 'success' | 'error', message: string) => void
}
export function BotDetailDialog({
  botId,
  onOpenChange,
  bot,
  realtime,
  onSendCommand,
  sending = false,
  onNotify,
}: BotDetailDialogProps) {
  const { t } = useTranslation()
  const [command, setCommand] = useState('')
  const open = botId !== null

  const status = realtime.status || bot?.status || ''
  const behavior = realtime.behavior || bot?.behavior || ''
  const health = realtime.health
  const food = realtime.food

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const text = command.trim()
    if (!botId || !text) return
    try {
      await onSendCommand(text)
      setCommand('')
    } catch {
      onNotify?.('error', t('bots.commandFailed'))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-2xl`}>
        <DialogHeader>
          <DialogTitle>{bot ? bot.name : t('bots.detail')}</DialogTitle>
        </DialogHeader>
        <ScrollableDialogBody className="space-y-4 py-1">
          <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
            <BotMetric label={t('bots.status')} value={status ? t(`bots.status_${status}`, status) : '—'} />
            <BotMetric label={t('bots.behavior')} value={behavior ? t(`bots.${behavior}`, behavior) : '—'} />
            <BotMetric label={t('bots.health')} value={health == null ? '—' : String(Math.round(health))} />
            <BotMetric label={t('bots.food')} value={food == null ? '—' : String(food)} />
          </div>

          {realtime.position && (
            <div className="rounded-lg border px-3 py-2 text-sm text-muted-foreground">
              {t('bots.position')}: {formatPosition(realtime.position)}
            </div>
          )}

          <form onSubmit={submit} className="flex gap-2">
            <Input
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              placeholder={t('bots.commandPlaceholder')}
            />
            <Button type="submit" disabled={!command.trim() || sending}>
              <Send className="size-4" />
              {t('bots.sendCommand')}
            </Button>
          </form>

          <div className="space-y-2">
            <div className="flex items-center justify-between text-sm">
              <span className="font-medium">{t('bots.realtimeEvents')}</span>
              <span className="text-xs text-muted-foreground">
                {realtime.connected ? t('bots.streamConnected') : t('bots.streamConnecting')}
              </span>
            </div>
            <div className="max-h-72 overflow-auto rounded-lg border">
              {realtime.events.length === 0 ? (
                <p className="px-3 py-6 text-center text-sm text-muted-foreground">{t('bots.noEvents')}</p>
              ) : (
                <ul className="divide-y text-sm">
                  {realtime.events.map((event, index) => (
                    <li key={`${event.timestamp}-${index}`} className="px-3 py-2">
                      <div className="flex items-center justify-between gap-3">
                        <span className="font-medium">{t(`bots.event_${event.type}`, event.type)}</span>
                        <span className="text-xs text-muted-foreground">{formatEventTime(event.timestamp)}</span>
                      </div>
                      <p className="mt-1 break-words text-muted-foreground">{formatBotEvent(event)}</p>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        </ScrollableDialogBody>
      </DialogContent>
    </Dialog>
  )
}
