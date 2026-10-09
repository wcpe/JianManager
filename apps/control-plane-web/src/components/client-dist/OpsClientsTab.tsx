import type { ClientRuntimeOverview, RuntimeLink } from '@jianmanager/ui'
import { OpsClientsTabView } from '@/components/views/client-dist/OpsClientsTabView'
import type { ObsWindow } from '@jianmanager/ui/lib/obs-window'
import { ObsOverviewSection } from './ObsOverviewSection'

/**
 * 页面 B · 机器 / 客户端 Tab（FR-430，迁自旧监控页 `ClientsTab` + `ObsOverviewSection`）。
 * 更新侧总览（洞察卡 + 热力图 + 机器排行）+ 运行态 KPI/分布/明细；
 * 维护 FR-265「统计/监控=请求侧」边界——本 Tab 只走更新侧与运行态数据。
 *
 * 展示层已归包（`OpsClientsTabView`），此处只组装取数容器 slot：更新侧总览区块。
 */
export default function OpsClientsTab({
  channelId,
  window,
  overview,
  isError,
  onLink,
}: {
  channelId?: string
  window: ObsWindow
  overview?: ClientRuntimeOverview
  isError: boolean
  onLink: (link: RuntimeLink) => void
}) {
  return (
    <OpsClientsTabView
      overview={overview}
      isError={isError}
      onLink={onLink}
      obsSection={<ObsOverviewSection channelId={channelId} window={window} />}
    />
  )
}
