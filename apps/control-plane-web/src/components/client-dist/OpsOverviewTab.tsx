import { Link, useSearchParams } from 'react-router'
import { OpsOverviewTabView } from '@/components/views/client-dist/OpsOverviewTabView'
import type { OpsOverviewLinkRenderer, RankFilterKey } from '@/components/views/client-dist/OpsOverviewTabView'
import type { ObsWindow } from '@/lib/obs-window'
import { useClientDistObservability, type ClientDistStats } from '@/api/clientStats'
import { useClientDistSecurityOverview } from '@/api/clientDistSecurity'
import type { ClientRuntimeOverview } from '@/api/clientRuntimeStates'
import { buildClientDistHref } from '@/lib/client-dist-query'
import type { RuntimeLink } from './ops-shared'

/**
 * 页面 B · 总览 Tab（全面融合：分发健康 + 运行态 + 请求侧 + 安全态势）。
 * KPI 口径严格遵循 FR-356：更新侧率只信 observability，请求侧率只信 stats。
 *
 * 展示层已回迁应用侧（`OpsOverviewTabView`），此处只保留取数与路由语义：
 * 更新侧观测（`useClientDistObservability`，随页头频道/时间窗）与安全态势快照
 * （`useClientDistSecurityOverview`，不随窗口）两个 hook、排行主体 → 全量日志的深链构造
 * （目标依赖当前查询串）与 `Link` 注入都在这里；`stats` / `runtime` / `onLink` 仍由页面传入。
 */

/** 排行深链渲染：容器提供 router `Link`，目标与样式类名由视图给出。 */
const renderLink: OpsOverviewLinkRenderer = ({ to, className, children }) => (
  <Link className={className} to={to}>
    {children}
  </Link>
)

export default function OpsOverviewTab({
  channelId,
  window,
  stats,
  runtime,
  onLink,
}: {
  channelId?: string
  window: ObsWindow
  stats?: ClientDistStats
  runtime?: ClientRuntimeOverview
  onLink: (link: RuntimeLink) => void
}) {
  const obs = useClientDistObservability({ channelId, window })
  const security = useClientDistSecurityOverview()
  const [searchParams] = useSearchParams()

  /** 排行主体 → 全量日志 request 视图（沿用原深链语义）。 */
  const rankLogsHref = (filterKey: RankFilterKey, subject: string) =>
    buildClientDistHref('/client-dist-ops', searchParams, {
      [filterKey]: subject,
      tab: 'logs',
      type: 'request',
    })

  return (
    <OpsOverviewTabView
      stats={stats}
      runtime={runtime}
      observability={obs.data}
      obsLoading={obs.isLoading}
      obsError={obs.isError}
      security={security.data}
      securityLoading={security.isLoading}
      securityError={security.isError}
      onLink={onLink}
      rankLogsHref={rankLogsHref}
      renderLink={renderLink}
    />
  )
}
