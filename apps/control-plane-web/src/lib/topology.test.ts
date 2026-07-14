import { describe, it, expect } from 'vitest'
import {
  buildTopology,
  layoutTopology,
  memberHealth,
  memberHealthFromStatus,
  groupTopology,
  layoutTopologyGrouped,
  edgeLevel,
  type ProxyRegistrations,
  type TopoGroupBrief,
} from './topology'
import type { Registration } from '@/api/registrations'
import type { InstanceInfo } from '@/api/instances'
import type { NetworkMember } from '@/api/networks'

function proxy(id: number, name: string, status = 'RUNNING'): InstanceInfo {
  return {
    id,
    uuid: `u-${id}`,
    nodeId: 1,
    name,
    type: 'mc',
    role: 'proxy',
    processType: 'daemon',
    status,
    startCommand: '',
    workDir: '',
    serverPort: 25565,
    autoStart: false,
    autoRestart: false,
    tags: null,
    createdAt: '',
  }
}

function reg(
  id: number,
  proxyId: number,
  backendId: number,
  opts: Partial<Registration> & { bName?: string; bStatus?: string; bPort?: number; bNode?: number } = {},
): Registration {
  return {
    id,
    proxyId,
    backendId,
    alias: opts.alias ?? `b${backendId}`,
    priority: opts.priority ?? 0,
    forcedHost: opts.forcedHost ?? '',
    restricted: opts.restricted ?? false,
    enabled: opts.enabled ?? true,
    backend: {
      id: backendId,
      name: opts.bName ?? `backend-${backendId}`,
      role: 'backend',
      nodeId: opts.bNode ?? 2,
      serverPort: opts.bPort ?? 30000 + backendId,
      status: opts.bStatus ?? 'RUNNING',
    },
  }
}

describe('edgeLevel', () => {
  it('禁用的注册恒为 neutral，不论后端状态', () => {
    expect(edgeLevel(false, 'RUNNING')).toBe('neutral')
    expect(edgeLevel(false, 'CRASHED')).toBe('neutral')
  })
  it('启用时跟随后端运行状态等级', () => {
    expect(edgeLevel(true, 'RUNNING')).toBe('success')
    expect(edgeLevel(true, 'CRASHED')).toBe('danger')
    expect(edgeLevel(true, 'STARTING')).toBe('warning')
    expect(edgeLevel(true, 'STOPPED')).toBe('neutral')
  })
})

describe('buildTopology', () => {
  const input: ProxyRegistrations[] = [
    { proxy: proxy(1, 'velocity-a'), registrations: [reg(10, 1, 100), reg(11, 1, 101)] },
    { proxy: proxy(2, 'velocity-b'), registrations: [reg(20, 2, 101), reg(21, 2, 102)] },
  ]

  it('proxy 节点来自入参 proxies', () => {
    const g = buildTopology(input)
    const proxies = g.nodes.filter((n) => n.kind === 'proxy')
    expect(proxies.map((n) => n.id)).toEqual([1, 2])
    expect(proxies[0].name).toBe('velocity-a')
  })

  it('backend 节点跨 proxy 去重（共享后端只出现一次）', () => {
    const g = buildTopology(input)
    const backends = g.nodes.filter((n) => n.kind === 'backend')
    // 100,101,102 三个唯一 backend；101 被两个 proxy 共享
    expect(backends.map((n) => n.id).sort()).toEqual([100, 101, 102])
  })

  it('每条注册一条 edge（M:N，共享后端产生多条 edge 一个节点）', () => {
    const g = buildTopology(input)
    expect(g.edges).toHaveLength(4)
    const shared = g.edges.filter((e) => e.backendId === 101)
    expect(shared.map((e) => e.proxyId).sort()).toEqual([1, 2])
  })

  it('backend 节点带状态/端口/节点与被注册次数', () => {
    const g = buildTopology([
      { proxy: proxy(1, 'p'), registrations: [reg(10, 1, 100, { bPort: 30001, bStatus: 'CRASHED', bNode: 5 })] },
    ])
    const b = g.nodes.find((n) => n.id === 100 && n.kind === 'backend')!
    expect(b.port).toBe(30001)
    expect(b.status).toBe('CRASHED')
    expect(b.nodeId).toBe(5)
    expect(b.registrationCount).toBe(1)
  })

  it('edge 等级随启用态与后端状态', () => {
    const g = buildTopology([
      {
        proxy: proxy(1, 'p'),
        registrations: [
          reg(10, 1, 100, { enabled: true, bStatus: 'RUNNING' }),
          reg(11, 1, 101, { enabled: false, bStatus: 'RUNNING' }),
        ],
      },
    ])
    expect(g.edges.find((e) => e.backendId === 100)!.level).toBe('success')
    expect(g.edges.find((e) => e.backendId === 101)!.level).toBe('neutral')
  })

  it('无 proxy 时空图', () => {
    const g = buildTopology([])
    expect(g.nodes).toHaveLength(0)
    expect(g.edges).toHaveLength(0)
  })

  it('proxy 自身状态归一为节点状态等级', () => {
    const g = buildTopology([{ proxy: proxy(1, 'p', 'CRASHED'), registrations: [] }])
    const p = g.nodes.find((n) => n.kind === 'proxy')!
    expect(p.status).toBe('CRASHED')
  })

  it('容错缺失的 backend 概要（仅有 backendId）', () => {
    const bare: Registration = {
      id: 9,
      proxyId: 1,
      backendId: 99,
      alias: 'x',
      priority: 0,
      forcedHost: '',
      restricted: false,
      enabled: true,
    }
    const g = buildTopology([{ proxy: proxy(1, 'p'), registrations: [bare] }])
    const b = g.nodes.find((n) => n.id === 99 && n.kind === 'backend')!
    expect(b.name).toBe('#99')
    expect(g.edges).toHaveLength(1)
  })
})

