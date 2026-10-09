import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { ClientDistAggregateLogsView, ClientDistRequestLogsView, isClientDistLogTypeValue } from '@/components/views/client-dist/ClientDistLogsTabView'
import type { ClientDistLogTypeValue, ClientDistLogsLinkRenderer } from '@/components/views/client-dist/ClientDistLogsTabView'
import ClientDistExportButton from '@/components/client-dist/ClientDistExportButton'
import { useClientDistSecurityLogs } from '@/api/clientDistSecurity'
import { useClientDistEventDetail, useClientDistEventSearch } from '@/api/clientDistEvents'
import { buildClientDistHref, readClientDistQuery, updateClientDistQuery } from '@/lib/client-dist-query'
import { OPS_ALL } from '@/components/views/client-dist/OpsShared'
import type { RuntimeLink } from '@/components/views/client-dist/OpsShared'

/**
 * 页面 B · 全量日志 Tab（FR-430 / ADR-088 去重合并的**唯一入口**）。
 *
 * 按 `type` 双视图：
 * - `type=request`（缺省）：加厚请求表 + 脱敏详情（`useClientDistEventSearch` + `useClientDistEventDetail`，保留 FR-357/FR-265）。
 * - `type=all` 或其余 5 类：安全聚合多类型表（`useClientDistSecurityLogs`，服务端聚合 hello/risk/action/request/runtime/telemetry）。
 *
 * 展示层已回迁应用侧（`ClientDistRequestLogsView` / `ClientDistAggregateLogsView`），此处只保留应用侧职责：
 * 两路取数（各分支容器内调用，保持「只有当前分支的 hook 生效」）、`type` 与筛选的查询串读写、
 * 排行/联动深链构造与 `Link` 注入、导出按钮接线；详情弹窗开合真源在视图，容器只镜像 id 驱动取详情。
 */

/** 联动条深链渲染：容器提供 router `Link`，目标与样式类名由视图给出。 */
const renderLink: ClientDistLogsLinkRenderer = ({ to, className, children }) => (
  <Link className={className} to={to}>
    {children}
  </Link>
)

export default function ClientDistLogsTab({
  channelId,
  range,
  enabled,
  link,
  onClearLink,
}: {
  channelId?: string
  range: string
  enabled: boolean
  link: RuntimeLink
  onClearLink: () => void
}) {
  const [searchParams, setSearchParams] = useSearchParams()
  const rawType = searchParams.get('type')
  // 缺省进入 logs 无 type → request（等同旧监控体验）。
  const type: ClientDistLogTypeValue = isClientDistLogTypeValue(rawType) ? rawType : 'request'
  const setType = (v: ClientDistLogTypeValue) => {
    setSearchParams(updateClientDistQuery(searchParams, { type: v }), { replace: true })
  }
  // 联动条两个页内深链目标依赖当前查询串，故在容器构造后注入视图。
  const securityCenterHref = buildClientDistHref('/client-dist-ops', searchParams, { tab: 'logs', type: 'all' })
  const channelWorkbenchHref = buildClientDistHref('/client-channels', searchParams, { tab: 'stats' })

  if (type === 'request') {
    return (
      <RequestLogsContainer
        channelId={channelId}
        range={range}
        enabled={enabled}
        link={link}
        onClearLink={onClearLink}
        type={type}
        onTypeChange={setType}
        securityCenterHref={securityCenterHref}
        channelWorkbenchHref={channelWorkbenchHref}
      />
    )
  }
  return <AggregateLogsContainer channelId={channelId} range={range} type={type} onTypeChange={setType} />
}

/** 加厚请求日志分支容器：取数、筛选状态（触发重新取数故归容器）与导出接线。 */
function RequestLogsContainer({
  channelId,
  range,
  enabled,
  link,
  onClearLink,
  type,
  onTypeChange,
  securityCenterHref,
  channelWorkbenchHref,
}: {
  channelId?: string
  range: string
  enabled: boolean
  link: RuntimeLink
  onClearLink: () => void
  type: ClientDistLogTypeValue
  onTypeChange: (v: ClientDistLogTypeValue) => void
  securityCenterHref: string
  channelWorkbenchHref: string
}) {
  const [outcome, setOutcome] = useState<string>(OPS_ALL)
  const [kind, setKind] = useState<string>(OPS_ALL)
  const [detailId, setDetailId] = useState<number | null>(null)

  const { data, isError, isLoading } = useClientDistEventSearch({
    channelId,
    ...link,
    kind: kind === OPS_ALL ? undefined : kind,
    outcome: outcome === OPS_ALL ? '' : (outcome as 'success' | 'failure'),
    page: 1,
    pageSize: 100,
    enabled,
  })
  // 详情按视图上报的 id 取数（id 为空即不发请求；开合真源在视图）。
  const detail = useClientDistEventDetail(detailId)

  return (
    <ClientDistRequestLogsView
      type={type}
      onTypeChange={onTypeChange}
      link={link}
      onClearLink={onClearLink}
      events={data?.items ?? []}
      eventsLoading={isLoading}
      eventsError={isError}
      outcome={outcome}
      onOutcomeChange={setOutcome}
      kind={kind}
      onKindChange={setKind}
      detail={detail.data}
      detailLoading={detail.isLoading}
      detailError={detail.isError}
      onDetailChange={setDetailId}
      securityCenterHref={securityCenterHref}
      channelWorkbenchHref={channelWorkbenchHref}
      renderLink={renderLink}
      exportSlot={
        <ClientDistExportButton
          kind="dist-events"
          filters={{
            channelId,
            range,
            machineId: link.machineId,
            version: link.version,
            errCode: link.errCode,
            ip: link.ip,
            eventKind: kind === OPS_ALL ? undefined : (kind as 'manifest' | 'artifact'),
            outcome: outcome === OPS_ALL ? undefined : outcome,
          }}
        />
      }
    />
  )
}

/** 安全聚合分支容器：取数（含路由查询串筛选）与导出接线。 */
function AggregateLogsContainer({
  channelId,
  range,
  type,
  onTypeChange,
}: {
  channelId?: string
  range: string
  type: ClientDistLogTypeValue
  onTypeChange: (v: ClientDistLogTypeValue) => void
}) {
  const [searchParams, setSearchParams] = useSearchParams()
  const query = readClientDistQuery(searchParams)
  const updateQuery = (patch: Partial<Record<'channelId' | 'machineId' | 'ip' | 'errCode', string | null>>) => {
    setSearchParams(updateClientDistQuery(searchParams, patch), { replace: true })
  }
  const [playerName, setPlayerName] = useState('')
  const { data, isError, isLoading } = useClientDistSecurityLogs({
    type,
    channelId: query.channelId ?? channelId,
    machineId: query.machineId,
    playerName: playerName || undefined,
    ip: query.ip,
    errCode: query.errCode,
    page: 1,
    pageSize: 100,
  })

  return (
    <ClientDistAggregateLogsView
      type={type}
      onTypeChange={onTypeChange}
      query={query}
      onQueryChange={updateQuery}
      playerName={playerName}
      onPlayerNameChange={setPlayerName}
      logs={data?.items ?? []}
      logsLoading={isLoading}
      logsError={isError}
      exportSlot={
        <ClientDistExportButton
          kind="security-logs"
          filters={{
            range,
            type: type === 'all' ? undefined : type,
            channelId: query.channelId ?? channelId,
            machineId: query.machineId,
            playerName: playerName || undefined,
            ip: query.ip,
            errCode: query.errCode,
          }}
        />
      }
    />
  )
}
