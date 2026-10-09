import { usePingNode } from '@/api/diagnostics'
import { PingNodeButton as ControlledPingNodeButton } from '@/components/views/nodes/PingNodeButton'

/**
 * 节点存活测试按钮的应用接线层（ADR-097）。
 *
 * 按钮本体已迁入组件库并受控（不取数）；本层只把 `usePingNode` 的结果与在途态接上去。
 * 保留同名同签名的导出，使既有调用点无需改动。
 */
export function PingNodeButton({ nodeId }: { nodeId: number }) {
  const ping = usePingNode(nodeId)
  return (
    <ControlledPingNodeButton
      result={ping.data}
      pending={ping.isPending}
      isError={ping.isError}
      onPing={() => ping.mutate()}
    />
  )
}
