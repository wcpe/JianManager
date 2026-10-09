import { describe, it, expect } from 'vitest'
import type { NodeInfo } from '@/api/nodes'
import {
  nodeStatusLevel,
  filterNodes,
  resolveSelectedNode,
  loadNodeListCollapsed,
  persistNodeListCollapsed,
  NODE_LIST_COLLAPSED_KEY,
  type KVStorage,
} from './node-list'

/** 内存 storage（模拟 localStorage，vitest node 环境无 DOM）。 */
function memStorage(initial: Record<string, string> = {}): KVStorage & { data: Record<string, string> } {
  const data: Record<string, string> = { ...initial }
  return {
    data,
    getItem: (k) => (k in data ? data[k] : null),
    setItem: (k, v) => {
      data[k] = v
    },
  }
}

/** 构造最小节点对象（只填列表/筛选用到的字段）。 */
function node(p: Partial<NodeInfo>): NodeInfo {
  return {
    id: Math.floor(Math.random() * 1e9),
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

describe('nodeStatusLevel', () => {
  it('1 在线=success / 2 启动中=warning / 0 离线=danger', () => {
    expect(nodeStatusLevel(1)).toBe('success')
    expect(nodeStatusLevel(2)).toBe('warning')
    expect(nodeStatusLevel(0)).toBe('danger')
  })
})

describe('filterNodes', () => {
  const nodes = [
    node({ id: 1, name: 'alpha', host: '10.0.0.1' }),
    node({ id: 2, name: 'Beta', host: '10.0.0.2' }),
    node({ id: 3, name: 'gamma', host: '192.168.1.9' }),
  ]

  it('空查询返回原列表（同引用元素，不丢节点）', () => {
    expect(filterNodes(nodes, '')).toHaveLength(3)
    expect(filterNodes(nodes, '   ')).toHaveLength(3)
  })

  it('按名称大小写不敏感匹配', () => {
    expect(filterNodes(nodes, 'beta').map((n) => n.id)).toEqual([2])
    expect(filterNodes(nodes, 'ALPHA').map((n) => n.id)).toEqual([1])
  })

  it('按 host 子串匹配', () => {
    expect(filterNodes(nodes, '192.168').map((n) => n.id)).toEqual([3])
    expect(filterNodes(nodes, '10.0.0').map((n) => n.id)).toEqual([1, 2])
  })

  it('无匹配返回空数组', () => {
    expect(filterNodes(nodes, 'zzz')).toEqual([])
  })
})

describe('resolveSelectedNode', () => {
  const nodes = [node({ id: 1, name: 'a' }), node({ id: 2, name: 'b' })]

  it('未选（null）→ 返回 null', () => {
    expect(resolveSelectedNode(nodes, null)).toBeNull()
  })

  it('选中存在的节点 → 返回该节点的最新对象（按 id 命中实时列表）', () => {
    const fresh = [node({ id: 1, name: 'a', cpuUsage: 0.9 }), node({ id: 2, name: 'b' })]
    const got = resolveSelectedNode(fresh, 1)
    expect(got?.id).toBe(1)
    expect(got?.cpuUsage).toBe(0.9)
  })

  it('选中的节点已不在列表（被下线）→ 返回 null（右栏回空态）', () => {
    expect(resolveSelectedNode(nodes, 999)).toBeNull()
  })
})

describe('node list collapsed persistence', () => {
  it('默认未折叠（无持久值回退 false）', () => {
    expect(loadNodeListCollapsed(memStorage())).toBe(false)
  })

  it('无可用 storage（非 DOM）回退 false，不抛错', () => {
    expect(loadNodeListCollapsed(null)).toBe(false)
  })

  it('持久 true 后能读回', () => {
    const s = memStorage()
    persistNodeListCollapsed(true, s)
    expect(s.data[NODE_LIST_COLLAPSED_KEY]).toBe('1')
    expect(loadNodeListCollapsed(s)).toBe(true)
  })

  it('持久 false 后读回 false', () => {
    const s = memStorage()
    persistNodeListCollapsed(true, s)
    persistNodeListCollapsed(false, s)
    expect(loadNodeListCollapsed(s)).toBe(false)
  })
})
