import { useNodePorts } from '@/api/ports'
import NodePortsPanel from '@/components/views/nodes/NodePortsPanel'

/**
 * 节点端口占用页签外壳（ADR-102 a 范式）。
 *
 * 取数留在应用侧——「何时取、按 nodeId 缓存多久、节点离线是否还取」都是应用策略，
 * 不该由设计底座决定。受控视图只负责渲染与本地 UI 状态。
 * 外壳之所以薄，是因为纯读组件的决策面本来就是「取数」这一件事。
 */
export default function NodePortsTab({ nodeId }: { nodeId: number }) {
  const { data, isLoading, isError, refetch } = useNodePorts(nodeId)
  /*
    必须把 isError 透给视图：只传 data + isLoading 时，请求失败会落到
    「data 为空、isLoading 为假」这一支，界面显示「该节点暂无端口占用」——
    把拉取失败伪装成没有数据，正是排查时最容易走偏的那种误导。
  */
  return (
    <NodePortsPanel
      data={data}
      isLoading={isLoading}
      {...(isError ? { isError: true, onRetry: () => void refetch() } : {})}
    />
  )
}
