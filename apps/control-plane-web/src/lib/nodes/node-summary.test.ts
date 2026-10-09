import { describe, it, expect } from 'vitest'
import { summarizeNodes, type NodeClusterSummary } from './node-summary'
import type { NodeInfo } from '@/api/nodes'

/** 构造最小节点对象（只填汇总用到的字段）。 */
function node(p: Partial<NodeInfo>): NodeInfo {
  return {
    id: Math.random(),
    uuid: '',
    name: 'n',
    host: '',
    grpcPort: 0,
    wsPort: 0,
    status: 1,
    maintenance: false,
    os: '',
    arch: '',
    cpuCores: 4,
    memoryMb: 0,
    diskTotalMb: 0,
    cpuUsage: 0,
    memoryUsage: 0,
    diskUsage: 0,
    networkBytesSent: 0,
    networkBytesRecv: 0,
    loadAvg1: 0,
    lastHeartbeat: null,
    createdAt: '',
    ...p,
  }
}

describe('summarizeNodes', () => {
  it('空集合：计数为 0，聚合水位为 null（无在线节点不显示水位）', () => {
    const s: NodeClusterSummary = summarizeNodes([])
    expect(s.total).toBe(0)
    expect(s.online).toBe(0)
    expect(s.offline).toBe(0)
    expect(s.maintenance).toBe(0)
    expect(s.cpuPct).toBeNull()
    expect(s.memPct).toBeNull()
    expect(s.diskPct).toBeNull()
  })

  it('在线/离线/维护计数：维护与在线正交（维护节点仍可能在线）', () => {
    const s = summarizeNodes([
      node({ status: 1 }),
      node({ status: 1, maintenance: true }),
      node({ status: 0 }), // 离线
      node({ status: 2 }), // 启动中：非在线非离线
    ])
    expect(s.total).toBe(4)
    expect(s.online).toBe(2)
    expect(s.offline).toBe(1)
    expect(s.maintenance).toBe(1)
  })

  it('聚合水位只对在线节点取均值，离线节点的陈旧值不参与', () => {
    const s = summarizeNodes([
      node({ status: 1, cpuUsage: 0.2, memoryUsage: 0.4, diskUsage: 0.6 }),
      node({ status: 1, cpuUsage: 0.4, memoryUsage: 0.6, diskUsage: 0.8 }),
      node({ status: 0, cpuUsage: 0.99, memoryUsage: 0.99, diskUsage: 0.99 }), // 离线不计
    ])
    // (0.2+0.4)/2 = 0.3 → 30%
    expect(s.cpuPct).toBeCloseTo(30, 5)
    expect(s.memPct).toBeCloseTo(50, 5)
    expect(s.diskPct).toBeCloseTo(70, 5)
  })

  it('全部离线：聚合水位为 null', () => {
    const s = summarizeNodes([node({ status: 0, cpuUsage: 0.5 })])
    expect(s.online).toBe(0)
    expect(s.cpuPct).toBeNull()
  })
})
