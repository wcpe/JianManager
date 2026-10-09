import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { SecurityEventsView } from '@/components/views/client-dist/SecurityEventsView'
import { SecurityEventRowView } from '@/components/views/client-dist/SecurityEventRowView'
import type { ClientDistSecurityEvent } from '@/lib/client-dist/client-dist-security-contracts'
import { buildClientDistHref } from '@/lib/client-dist/client-dist-query'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import { useBlockClientDistIP, useClientDistSecurityEvents, useSetClientDistChannelProtection, useSetClientDistKeyState } from '@/api/clientDistSecurity'
import { useSecurityQuery } from './security-shared'

/**
 * 安全侧「异常请求分析」Tab（FR-430 / ADR-088）。
 * 由旧 `ProtectionCenterPage.tsx` 内联实现原样迁出，行为不变。
 *
 * 展示层已回迁应用侧（`SecurityEventsView` / `SecurityEventRowView`），此处提供取数、
 * 查询读写、三个处置 mutation（含提示）、日志深链与确认弹窗（权限注入）。
 */

export function EventsTab() {
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

  return (
    <SecurityEventsView
      events={data ?? []}
      isLoading={isLoading}
      isError={isError}
      query={query}
      onQueryChange={(patch) => updateQuery(patch)}
      riskRule={riskRule}
      onRiskRuleChange={setRiskRule}
      renderRow={(event) => <EventRow key={event.id} event={event} />}
    />
  )
}

function EventRow({ event }: { event: ClientDistSecurityEvent }) {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  const blockIP = useBlockClientDistIP()
  const setKeyState = useSetClientDistKeyState()
  const setProtection = useSetClientDistChannelProtection()
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
    <SecurityEventRowView
      event={event}
      logsHref={logsHref}
      renderLogsLink={(href, label) => <Link to={href}>{label}</Link>}
      renderConfirm={(state, close) => (
        <>
          <DangerConfirm
            open={state === 'block-ip'}
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
                    close()
                  },
                  onError: () => toast.error(t('clientDistOps.actions.toastIpBlockFailed')),
                },
              )
            }}
            onCancel={close}
          />
          <DangerConfirm
            open={state === 'key-state'}
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
                    close()
                  },
                  onError: () => toast.error(t('clientDistOps.actions.toastKeyFailed')),
                },
              )
            }}
            onCancel={close}
          />
          <DangerConfirm
            open={state === 'channel-protection'}
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
                    close()
                  },
                  onError: () => toast.error(t('clientDistOps.actions.toastProtFailed')),
                },
              )
            }}
            onCancel={close}
          />
        </>
      )}
    />
  )
}
