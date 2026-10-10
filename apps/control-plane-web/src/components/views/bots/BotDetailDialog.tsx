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
import type { BotInfo } from '@/lib/bots/bot'
import type { BotRealtimeEvent, BotRealtimeState } from '@/lib/bots/bot-realtime-types'

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
            {/* 命令输入框原先只有 placeholder，读屏取不到名字（发送按钮也只叫「发送」）。 */}
            <Input
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              placeholder={t('bots.commandPlaceholder')}
              aria-label={t('bots.commandPlaceholder')}
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
                  {/*
                    事件流是「新事件插到队首」的滚窗列表：用 `${timestamp}-${index}` 作 key 时，
                    每来一条新事件旧行下标全部平移 → key 变化 → React 拆掉整列 <li> 重建。
                    事件没有服务端 id，故按内容指纹做稳定 key（完全相同的重复事件按出现次序补序号）。
                  */}
                  {realtime.events.map((event) => (
                    <li key={botEventKey(event)} className="px-3 py-2">
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

/**
 * Bot 实时事件的稳定 React key：`BotRealtimeEvent` 没有 id，改用「时间戳 + 类型 + 载荷」指纹。
 * 同一条事件无论滚到第几位都保持同一个 key；极端情况下同毫秒同内容的事件不可区分，
 * 这种情况本身也无从区分，指纹相同即视为同一条（列表窗口仅保留最近若干条）。
 */
function botEventKey(event: BotRealtimeEvent): string {
  return `${event.timestamp}|${event.type}|${JSON.stringify(event.data)}`
}
