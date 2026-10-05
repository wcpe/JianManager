/**
 * 节点端口占用的共享契约类型（FR-032；RCON 已退役 FR-067）。
 *
 * 归属说明（ADR-097）：这些类型同时被两侧消费——应用 API 层 `@/api/ports` 的
 * `useNodePorts()` 返回它，受控视图 `components/views/nodes/NodePortsPanel` 经 props
 * 接收它。若类型留在应用层，包内组件就只能反向依赖 `@/api` 来取类型，与「包不取数」
 * 的硬约束冲突；故随组件归包，两边都从这里取。
 */

/** 单个实例的端口占用（对应后端 service.PortUsage）。 */
export interface PortUsage {
  instanceId: number
  name: string
  role: string
  serverPort: number
  queryPort: number
}

/** 节点端口占用与分配范围（对应后端 service.NodePortsResult）。 */
export interface NodePorts {
  nodeId: number
  ranges: { serverPortBase: number; rangeSize: number }
  occupied: PortUsage[]
}