describe('layoutTopology', () => {
  it('proxy 左列 / backend 右列，x 分两列', () => {
    const g = buildTopology([
      { proxy: proxy(1, 'p1'), registrations: [reg(10, 1, 100), reg(11, 1, 101)] },
    ])
    const laid = layoutTopology(g, { width: 600, rowHeight: 60, nodeWidth: 140, paddingY: 20 })
    const px = laid.nodes.filter((n) => n.kind === 'proxy').map((n) => n.x)
    const bx = laid.nodes.filter((n) => n.kind === 'backend').map((n) => n.x)
    // 所有 proxy 同一 x，所有 backend 同一 x，且 proxy 在左
    expect(new Set(px).size).toBe(1)
    expect(new Set(bx).size).toBe(1)
    expect(px[0]).toBeLessThan(bx[0])
  })

  it('同列节点 y 不重叠且按序递增', () => {
    const g = buildTopology([
      { proxy: proxy(1, 'p1'), registrations: [reg(10, 1, 100), reg(11, 1, 101), reg(12, 1, 102)] },
    ])
    const laid = layoutTopology(g, { width: 600, rowHeight: 60, nodeWidth: 140, paddingY: 20 })
    const bys = laid.nodes.filter((n) => n.kind === 'backend').map((n) => n.y)
    for (let i = 1; i < bys.length; i++) expect(bys[i]).toBeGreaterThan(bys[i - 1])
  })

  it('高度按较多一侧的行数推算', () => {
    const g = buildTopology([
      { proxy: proxy(1, 'p1'), registrations: [reg(10, 1, 100), reg(11, 1, 101), reg(12, 1, 102)] },
      { proxy: proxy(2, 'p2'), registrations: [] },
    ])
    const laid = layoutTopology(g, { width: 600, rowHeight: 60, nodeWidth: 140, paddingY: 20 })
    // 右列 3 个 backend 是较多侧 → 高度 = 3*60 + 2*20
    expect(laid.height).toBe(3 * 60 + 2 * 20)
  })

  it('edge 端点坐标解析到对应节点中心', () => {
    const g = buildTopology([{ proxy: proxy(1, 'p1'), registrations: [reg(10, 1, 100)] }])
    const laid = layoutTopology(g, { width: 600, rowHeight: 60, nodeWidth: 140, paddingY: 20 })
    const e = laid.edges[0]
    const p = laid.nodes.find((n) => n.kind === 'proxy' && n.id === 1)!
    const b = laid.nodes.find((n) => n.kind === 'backend' && n.id === 100)!
    // 连线从 proxy 右边缘到 backend 左边缘
    expect(e.x1).toBeCloseTo(p.x + nodeHalf(140))
    expect(e.x2).toBeCloseTo(b.x - nodeHalf(140))
    expect(e.y1).toBeCloseTo(p.y)
    expect(e.y2).toBeCloseTo(b.y)
  })

  it('空图高度退化为最小一行', () => {
    const laid = layoutTopology(buildTopology([]), { width: 600, rowHeight: 60, nodeWidth: 140, paddingY: 20 })
    expect(laid.height).toBe(60)
    expect(laid.nodes).toHaveLength(0)
  })
})

