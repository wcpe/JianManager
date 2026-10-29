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
 * 安全侧「异常请求分析」Tab（ADR-088）。
 * 由旧 `ProtectionCenterPage.tsx` 内联实现原样迁出，行为不变。
 *
 * 展示层已回迁应用侧（`SecurityEventsView` / `SecurityEventRowView`），此处提供取数、
 * 查询读写、三个处置 mutation（含提示）、日志深链与确认弹窗（权限注入）。
 */

/** 
 * 行内三类处置动作的注入包。
 *
 * 三个 mutation **上提到容器**（而非每行各调一遍 hook）：事件表按 `limit=200` 取数，
 * 行级持有全局 mutation 会让 200 行挂 600 个 mutation observer——分层错位，也让每次
 * 改一行时的状态更新波及整表。`pending` 同样由容器统一判定（同一时刻只会有一个确认框打开）。
 */
type EventRowActions = {
  blockIP: ReturnType<typeof useBlockClientDistIP>
  setKeyState: ReturnType<typeof useSetClientDistKeyState>
  setProtection: ReturnType<typeof useSetClientDistChannelProtection>
  /** 任一处置在途（确认按钮禁用）。 */
  pending: boolean
}

/** 事件表服务端上限（列表无翻页器）：命中数达到上限即视为可能被截断，由视图给出提示。 */
const EVENTS_LIMIT = 200

export function EventsTab() {
  const { query, updateQuery } = useSecurityQuery()
  const [riskRule, setRiskRule] = useState('')
  const { data, isError, isLoading } = useClientDistSecurityEvents({
    channelId: query.channelId,
    ip: query.ip,
    machineId: query.machineId,
    errCode: query.errCode,
    riskRule: riskRule || undefined,
    limit: EVENTS_LIMIT,
  })

  const blockIP = useBlockClientDistIP()
  const setKeyState = useSetClientDistKeyState()
  const setProtection = useSetClientDistChannelProtection()
  const actions: EventRowActions = {
    blockIP,
    setKeyState,
    setProtection,
    pending: blockIP.isPending || setKeyState.isPending || setProtection.isPending,
  }

  return (
    <SecurityEventsView
      events={data ?? []}
      truncated={(data?.length ?? 0) >= EVENTS_LIMIT}
      isLoading={isLoading}
      isError={isError}
      query={query}
      onQueryChange={(patch) => updateQuery(patch)}
      riskRule={riskRule}
      onRiskRuleChange={setRiskRule}
      renderRow={(event) => <EventRow key={event.id} event={event} actions={actions} />}
    />
  )
}

function EventRow({ event, actions }: { event: ClientDistSecurityEvent; actions: EventRowActions }) {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  const { blockIP, setKeyState, setProtection, pending } = actions
  const logsHref = buildClientDistHref('/client-dist-ops', searchParams, {
    channelId: event.channelId,
    ip: event.ip,
    machineId: event.machineId,
    errCode: event.errCode,
    tab: 'logs',
    type: 'request',
  })

  return (
    <SecurityEventRowView
      event={event}
      logsHref={logsHref}
      renderLogsLink={(href, label) => <Link to={href}>{label}</Link>}
      /** 
       * 按需实例化：只在当前行真正点了某个处置按钮时才挂那个 DangerConfirm。
       * 原实现把三个弹窗常驻在每一行（200 行 = 600 个 Dialog 实例），而同一时刻至多一个在打开。
       */
      renderConfirm={(state, close) => {
        if (state === 'block-ip') {
          return (
            <DangerConfirm
              open
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
          )
        }
        if (state === 'key-state') {
          return (
            <DangerConfirm
              open
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
          )
        }
        if (state === 'channel-protection') {
          return (
            <DangerConfirm
              open
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
          )
        }
        return null
      }}
    />
  )
}
