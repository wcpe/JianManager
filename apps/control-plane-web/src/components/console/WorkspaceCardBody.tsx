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
  /**
   * 终端会话保活（FR-295，ADR-067）：服务器统一控制台（keep-alive 宿主）下传 true——
   * 页签隐藏/卸载不断 WS；画布/导播台等独立表面保持默认卸载即释放（ADR-035 资源模型不变）。
   */
  persistTerminal?: boolean
}

export default function WorkspaceCardBody({ instanceId, type, persistTerminal = false }: WorkspaceCardBodyProps) {
  const { data: instance } = useInstance(instanceId)

  switch (type) {
    case 'terminal':
      return <TerminalPane instanceId={instanceId} hideHeader persistSession={persistTerminal} />
    case 'resource':
      // 资源卡=文件+配置合一：管理视图复用 ConfigExplorer（ResourceExplorer + config 能力，FR-130），
      // 浏览视图用共享 FileBrowser（FR-213）；二者并存，能力不减。
      // FR-375：外层 overflow-hidden，滚动收口在资源管理器内部，避免滚轮带动整页。
      return (
        <div className="flex h-full min-h-0 flex-col overflow-hidden p-3">
          <InstanceResourceCard instanceId={instanceId} />
        </div>
      )
    case 'plugins':
      // FR-423：插件段自己分栏并在卡内滚，外层退成 flex 列——若仍是 `h-full overflow-auto`（块级），
      // 内部的 flex 权重与 max-h-full 都失去可解析的高度。
      // 保留 overflow-y-auto 而非 hidden：xl 以下两栏降级为纵向堆叠、卡片改为纯内容驱动，
      // 总高会超出内容区，此时必须有人能滚（hidden 会把下半栏直接裁掉）。xl 及以上无溢出、无滚动条。
      return (
        <div className="flex h-full min-h-0 flex-col overflow-y-auto p-3">
          <PluginManager instanceId={instanceId} />
        </div>
      )
    case 'metrics':
      // FR-423：监控段头部常驻 + 图表区自滚，故滚动交给内部（同上）。
      return (
        <div className="flex h-full min-h-0 flex-col overflow-y-auto">
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
      // FR-423：业务段分栏（能力清单 / 下发面板）并各自卡内滚；overflow-y-auto 兜窄屏堆叠（同 plugins）。
      return (
        <div className="flex h-full min-h-0 flex-col overflow-y-auto">
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
      // FR-423：Bot 段分栏（Bot 表 / 运行概览），列表在卡内滚；overflow-y-auto 兜窄屏堆叠（同 plugins）。
      return (
        <div className="flex h-full min-h-0 flex-col overflow-y-auto">
          <BotSegment instanceId={instanceId} />
        </div>
      )
    default:
      return null
  }
}
