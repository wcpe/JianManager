import type { NodeInfo } from '@/api/nodes'

/**
 * 节点集群汇总（FR-144）。
 * 抽成无 React 依赖的纯函数以便单测，供节点页顶部 sticky 概览（在线/离线/维护计数 + 集群 CPU/内存/磁盘聚合水位）。
 */

/** 集群概览：状态计数 + 在线节点资源水位均值（百分比 0~100；无在线节点为 null）。 */
export interface NodeClusterSummary {
  total: number
  online: number
  offline: number
  /** 维护模式节点数（与在线正交，维护节点仍可能在线）。 */
  maintenance: number
  /** 在线节点 CPU 占用均值（%）；无在线节点为 null（陈旧离线值不参与，BUG-019 同理）。 */
  cpuPct: number | null
  memPct: number | null
  diskPct: number | null
}

/**
 * 汇总节点集群状态与资源水位。
 * 状态：status===1 在线、===0 离线、===2 启动中（既非在线也非离线，仅计入 total）。
 * 资源水位：仅对在线节点取占用率均值，离线节点 DB 陈旧值不计入（避免误导为在线满载）。
 */
export function summarizeNodes(nodes: NodeInfo[]): NodeClusterSummary {
  let online = 0
  let offline = 0
  let maintenance = 0
  let cpuSum = 0
  let memSum = 0
  let diskSum = 0

  for (const n of nodes) {
    if (n.status === 1) {
      online++
      cpuSum += n.cpuUsage ?? 0
      memSum += n.memoryUsage ?? 0
      diskSum += n.diskUsage ?? 0
    } else if (n.status === 0) {
      offline++
    }
    if (n.maintenance) maintenance++
  }

  const avg = (sum: number): number | null => (online > 0 ? (sum / online) * 100 : null)

  return {
    total: nodes.length,
    online,
    offline,
    maintenance,
    cpuPct: avg(cpuSum),
    memPct: avg(memSum),
    diskPct: avg(diskSum),
  }
}
