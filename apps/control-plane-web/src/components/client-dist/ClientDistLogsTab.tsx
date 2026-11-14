import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { ClientDistAggregateLogsView, ClientDistRequestLogsView, isClientDistLogTypeValue } from '@/components/views/client-dist/ClientDistLogsTabView'
import type { ClientDistLogTypeValue, ClientDistLogsLinkRenderer } from '@/components/views/client-dist/ClientDistLogsTabView'
import ClientDistExportButton from '@/components/client-dist/ClientDistExportButton'
import { useClientDistSecurityLogs } from '@/api/clientDistSecurity'
import { useClientDistEventDetail, useClientDistEventSearch } from '@/api/clientDistEvents'
import { buildClientDistHref, readClientDistQuery, updateClientDistQuery } from '@/lib/client-dist/client-dist-query'
import { OPS_ALL, logWindowParams } from '@/components/views/client-dist/OpsShared'
import type { RuntimeLink } from '@/components/views/client-dist/OpsShared'
import type { ObsWindow } from '@/lib/metrics/obs-window'

/** 
 * 页面 B · 全量日志 Tab（去重合并的**唯一入口**）。
 *
 * 按 `type` 双视图：
 * - `type=request`（缺省）：加厚请求表 + 脱敏详情（`useClientDistEventSearch` + `useClientDistEventDetail`，保留）。
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
  window: obsWindow,
  enabled,
  link,
  onClearLink,
}: {
  channelId?: string
  /** 页头时间窗：列表与导出共用同一份（R31），自定义区间保留真实 from/to。 */
  window: ObsWindow
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
        obsWindow={obsWindow}
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
  return <AggregateLogsContainer channelId={channelId} obsWindow={obsWindow} type={type} onTypeChange={setType} />
}

/** 加厚请求日志分支容器：取数、筛选状态（触发重新取数故归容器）与导出接线。 */
function RequestLogsContainer({
  channelId,
  obsWindow,
  enabled,
  link,
  onClearLink,
  type,
  onTypeChange,
  securityCenterHref,
  channelWorkbenchHref,
}: {
  channelId?: string
  obsWindow: ObsWindow
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

  // 时间窗与导出同一份（R31）：此前列表不带时间参数、导出却按 range 过滤，
  // 用户看到的行和下载的 CSV 不是同一集合。
  const window = logWindowParams(obsWindow)
  const { data, isError, isLoading } = useClientDistEventSearch({
    channelId,
    ...link,
    ...window,
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
      eventsTotal={data?.total ?? 0}
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
            range: window.range,
            from: window.from,
            to: window.to,
            machineId: link.machineId,
            version: link.version,
            errCode: link.errCode,
            ip: link.ip,
            // 运行态维度（R34）：列表按这四项过滤，导出必须同一口径。
            runtimeVersion: link.runtimeVersion,
            coreVersion: link.coreVersion,
            platform: link.platform,
            lag: link.lag,
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
  obsWindow,
  type,
  onTypeChange,
}: {
  channelId?: string
  obsWindow: ObsWindow
  type: ClientDistLogTypeValue
  onTypeChange: (v: ClientDistLogTypeValue) => void
}) {
  const [searchParams, setSearchParams] = useSearchParams()
  const query = readClientDistQuery(searchParams)
  const updateQuery = (patch: Partial<Record<'channelId' | 'machineId' | 'ip' | 'errCode', string | null>>) => {
    setSearchParams(updateClientDistQuery(searchParams, patch), { replace: true })
  }
  const [playerName, setPlayerName] = useState('')
  // 时间窗（R31）：列表与导出共用页头时间窗，避免「列表全量、导出按窗」对不上。
  const window = logWindowParams(obsWindow)
  const { data, isError, isLoading } = useClientDistSecurityLogs({
    type,
    ...window,
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
      logsTotal={data?.total ?? 0}
      logsLoading={isLoading}
      logsError={isError}
      exportSlot={
        <ClientDistExportButton
          kind="security-logs"
          filters={{
            range: window.range,
            from: window.from,
            to: window.to,
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