function nodeHalf(w: number) {
  return w / 2
}

describe('memberHealth', () => {
  function member(status: string): NetworkMember {
    return { instanceId: 1, name: 'm', role: 'backend', nodeId: 1, status }
  }
  it('按运行/崩溃/过渡/停止分桶并计总', () => {
    const h = memberHealth([
      member('RUNNING'),
      member('RUNNING'),
      member('CRASHED'),
      member('STARTING'),
      member('STOPPED'),
    ])
    expect(h.total).toBe(5)
    expect(h.running).toBe(2)
    expect(h.crashed).toBe(1)
    expect(h.transitioning).toBe(1)
    expect(h.stopped).toBe(1)
  })
  it('空集合全 0', () => {
    const h = memberHealth([])
    expect(h).toEqual({ total: 0, running: 0, crashed: 0, transitioning: 0, stopped: 0 })
  })
  it('未知状态计入 stopped 桶（中性）', () => {
    const h = memberHealth([member('WEIRD')])
    expect(h.stopped).toBe(1)
    expect(h.total).toBe(1)
  })
})

describe('memberHealthFromStatus', () => {
  it('五态桶 → 健康分布（starting+stopping 合入 transitioning）', () => {
    const h = memberHealthFromStatus({ running: 2, stopped: 1, crashed: 3, starting: 1, stopping: 2 })
    expect(h.running).toBe(2)
    expect(h.stopped).toBe(1)
    expect(h.crashed).toBe(3)
    expect(h.transitioning).toBe(3) // 1 + 2
    expect(h.total).toBe(9)
  })
  it('全 0 → total 0', () => {
    const h = memberHealthFromStatus({ running: 0, stopped: 0, crashed: 0, starting: 0, stopping: 0 })
    expect(h).toEqual({ total: 0, running: 0, crashed: 0, transitioning: 0, stopped: 0 })
  })
})

describe('groupTopology', () => {
  const topo = buildTopology([
    { proxy: proxy(1, 'p1'), registrations: [reg(10, 1, 100), reg(11, 1, 101)] },
    { proxy: proxy(2, 'p2'), registrations: [reg(20, 2, 102)] },
  ])

  it('节点按 network 成员归属落带；未归属入未分组兜底带', () => {
    const num = (a: number, b: number) => a - b
    const groups: TopoGroupBrief[] = [{ id: 5, name: 'survival', memberInstanceIds: [1, 100] }]
    const g = groupTopology(topo, groups)
    // 首带 survival 含 p1(1) + backend 100；其余（p2、101、102）入未分组带。
    expect(g.bands).toHaveLength(2)
    expect(g.bands[0].id).toBe(5)
    expect(g.bands[0].nodes.map((n) => n.id).sort(num)).toEqual([1, 100])
    expect(g.bands[1].id).toBeNull()
    expect(g.bands[1].nodes.map((n) => n.id).sort(num)).toEqual([2, 101, 102])
  })

  it('多归属节点落首带并记入 multiHomed', () => {
    const groups: TopoGroupBrief[] = [
      { id: 5, name: 'a', memberInstanceIds: [100] },
      { id: 6, name: 'b', memberInstanceIds: [100] },
    ]
    const g = groupTopology(topo, groups)
    expect(g.multiHomed.has(100)).toBe(true)
    // 100 只出现在首带 a，不重复渲染到 b。
    const bandA = g.bands.find((b) => b.id === 5)!
    expect(bandA.nodes.some((n) => n.id === 100)).toBe(true)
    const bandB = g.bands.find((b) => b.id === 6)
    expect(bandB).toBeUndefined() // b 无独占节点 → 空带不出现
  })

  it('无分组时全部节点入未分组带', () => {
    const g = groupTopology(topo, [])
    expect(g.bands).toHaveLength(1)
    expect(g.bands[0].id).toBeNull()
    expect(g.bands[0].nodes).toHaveLength(topo.nodes.length)
    expect(g.multiHomed.size).toBe(0)
  })

  it('连线原样携带', () => {
    const g = groupTopology(topo, [{ id: 5, name: 'a', memberInstanceIds: [1] }])
    expect(g.edges).toEqual(topo.edges)
  })
})

