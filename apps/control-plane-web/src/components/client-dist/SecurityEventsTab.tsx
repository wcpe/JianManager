import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useBlockClientDistIP,
  useClientDistSecurityEvents,
  useSetClientDistChannelProtection,
  useSetClientDistKeyState,
  type ClientDistSecurityEvent,
} from '@/api/clientDistSecurity'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import DangerConfirm from '@/components/DangerConfirm'
import UntrustedFieldBadge from '@/components/UntrustedFieldBadge'
import { buildClientDistHref } from '@/lib/client-dist-query'
import { maskPlayerName } from '@/lib/privacy-mask'
import { EmptyState, SECURITY_EMPTY as EMPTY, fmtTime, levelVariant, useSecurityQuery } from './security-shared'

/**
 * 安全侧「异常请求分析」Tab（FR-430 / ADR-088）。
 * 由旧 `ProtectionCenterPage.tsx` 内联实现原样迁出，行为不变。
 */

export function EventsTab() {
  const { t } = useTranslation()
  const { query, updateQuery } = useSecurityQuery()
  const [riskRule, setRiskRule] = useState('')
  const { data, isError, isLoading } = useClientDistSecurityEvents({
    channelId: query.channelId,
    ip: query.ip,
    machineId: query.machineId,
    errCode: query.errCode,
    riskRule: riskRule || undefined,
    limit: 200,
  })
  const events = data ?? []
  return (
    <Panel
      title={t('clientDistOps.events.title')}
      actions={
        <div className="flex flex-wrap gap-2">
          <Input className="w-36" placeholder={t('clientDistOps.events.phChannel')} value={query.channelId ?? ''} onChange={(e) => updateQuery({ channelId: e.target.value || null })} />
          <Input className="w-36" placeholder={t('clientDistOps.events.phIp')} value={query.ip ?? ''} onChange={(e) => updateQuery({ ip: e.target.value || null })} />
          <Input className="w-40" placeholder={t('clientDistOps.events.phErrCode')} value={query.errCode ?? ''} onChange={(e) => updateQuery({ errCode: e.target.value || null })} />
          <Input className="w-40" placeholder={t('clientDistOps.events.phRiskRule')} value={riskRule} onChange={(e) => setRiskRule(e.target.value)} />
        </div>
      }
    >
      {isError ? (
        <EmptyState text={t('clientDistOps.events.error')} />
      ) : events.length === 0 ? (
        <EmptyState text={isLoading ? t('common.loading') : t('clientDistOps.events.empty')} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('clientDistOps.events.colTime')}</TableHead>
              <TableHead>{t('clientDistOps.events.colLevel')}</TableHead>
              <TableHead>{t('clientDistOps.events.colIpPlayer')}</TableHead>
              <TableHead>{t('clientDistOps.events.colChannelKey')}</TableHead>
              <TableHead>{t('clientDistOps.events.colEndpoint')}</TableHead>
              <TableHead>{t('clientDistOps.events.colRuleErrCode')}</TableHead>
              <TableHead>{t('clientDistOps.events.colAction')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {events.map((event) => <EventRow key={event.id} event={event} />)}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}

type EventRowConfirm = 'block-ip' | 'key-state' | 'channel-protection' | null

function EventRow({ event }: { event: ClientDistSecurityEvent }) {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  const [confirm, setConfirm] = useState<EventRowConfirm>(null)
  const blockIP = useBlockClientDistIP()
  const setKeyState = useSetClientDistKeyState()
  const setProtection = useSetClientDistChannelProtection()
  const canBlock = Boolean(event.ip)
  const canKey = event.keyId != null && Number(event.keyId) > 0
  const canProtect = Boolean(event.channelId)
  const logsHref = buildClientDistHref('/client-dist-ops', searchParams, {
    channelId: event.channelId,
    ip: event.ip,
    machineId: event.machineId,
    errCode: event.errCode,
    tab: 'logs',
    type: 'request',
  })
  const pending = blockIP.isPending || setKeyState.isPending || setProtection.isPending
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
            <Link to={logsHref}>{t('clientDistOps.events.viewLogs')}</Link>
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
        <DangerConfirm
          open={confirm === 'block-ip'}
          title={t('clientDistOps.events.confirmBlockTitle', { ip: event.ip })}
          description={t('clientDistOps.events.confirmBlockDesc')}
          confirmLabel={t('clientDistOps.events.confirmBlockLabel')}
          pending={pending}
          onConfirm={() => {
            blockIP.mutate(
              { ip: event.ip, channelId: event.channelId || undefined, reason: event.reason || t('clientDistOps.events.reasonBlock'), durationMinutes: 30 },
              {
                onSuccess: () => {
                  toast.success(t('clientDistOps.actions.toastIpBlocked'))
                  setConfirm(null)
                },
                onError: () => toast.error(t('clientDistOps.actions.toastIpBlockFailed')),
              },
            )
          }}
          onCancel={() => setConfirm(null)}
        />
        <DangerConfirm
          open={confirm === 'key-state'}
          title={t('clientDistOps.events.confirmKeyTitle', { key: event.keyId ?? '' })}
          description={t('clientDistOps.events.confirmKeyDesc')}
          confirmLabel={t('clientDistOps.events.confirmKeyLabel')}
          pending={pending}
          onConfirm={() => {
            setKeyState.mutate(
              {
                keyId: String(event.keyId),
                body: { state: 'throttled', reason: event.reason || t('clientDistOps.events.reasonKey') },
              },
              {
                onSuccess: () => {
                  toast.success(t('clientDistOps.actions.toastKeyOk'))
                  setConfirm(null)
                },
                onError: () => toast.error(t('clientDistOps.actions.toastKeyFailed')),
              },
            )
          }}
          onCancel={() => setConfirm(null)}
        />
        <DangerConfirm
          open={confirm === 'channel-protection'}
          title={t('clientDistOps.events.confirmProtTitle', { channel: event.channelId })}
          description={t('clientDistOps.events.confirmProtDesc')}
          confirmLabel={t('clientDistOps.events.confirmProtLabel')}
          pending={pending}
          onConfirm={() => {
            setProtection.mutate(
              {
                channelId: event.channelId,
                body: {
                  mode: 'retry_after',
                  reason: event.reason || t('clientDistOps.events.reasonProtection'),
                  retryAfterSeconds: 60,
                },
              },
              {
                onSuccess: () => {
                  toast.success(t('clientDistOps.actions.toastProtOk'))
                  setConfirm(null)
                },
                onError: () => toast.error(t('clientDistOps.actions.toastProtFailed')),
              },
            )
          }}
          onCancel={() => setConfirm(null)}
        />
      </TableCell>
    </TableRow>
  )
}
