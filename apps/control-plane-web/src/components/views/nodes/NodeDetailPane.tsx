import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Server } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { ResourceGauge } from '@jianmanager/ui/components/gauge'
import { Panel } from '@jianmanager/ui/components/panel'
import { ObjectPageHeader } from '@jianmanager/ui/components/shell'
import { NodeActionsMenu } from '@/components/views/nodes/NodeListParts'
import type { NodeInfo } from '@jianmanager/ui/lib/node-types'

/** 右栏分段（FR-177 §3.3 + FR-185）：概览/实例/JDK/缓存/端口/代理/监控/坏节点修复。 */
export type DetailTab = 'overview' | 'instances' | 'runtime' | 'cache' | 'ports' | 'proxy' | 'probe' | 'monitor' | 'repair'

export const DETAIL_TABS: DetailTab[] = ['overview', 'instances', 'runtime', 'cache', 'ports', 'proxy', 'probe', 'monitor', 'repair']

export interface NodeDetailPaneProps {
  node: NodeInfo
  instanceCount: number
  tab: DetailTab
  onTab: (t: DetailTab) => void
  onToggleMaintenance: () => void
  onDrain: () => void
  onDelete: () => void
  /** 面包屑导航（容器注入路由跳转；组件库不依赖 react-router）。 */
  onNavigate: (to: string) => void
  /**
   * 分段内容插槽：各分段的取数接线层与面板（含 7 个 tab 容器）留在应用侧，
   * 由调用方按当前 tab 渲染。
   */
  renderTab: (tab: DetailTab, node: NodeInfo) => ReactNode
}
export function NodeDetailPane({
  node,
  instanceCount,
  tab,
  onTab,
  onToggleMaintenance,
  onDrain,
  onDelete,
  onNavigate,
  renderTab,
}: NodeDetailPaneProps) {
  const { t } = useTranslation()
  const online = node.status === 1
  const statusLabel = online ? t('nodes.online') : node.status === 2 ? t('nodes.starting') : t('nodes.offline')
  const loadPct = node.cpuCores > 0 ? ((node.loadAvg1 ?? 0) / node.cpuCores) * 100 : 0

  return (
    <div className="space-y-3">
      {/* 阶段 6 第二步：身份块与分段 Tabs 合并为对象头（原型 `object-head` 的六段：
          面包屑 → 名/状态/元信息/操作 → 指标条 → 工具导航）。
          两个取舍：① 4 个环形仪表**不进 metrics**——原型 `object-stats` 是文字格，但
          FR-311 v2 明确「资源仪表内联右置」，那个设计比原型新且是有意的，故留在内容区；
          ② 隧道/维护徽标同理，对象头只有单个 status，它们在仪表行一并呈现。 */}
      <ObjectPageHeader
        breadcrumbs={[
          { label: t('nodes.title'), to: '/nodes' },
          { label: node.name },
        ]}
        icon={<Server className="size-5" />}
        title={node.name}
        status={{ tone: online ? 'success' : node.status === 2 ? 'warning' : 'default', label: statusLabel }}
        meta={[
          { label: t('nodes.ip'), value: node.host },
          { label: t('nodes.system'), value: `${node.os} ${node.arch}` },
          { label: t('nodes.instancesUnit'), value: instanceCount },
        ]}
        actions={
          <NodeActionsMenu node={node} onToggleMaintenance={onToggleMaintenance} onDrain={onDrain} onDelete={onDelete} />
        }
        tools={DETAIL_TABS.map((k) => ({
          key: k,
          label: t(`nodes.tab.${k}`),
          active: k === tab,
          onSelect: () => onTab(k),
        }))}
        toolsLabel={t('nodes.tools')}
        onNavigate={onNavigate}
      />

      {/* 资源仪表 + 隧道/维护徽标：对象头之下的独立一行（见上注的取舍①）。 */}
      <Panel bodyClassName="p-4">
        <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
          <ResourceGauge label={t('nodes.cpu')} value={online ? (node.cpuUsage ?? 0) * 100 : 0} unit="%" size={56} />
          <ResourceGauge label={t('nodes.memory')} value={online ? (node.memoryUsage ?? 0) * 100 : 0} unit="%" size={56} />
          <ResourceGauge label={t('nodes.disk')} value={online ? (node.diskUsage ?? 0) * 100 : 0} unit="%" size={56} />
          <ResourceGauge label={t('nodes.load')} value={online ? loadPct : 0} unit="%" size={56} />
          <div className="flex flex-wrap items-center gap-2">
            {/* 反向隧道状态（FR-281，见 ADR-066）：仅在线节点有意义——隧道已连=指令免入站；直拨回退=走 node.Host:GRPCPort */}
            {online && (
              <Badge
                variant="outline"
                className={node.tunnelConnected ? 'text-status-success border-status-success/50' : 'text-muted-foreground'}
                title={node.tunnelConnected ? t('nodes.tunnelConnectedHint') : t('nodes.tunnelDirectHint')}
              >
                {node.tunnelConnected ? t('nodes.tunnelConnected') : t('nodes.tunnelDirect')}
              </Badge>
            )}
            {node.maintenance && (
              <Badge variant="outline" className="text-status-warning border-status-warning/50">
                {t('nodes.maintenance')}
              </Badge>
            )}
          </div>
        </div>
      </Panel>

      <div>{renderTab(tab, node)}</div>
    </div>
  )
}
