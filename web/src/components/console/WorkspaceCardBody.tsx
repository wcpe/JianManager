import { useInstance } from '@/api/instances'
import type { CardType } from '@/lib/workspace-card'
import TerminalPane from './TerminalPane'
import BotSegment from './BotSegment'
import MetricsSegment from './MetricsSegment'
import ServerStateSegment from './ServerStateSegment'
import BusinessSegment from './BusinessSegment'
import EconomySegment from './EconomySegment'
import InventorySegment from './InventorySegment'
import InstanceResourceCard from './InstanceResourceCard'
import PluginManager from '@/components/plugins/PluginManager'

/**
 * 卡片内容分发器（FR-166）：按卡片类型渲染既有工作区面板。
 *
 * **不丢任何功能**——画布化后所有原工作区段都作为卡片可用：
 * 终端 {@link TerminalPane} / 资源（{@link InstanceResourceCard}，文件+配置合一，承 FR-130/213）/
 * 插件 {@link PluginManager} / 监控 {@link MetricsSegment} / 服务器状态 {@link ServerStateSegment} /
 * JBIS 业务 {@link BusinessSegment} / 经济 {@link EconomySegment} / 背包 {@link InventorySegment} /
 * Bot {@link BotSegment}。
 *
 * 仅当卡片在画布上挂载时本组件才渲染（惰性挂载——承 ADR「未挂载卡不建 WS」），
 * 故终端 WS / metrics 轮询只对画布上的卡建立。
 */
interface WorkspaceCardBodyProps {
  /** 卡片所属实例 id（本 FR 单实例，全部卡片同一 id）。 */
  instanceId: number
  /** 卡片功能类型。 */
  type: CardType
}

export default function WorkspaceCardBody({ instanceId, type }: WorkspaceCardBodyProps) {
  const { data: instance } = useInstance(instanceId)

  switch (type) {
    case 'terminal':
      return <TerminalPane instanceId={instanceId} hideHeader />
    case 'resource':
      // 资源卡=文件+配置合一：管理视图复用 ConfigExplorer（ResourceExplorer + config 能力，FR-130），
      // 浏览视图用共享 FileBrowser（FR-213）；二者并存，能力不减。
      return (
        <div className="h-full overflow-auto p-3">
          <InstanceResourceCard instanceId={instanceId} />
        </div>
      )
    case 'plugins':
      return (
        <div className="h-full overflow-auto p-3">
          <PluginManager instanceId={instanceId} />
        </div>
      )
    case 'metrics':
      return (
        <div className="h-full overflow-auto">
          <MetricsSegment instanceUuid={instance?.uuid ?? ''} instanceId={instanceId} />
        </div>
      )
    case 'serverstate':
      return (
        <div className="h-full overflow-auto">
          <ServerStateSegment instanceId={instanceId} />
        </div>
      )
    case 'business':
      return (
        <div className="h-full overflow-auto">
          <BusinessSegment instanceId={instanceId} />
        </div>
      )
    case 'economy':
      return (
        <div className="h-full overflow-auto">
          <EconomySegment instanceId={instanceId} />
        </div>
      )
    case 'inventory':
      return (
        <div className="h-full overflow-auto">
          <InventorySegment instanceId={instanceId} />
        </div>
      )
    case 'bot':
      return (
        <div className="h-full overflow-auto">
          <BotSegment instanceId={instanceId} />
        </div>
      )
    default:
      return null
  }
}
