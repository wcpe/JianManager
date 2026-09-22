import { describe, it, expect } from 'vitest'
import {
  buildTopology,
  layoutTopology,
  memberHealth,
  memberHealthFromStatus,
  groupTopology,
  groupTopologyByDimension,
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

/** FR-453 全量实例投影（拓扑孤立节点来源）的最小构造器。 */
function inst(
  id: number,
  name: string,
  opts: { role?: string; type?: string; status?: string; tags?: string[] | null; nodeId?: number } = {},
): InstanceInfo {
  return {
    id,
    uuid: `u-${id}`,
    nodeId: opts.nodeId ?? 1,
    name,
    type: opts.type ?? 'minecraft_java',
    role: opts.role ?? 'backend',
    processType: 'daemon',
    status: opts.status ?? 'RUNNING',
    startCommand: '',
    workDir: '',
    serverPort: 25000 + id,
    autoStart: false,
    autoRestart: false,
    tags: opts.tags ?? null,
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

  it('FR-453：未注册实例作为孤立节点保留（不出连线）', () => {
    const g = buildTopology(
      [{ proxy: proxy(1, 'p'), registrations: [reg(10, 1, 100)] }],
      [
        inst(1, 'p', { role: 'proxy', status: 'RUNNING' }),
        inst(40, 'beacon-cp', { role: 'beacon', type: 'generic', status: 'RUNNING' }),
        inst(41, 'orphan-backend', { role: 'backend', status: 'STOPPED' }),
      ],
    )
    // 已注册的 proxy(1)/backend(100) 不重复；孤立 beacon/orphan 上带。
    expect(g.nodes.map((n) => n.id).sort((a, b) => a - b)).toEqual([1, 40, 41, 100])
    const beacon = g.nodes.find((n) => n.id === 40)!
    expect(beacon.name).toBe('beacon-cp')
    // beacon 非 proxy → backend 列（布局唯一二列）。
    expect(beacon.kind).toBe('backend')
    expect(beacon.registrationCount).toBe(0)
    expect(g.edges).toHaveLength(1)
  })
})

describe('groupTopologyByDimension', () => {
  const instances = [
    inst(1, 'velocity-a', { role: 'proxy', tags: ['region:r1', 'zone:z1'] }),
    inst(2, 'lobby', { role: 'backend', tags: ['region:r1', 'zone:z2'] }),
    inst(3, 'creative', { role: 'backend', tags: ['region:r2', 'zone:z1'] }),
    inst(40, 'beacon-cp', { role: 'beacon', type: 'generic' }),
  ]
  const topo = buildTopology(
    [{ proxy: proxy(1, 'velocity-a'), registrations: [reg(10, 1, 2, { bName: 'lobby' })] }],
    instances,
  )

  it('region 维度：层级按 region→zone 两级带，未分组末尾', () => {
    const g = groupTopologyByDimension(topo, 'region', instances)
    // r1/z1, r1/z2, r2/z1 先（按大区再小区）；未分组（beacon 无 region 标签）末尾。
    expect(g.bands.map((b) => b.name)).toEqual(['z1', 'z2', 'z1', ''])
    expect(g.bands.map((b) => b.parent)).toEqual(['r1', 'r1', 'r2', undefined])
    expect(g.bands[0].nodes.map((n) => n.id)).toEqual([1])
    expect(g.bands[1].nodes.map((n) => n.id)).toEqual([2])
    expect(g.bands[2].nodes.map((n) => n.id)).toEqual([3])
    expect(g.bands[3].nodes.map((n) => n.id)).toEqual([40])
  })

  it('role 维度：层级随维度变化（proxy/backend/beacon）', () => {
    const g = groupTopologyByDimension(topo, 'role', instances)
    expect(g.bands.map((b) => b.name)).toEqual(['backend', 'beacon', 'proxy'])
    expect(g.bands[2].nodes.map((n) => n.id)).toEqual([1])
  })

  it('type 维度：generic 与 minecraft_java', () => {
    const g = groupTopologyByDimension(topo, 'type', instances)
    expect(g.bands.map((b) => b.name)).toEqual(['generic', 'minecraft_java'])
  })

  it('network 维度：按外部映射落首带，未命中落未分组末尾且记多归属', () => {
    const keys = new Map<number, string[]>([
      [1, ['survival', 'creative']],
      [2, ['survival']],
    ])
    const g = groupTopologyByDimension(topo, 'network', instances, keys)
    expect(g.bands.map((b) => b.name)).toEqual(['creative', 'survival', ''])
    expect(g.multiHomed.has(1)).toBe(true)
    expect(g.multiHomed.has(2)).toBe(false)
    expect(g.bands[2].nodes.map((n) => n.id)).toEqual([3, 40].sort((a, b) => a - b))
  })

  it('none 维度：单带含全部节点，无未分组带', () => {
    const g = groupTopologyByDimension(topo, 'none', instances)
    expect(g.bands).toHaveLength(1)
    expect(g.bands[0].name).toBe('')
    expect(g.bands[0].nodes).toHaveLength(4)
  })

  it('带键唯一（region 两级用 region:r/zone:z 组合键）', () => {
    const g = groupTopologyByDimension(topo, 'region', instances)
    const keys = g.bands.map((b) => b.key)
    expect(new Set(keys).size).toBe(keys.length)
    expect(keys[0]).toBe('region:r1/zone:z1')
  })

  it('region 维度：有 region 无 zone 的成员空名带与该 region 的 zone 带同外层、排在 zone 之后（与列表 zoneNone 同序）', () => {
    const withNoZone = [
      inst(1, 'velocity-a', { role: 'proxy', tags: ['region:r1', 'zone:z1'] }),
      inst(2, 'lobby', { role: 'backend', tags: ['region:r1'] }), // 有 region 无 zone
      inst(50, 'no-region', { role: 'backend' }), // 无 region → 未分组
    ]
    const t2 = buildTopology([], withNoZone)
    const g = groupTopologyByDimension(t2, 'region', withNoZone)
    // r1/z1 → r1/（未分小区）→ 未分组，三层顺序与列表「大区 → 小区 → 未分小区 → 未分大区」一致。
    expect(g.bands.map((b) => ({ name: b.name, parent: b.parent }))).toEqual([
      { name: 'z1', parent: 'r1' },
      { name: '', parent: 'r1' },
      { name: '', parent: undefined },
    ])
  })

  it('groupTree 维度：键用组 id + labels 提供组名/祖先路径，按树前序排序，未命中落未分组末尾', () => {
    const nodes = [
      inst(1, 'velocity-a', { role: 'proxy' }),
      inst(2, 'lobby', { role: 'backend' }),
      inst(3, 'creative', { role: 'backend' }),
      inst(40, 'beacon-cp', { role: 'beacon', type: 'generic' }),
    ]
    const t2 = buildTopology([], nodes)
    const keys = new Map<number, string[]>([
      [1, ['g:2']],
      [2, ['g:5']],
      [3, ['g:3']],
    ])
    const labels = new Map<string, { name: string; parent?: string; order: number; depth: number }>([
      ['g:2', { name: '生存', parent: '亚洲区', order: 1, depth: 1 }],
      ['g:3', { name: '创造', parent: '亚洲区', order: 2, depth: 1 }],
      ['g:5', { name: '生存', parent: '欧洲区', order: 4, depth: 1 }],
    ])
    const g = groupTopologyByDimension(t2, 'groupTree', nodes, keys, labels)
    // 带名为组名（非组键）、parent 为祖先路径；按树前序（g:2 → g:3 → g:5）而非名称排序。
    expect(g.bands.map((b) => ({ key: b.key, name: b.name, parent: b.parent }))).toEqual([
      { key: 'dim:groupTree:g:2', name: '生存', parent: '亚洲区' },
      { key: 'dim:groupTree:g:3', name: '创造', parent: '亚洲区' },
      { key: 'dim:groupTree:g:5', name: '生存', parent: '欧洲区' },
      { key: '', name: '', parent: undefined },
    ])
    // 同名不同组各自成带（键唯一）。
    expect(new Set(g.bands.map((b) => b.key)).size).toBe(g.bands.length)
    // 未命中任何组的 beacon 落未分组。
    expect(g.bands[g.bands.length - 1].nodes.map((n) => n.id)).toEqual([40])
  })

  it('groupTree 维度：数据源不可用（无映射/无 labels）→ 全部落未分组，不借错数据源', () => {
    const g = groupTopologyByDimension(topo, 'groupTree', instances)
    expect(g.bands).toHaveLength(1)
    expect(g.bands[0].name).toBe('')
    expect(g.bands[0].key).toBe('')
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
