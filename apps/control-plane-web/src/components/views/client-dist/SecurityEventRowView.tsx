import { useState } from 'react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { TableCell, TableRow } from '@jianmanager/ui/components/table'
import UntrustedFieldBadge from '@/components/views/UntrustedFieldBadge'
import type { ClientDistSecurityEvent } from '@/lib/client-dist-security-contracts'
import { maskPlayerName } from '@/lib/privacy-mask'
import { fmtTime, levelVariant, SECURITY_EMPTY as EMPTY } from '@/components/views/client-dist/security-format'

/** 行内确认弹窗的三种处置动作。 */
export type EventRowConfirm = 'block-ip' | 'key-state' | 'channel-protection' | null

/**
 * 安全侧「异常请求」事件行的展示层（FR-430 / ADR-088）。
 * 三类应用侧依赖经注入：日志深链（router Link）、三个处置动作（mutation 与提示）、
 * 以及确认弹窗本体（应用侧 DangerConfirm 包装层注入权限判定）。
 */
export function SecurityEventRowView({
  event,
  logsHref,
  renderLogsLink,
  renderConfirm,
}: {
  event: ClientDistSecurityEvent
  /** 日志深链地址（应用侧按当前 searchParams 构造）。 */
  logsHref: string
  /** 日志深链渲染（应用侧注入 router Link，保持 SPA 导航）。 */
  renderLogsLink: (href: string, label: string) => ReactNode
  /** 确认弹窗渲染（应用侧 DangerConfirm 注入权限）；close 由容器在动作成功后调用。 */
  renderConfirm: (state: EventRowConfirm, close: () => void) => ReactNode
}) {
  const { t } = useTranslation()
  const [confirm, setConfirm] = useState<EventRowConfirm>(null)
  const canBlock = Boolean(event.ip)
  const canKey = event.keyId != null && Number(event.keyId) > 0
  const canProtect = Boolean(event.channelId)

  return (
    <TableRow>
      <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(event.createdAt)}</TableCell>
      <TableCell><Badge variant={levelVariant(event.severity)}>{event.severity}</Badge></TableCell>
      <TableCell>
        <div className="font-medium">{event.ip || EMPTY}</div>
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          <span>{maskPlayerName(event.playerName) || EMPTY}</span>
          {event.playerName ? <UntrustedFieldBadge /> : null}
        </div>
      </TableCell>
      <TableCell>
        <div>{event.channelId || EMPTY}</div>
        <div className="text-xs text-muted-foreground">{event.keyId ?? EMPTY}</div>
      </TableCell>
      <TableCell className="max-w-44 truncate">{event.endpoint || EMPTY}</TableCell>
      <TableCell>
        <div>{event.ruleCode || EMPTY}</div>
        <div className="text-xs text-muted-foreground">{event.errCode || event.status || EMPTY}</div>
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs text-muted-foreground">{event.action || EMPTY}</span>
          <Button asChild size="xs" variant="outline">
            {renderLogsLink(logsHref, t('clientDistOps.events.viewLogs'))}
          </Button>
          {canBlock ? (
            <Button type="button" size="xs" variant="outline" className="text-status-danger" onClick={() => setConfirm('block-ip')}>
              {t('clientDistOps.events.blockIp')}
            </Button>
          ) : null}
          {canKey ? (
            <Button type="button" size="xs" variant="outline" onClick={() => setConfirm('key-state')}>
              {t('clientDistOps.events.keyState')}
            </Button>
          ) : null}
          {canProtect ? (
            <Button type="button" size="xs" variant="outline" onClick={() => setConfirm('channel-protection')}>
              {t('clientDistOps.events.channelProtection')}
            </Button>
          ) : null}
        </div>
        {renderConfirm(confirm, () => setConfirm(null))}
      </TableCell>
    </TableRow>
  )
}

