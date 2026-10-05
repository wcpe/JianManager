import { useNodePorts } from '@/api/ports'
import NodePortsPanel from '@jianmanager/ui/components/views/nodes/NodePortsPanel'

/**
 * 节点端口占用页签外壳（ADR-097 a 范式）。
 *
 * 取数留在应用侧——「何时取、按 nodeId 缓存多久、节点离线是否还取」都是应用策略，
 * 不该由设计底座决定。受控视图只负责渲染与本地 UI 状态。
 * 外壳之所以薄，是因为纯读组件的决策面本来就是「取数」这一件事。
 */
export default function NodePortsTab({ nodeId }: { nodeId: number }) {
  const { data, isLoading } = useNodePorts(nodeId)
  return <NodePortsPanel data={data} isLoading={isLoading} />
}
