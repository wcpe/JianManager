import type { CardType } from '@/lib/console/workspace-card'
import TerminalPane from './TerminalPane'
import BotSegment from './BotSegment'
import MetricsTabSegment from './MetricsTabSegment'
import ServerStateSegment from './ServerStateSegment'
import BusinessSegment from './BusinessSegment'
import BusinessDomainStatus from './BusinessDomainStatus'
import InstanceResourceCard from './InstanceResourceCard'
import PluginManager from '@/components/plugins/PluginManager'

/** 
 * 卡片内容分发器：按卡片类型渲染既有工作区面板。
 *
 * **不丢任何功能**——画布化后所有原工作区段都作为卡片可用：
 * 终端 {@link TerminalPane} / 资源（{@link InstanceResourceCard}，文件+配置合一，承213）/
 * 插件 {@link PluginManager} / 监控 {@link MetricsTabSegment} / 服务器状态 {@link ServerStateSegment} /
 * JBIS 业务 {@link BusinessSegment}（manifest 驱动的通用业务台后具体业务操作统一由它承载）/
 * 业务域状态 {@link BusinessDomainStatus}（业务卡片内的只读概览）/
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
   * 终端会话保活（ADR-067）：服务器统一控制台（keep-alive 宿主）下传 true——
   * 页签隐藏/卸载不断 WS；画布/导播台等独立表面保持默认卸载即释放（ADR-035 资源模型不变）。
   */
  persistTerminal?: boolean
}

export default function WorkspaceCardBody({ instanceId, type, persistTerminal = false }: WorkspaceCardBodyProps) {
  switch (type) {
    case 'terminal':
      return <TerminalPane instanceId={instanceId} hideHeader persistSession={persistTerminal} />
    case 'resource':
      // 资源卡=文件+配置合一：管理视图复用 ConfigExplorer（ResourceExplorer + config 能力），
      // 浏览视图用共享 FileBrowser；二者并存，能力不减。
      // 外层 overflow-hidden，滚动收口在资源管理器内部，避免滚轮带动整页。
      return (
        <div className="flex h-full min-h-0 flex-col overflow-hidden p-3">
          <InstanceResourceCard instanceId={instanceId} />
        </div>
      )
    case 'plugins':
      // 插件段自己分栏并在卡内滚，外层退成 flex 列——若仍是 `h-full overflow-auto`（块级），
      // 内部的 flex 权重与 max-h-full 都失去可解析的高度。
      // 保留 overflow-y-auto 而非 hidden：xl 以下两栏降级为纵向堆叠、卡片改为纯内容驱动，
      // 总高会超出内容区，此时必须有人能滚（hidden 会把下半栏直接裁掉）。xl 及以上无溢出、无滚动条。
      return (
        <div className="flex h-full min-h-0 flex-col overflow-y-auto p-3">
          <PluginManager instanceId={instanceId} />
        </div>
      )
    case 'metrics':
      // 监控卡按探针有无分流（探针全量 / 轻量进程·直探），分流器自带 instance，
      // 无需在此再取实例。滚动仍交给内部（监控段头部常驻 + 内容区自滚）。
      return (
        <div className="flex h-full min-h-0 flex-col overflow-y-auto">
          <MetricsTabSegment instanceId={instanceId} />
        </div>
      )
    case 'serverstate':
      return (
        <div className="h-full overflow-auto">
          <ServerStateSegment instanceId={instanceId} />
        </div>
      )
    case 'business':
      // 业务段分栏（能力清单 / 下发面板）并各自卡内滚；overflow-y-auto 兜窄屏堆叠（同 plugins）。
      // 再叠一块「域状态」总览在最上面——上半回答「注册了哪些域、健不健康」（只读），
      // 下半回答「能对它做什么动作」（manifest 驱动的下发台）。两块回答的是不同问题，
      // 故并存而非互相取代；域块限高（自身卡内滚），不抢下发台的可用高度。
      return (
        <div className="flex h-full min-h-0 flex-col overflow-y-auto">
          <div className="shrink-0 px-3 pt-3">
            <BusinessDomainStatus instanceId={instanceId} className="max-h-72" />
          </div>
          <BusinessSegment instanceId={instanceId} />
        </div>
      )
    case 'bot':
      // Bot 段分栏（Bot 表 / 运行概览），列表在卡内滚；overflow-y-auto 兜窄屏堆叠（同 plugins）。
      return (
        <div className="flex h-full min-h-0 flex-col overflow-y-auto">
          <BotSegment instanceId={instanceId} />
        </div>
      )
    default:
      return null
  }
}