describe('layoutTopologyGrouped', () => {
  const topo = buildTopology([
    { proxy: proxy(1, 'p1'), registrations: [reg(10, 1, 100), reg(11, 1, 101)] },
    { proxy: proxy(2, 'p2'), registrations: [reg(20, 2, 102)] },
  ])
  const opts = {
    width: 600,
    rowHeight: 44,
    nodeWidth: 168,
    paddingY: 18,
    bandHeaderHeight: 26,
    bandGap: 20,
  }

  it('多带纵向堆叠：后带 y 严格大于前带', () => {
    const groups: TopoGroupBrief[] = [
      { id: 5, name: 'a', memberInstanceIds: [1, 100] },
      { id: 6, name: 'b', memberInstanceIds: [2, 102] },
    ]
    const laid = layoutTopologyGrouped(groupTopology(topo, groups), opts)
    expect(laid.bands.length).toBeGreaterThanOrEqual(2)
    for (let i = 1; i < laid.bands.length; i++) {
      expect(laid.bands[i].y).toBeGreaterThan(laid.bands[i - 1].y)
    }
  })

  it('带内 proxy 左列 / backend 右列', () => {
    const laid = layoutTopologyGrouped(groupTopology(topo, []), opts)
    const px = laid.nodes.filter((n) => n.kind === 'proxy').map((n) => n.x)
    const bx = laid.nodes.filter((n) => n.kind === 'backend').map((n) => n.x)
    expect(new Set(px).size).toBe(1)
    expect(new Set(bx).size).toBe(1)
    expect(px[0]).toBeLessThan(bx[0])
  })

  it('总高度 = 各带高之和 + 带间隔（不随节点线性单列膨胀）', () => {
    const groups: TopoGroupBrief[] = [
      { id: 5, name: 'a', memberInstanceIds: [1, 100] },
      { id: 6, name: 'b', memberInstanceIds: [2, 102] },
    ]
    const laid = layoutTopologyGrouped(groupTopology(topo, groups), opts)
    const bandsHeight = laid.bands.reduce((sum, b) => sum + b.height, 0)
    // 带间各留一个 bandGap（n 带 → n-1 个间隔）。
    expect(laid.height).toBeCloseTo(bandsHeight + (laid.bands.length - 1) * opts.bandGap)
  })

  it('节点落在其所属带的垂直区间内', () => {
    const groups: TopoGroupBrief[] = [
      { id: 5, name: 'a', memberInstanceIds: [1, 100] },
      { id: 6, name: 'b', memberInstanceIds: [2, 102] },
    ]
    const grouped = groupTopology(topo, groups)
    const laid = layoutTopologyGrouped(grouped, opts)
    // p2(2) 属第二带，其 y 应落在第二带区间内。
    const p2 = laid.nodes.find((n) => n.kind === 'proxy' && n.id === 2)!
    const band2 = laid.bands[1]
    expect(p2.y).toBeGreaterThan(band2.y)
    expect(p2.y).toBeLessThan(band2.y + band2.height)
  })

  it('edge 端点解析到节点中心（可跨带）', () => {
    const groups: TopoGroupBrief[] = [{ id: 5, name: 'a', memberInstanceIds: [1] }]
    const grouped = groupTopology(topo, groups)
    const laid = layoutTopologyGrouped(grouped, opts)
    const e = laid.edges.find((x) => x.proxyId === 1 && x.backendId === 100)!
    const p = laid.nodes.find((n) => n.kind === 'proxy' && n.id === 1)!
    const b = laid.nodes.find((n) => n.kind === 'backend' && n.id === 100)!
    expect(e.x1).toBeCloseTo(p.x + 84)
    expect(e.x2).toBeCloseTo(b.x - 84)
    expect(e.y1).toBeCloseTo(p.y)
    expect(e.y2).toBeCloseTo(b.y)
  })
})
