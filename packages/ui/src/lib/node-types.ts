/**
 * 节点实体的类型（FR-048 / FR-062 / FR-281）。
 *
 * 从应用侧 `api/nodes.ts` 抽出：那里混着取数 hook，而节点实体是纯数据，
 * 视图与纯逻辑侧要复用。应用侧原文件原样转出，调用点零改动。
 */

export interface NodeInfo {
  id: number
  uuid: string
  name: string
  host: string
  grpcPort: number
  wsPort: number
  status: number // 0=offline, 1=online, 2=starting
  /** 维护模式（cordon）：true 时禁止新实例调度到该节点（FR-048）。 */
  maintenance: boolean
  /** 反向隧道已连（FR-281，见 ADR-066）：true=指令经隧道下发（NAT/内网可用），false=直拨回退。 */
  tunnelConnected: boolean
  os: string
  arch: string
  cpuCores: number
  memoryMb: number
  diskTotalMb: number
  cpuUsage: number
  memoryUsage: number
  diskUsage: number
  networkBytesSent: number
  networkBytesRecv: number
  /** 1 分钟 load average（FR-062）；取不到为 0。 */
  loadAvg1: number
  lastHeartbeat: string | null
  createdAt: string
}
/** 归档节点（已软删，FR-393）：活跃 NodeInfo 摘要 + deletedAt。 */
export interface ArchivedNode {
  id: number
  uuid: string
  name: string
  host: string
  grpcPort: number
  wsPort: number
  status: number
  maintenance: boolean
  os: string
  arch: string
  cpuCores: number
  memoryMb: number
  lastHeartbeat: string | null
  createdAt: string
  updatedAt?: string
  /** 下线（软删）时间，RFC3339。 */
  deletedAt: string
}
