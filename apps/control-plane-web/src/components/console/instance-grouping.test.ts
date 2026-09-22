import { describe, it, expect } from 'vitest'
import {
  envOf,
  freeTagsOf,
  collectEnvs,
  collectTags,
  groupInstances,
  groupInstancesByGroupTree,
  buildGroupTreeSource,
  groupTreeKey,
  regionOf,
  zoneOf,
  GROUP_DIMENSIONS,
  dimensionNeedsKeyMap,
  buildKeyMap,
  dimensionValueOf,
} from './instance-grouping'
import type { InstanceInfo } from '@/api/instances'
import type { InstanceGroupNode } from '@/api/instanceGroups'

function inst(
  id: number,
  nodeId: number,
  tags: string[] | null,
  status = 'RUNNING',
  over: Partial<InstanceInfo> = {},
): InstanceInfo {
  return {
    id,
    uuid: `u-${id}`,
    nodeId,
    name: `inst-${id}`,
    type: 'mc',
    role: 'backend',
    processType: 'daemon',
    status,
    startCommand: '',
    workDir: '',
    serverPort: 0,
    autoStart: false,
    autoRestart: false,
    tags,
    createdAt: '',
    ...over,
  }
}

describe('envOf / freeTagsOf', () => {
  it('提取首个 env 标签', () => {
    expect(envOf(inst(1, 1, ['survival', 'env:prod']))).toBe('prod')
    expect(envOf(inst(1, 1, ['env:dev', 'env:prod']))).toBe('dev')
  })
  it('无 env 标签返回空串', () => {
    expect(envOf(inst(1, 1, ['survival']))).toBe('')
    expect(envOf(inst(1, 1, null))).toBe('')
  })
  it('自由标签剔除 env: 前缀', () => {
    expect(freeTagsOf(inst(1, 1, ['env:prod', 'survival', 'eu']))).toEqual(['survival', 'eu'])
    expect(freeTagsOf(inst(1, 1, null))).toEqual([])
  })
})

describe('collectEnvs / collectTags', () => {
  const list = [
    inst(1, 1, ['env:prod', 'survival']),
    inst(2, 1, ['env:dev', 'survival']),
    inst(3, 2, ['env:prod', 'lobby']),
    inst(4, 2, null),
  ]
  it('收集去重排序的环境', () => {
    expect(collectEnvs(list)).toEqual(['dev', 'prod'])
  })
  it('收集去重排序的自由标签（不含 env）', () => {
    expect(collectTags(list)).toEqual(['lobby', 'survival'])
  })
})

describe('groupInstances', () => {
  const list = [
    inst(1, 1, ['env:prod'], 'RUNNING'),
    inst(2, 2, ['env:dev'], 'STOPPED'),
    inst(3, 1, null, 'RUNNING'),
  ]

  it('none 维度返回单一分组', () => {
    const g = groupInstances(list, 'none')
    expect(g).toHaveLength(1)
    expect(g[0].instances).toHaveLength(3)
  })

  it('按节点分组', () => {
    const g = groupInstances(list, 'node')
    expect(g.map((x) => x.key)).toEqual(['1', '2'])
    expect(g[0].instances.map((i) => i.id)).toEqual([1, 3])
  })

  it('按环境分组，未分环境排末尾', () => {
    const g = groupInstances(list, 'env')
    // dev / prod 字典序在前，空 key（未分环境）恒末尾
    expect(g.map((x) => x.key)).toEqual(['dev', 'prod', ''])
  })

  it('按状态分组', () => {
    const g = groupInstances(list, 'status')
    expect(g.map((x) => x.key)).toEqual(['RUNNING', 'STOPPED'])
  })
})

describe('regionOf / zoneOf', () => {
  it('提取 region:/zone: 标签', () => {
    expect(regionOf(inst(1, 1, ['env:prod', 'region:r1', 'zone:z1']))).toBe('r1')
    expect(zoneOf(inst(1, 1, ['region:r1', 'zone:z2']))).toBe('z2')
  })
  it('无标签返回空串', () => {
    expect(regionOf(inst(1, 1, ['env:prod']))).toBe('')
    expect(zoneOf(inst(1, 1, null))).toBe('')
  })
})

describe('groupInstances — region 两级', () => {
  const list = [
    inst(1, 1, ['region:r1', 'zone:z1']),
    inst(2, 1, ['region:r1', 'zone:z2']),
    inst(3, 2, ['region:r2', 'zone:z1']),
    inst(4, 2, ['region:r1']), // 有 region 无 zone
    inst(5, 1, null), // 无 region（未分组）
  ]

  it('外层按 region 字典序，未分组末尾', () => {
    const g = groupInstances(list, 'region')
    expect(g.map((x) => x.key)).toEqual(['r1', 'r2', ''])
  })

  it('内层按 zone 字典序，无 zone 的子组 key 为空且排末尾', () => {
    const g = groupInstances(list, 'region')
    const r1 = g.find((x) => x.key === 'r1')!
    expect(r1.children?.map((x) => x.key)).toEqual(['z1', 'z2', ''])
    // 外层 instances 为全部成员（跨 zone 扁平）。
    expect(r1.instances.map((i) => i.id).sort()).toEqual([1, 2, 4])
    // z1 子组仅 inst-1。
    expect(r1.children!.find((x) => x.key === 'z1')!.instances.map((i) => i.id)).toEqual([1])
  })

  it('未分组实例自身仍带 children（空 zone 子组）', () => {
    const g = groupInstances(list, 'region')
    const none = g.find((x) => x.key === '')!
    expect(none.instances.map((i) => i.id)).toEqual([5])
    expect(none.children?.map((x) => x.key)).toEqual([''])
  })

  it('单级 zone 维度，未分组末尾', () => {
    const g = groupInstances(list, 'zone')
    expect(g.map((x) => x.key)).toEqual(['z1', 'z2', ''])
  })
})

describe('groupInstances — role / type', () => {
  const list = [
    inst(1, 1, null, 'RUNNING', { role: 'proxy', type: 'minecraft_java' }),
    inst(2, 1, null, 'RUNNING', { role: 'backend', type: 'minecraft_java' }),
    inst(3, 1, null, 'RUNNING', { role: 'beacon', type: 'generic' }),
  ]
  it('按 role 分组', () => {
    const g = groupInstances(list, 'role')
    expect(g.map((x) => x.key)).toEqual(['backend', 'beacon', 'proxy'])
  })
  it('按 type 分组', () => {
    const g = groupInstances(list, 'type')
    expect(g.map((x) => x.key)).toEqual(['generic', 'minecraft_java'])
  })
  it('role/type 优先取能力画像，画像缺失回退实例字面字段（FR-445）', () => {
    // 权威画像与实例字面字段不一致时以画像为准。
    const withProfile = inst(1, 1, null, 'RUNNING', {
      role: 'backend',
      type: 'generic',
      capabilities: {
        type: 'minecraft_java',
        role: 'proxy',
        mcSemantics: false,
        capabilities: [],
      },
    })
    expect(dimensionValueOf(withProfile, 'role')).toBe('proxy')
    expect(dimensionValueOf(withProfile, 'type')).toBe('minecraft_java')
    // 画像缺失 → 回退字面字段。
    const noProfile = inst(2, 1, null, 'RUNNING', { role: 'beacon', type: 'generic' })
    expect(dimensionValueOf(noProfile, 'role')).toBe('beacon')
    expect(dimensionValueOf(noProfile, 'type')).toBe('generic')
  })
})

describe('groupInstances — network / groupTree（外部映射）', () => {
  const list = [inst(1, 1, null), inst(2, 1, null), inst(3, 1, null)]
  it('按映射落首组，未命中落未分组末尾', () => {
    const map = new Map<number, string[]>([
      [1, ['survival', 'creative']], // 多归属落首个
      [2, ['creative']],
    ])
    const g = groupInstances(list, 'network', map)
    expect(g.map((x) => x.key)).toEqual(['creative', 'survival', ''])
    expect(g.find((x) => x.key === 'survival')!.instances.map((i) => i.id)).toEqual([1])
    expect(g.find((x) => x.key === '')!.instances.map((i) => i.id)).toEqual([3])
  })
  it('无映射时全部落未分组', () => {
    const g = groupInstances(list, 'groupTree')
    expect(g.map((x) => x.key)).toEqual([''])
    expect(g[0].instances).toHaveLength(3)
  })
})

describe('buildKeyMap / dimensionValueOf', () => {
  it('由分组→成员构建实例→键映射（多归属保留全部）', () => {
    const map = buildKeyMap([
      { key: 'survival', instanceIds: [1, 2] },
      { key: 'creative', instanceIds: [2, 3] },
    ])
    expect(map.get(1)).toEqual(['survival'])
    expect(map.get(2)).toEqual(['survival', 'creative'])
    expect(map.get(3)).toEqual(['creative'])
  })
  it('dimensionValueOf 取首个映射键；无映射为空串', () => {
    const map = buildKeyMap([{ key: 'survival', instanceIds: [1] }])
    expect(dimensionValueOf(inst(1, 1, null), 'network', map)).toBe('survival')
    expect(dimensionValueOf(inst(9, 1, null), 'network', map)).toBe('')
  })
  it('dimensionValueOf 读 role/type/region/zone', () => {
    const i = inst(1, 1, ['region:r1', 'zone:z2'], 'RUNNING', { role: 'proxy', type: 'minecraft_java' })
    expect(dimensionValueOf(i, 'role')).toBe('proxy')
    expect(dimensionValueOf(i, 'type')).toBe('minecraft_java')
    expect(dimensionValueOf(i, 'region')).toBe('r1')
    expect(dimensionValueOf(i, 'zone')).toBe('z2')
  })
})

describe('GROUP_DIMENSIONS / dimensionNeedsKeyMap', () => {
  it('默认维度为 region 且覆盖 8+ 个维度', () => {
    expect(GROUP_DIMENSIONS[0]).toBe('region')
    expect(GROUP_DIMENSIONS).toEqual(
      expect.arrayContaining(['region', 'zone', 'groupTree', 'network', 'role', 'type', 'node', 'none']),
    )
  })
  it('仅 network/groupTree 需要外部映射', () => {
    expect(dimensionNeedsKeyMap('network')).toBe(true)
    expect(dimensionNeedsKeyMap('groupTree')).toBe(true)
    expect(dimensionNeedsKeyMap('region')).toBe(false)
    expect(dimensionNeedsKeyMap('role')).toBe(false)
  })
})

/** 组织分组树节点构造器（FR-452）。 */
function gnode(
  id: number,
  name: string,
  parentId: number | null,
  sort: number,
  memberInstanceIds: number[],
): InstanceGroupNode {
  return { id, uuid: `g-${id}`, name, parentId, sort, instanceCount: memberInstanceIds.length, memberInstanceIds }
}

describe('groupInstancesByGroupTree（FR-452 多级 + 组 id 键）', () => {
  // 两个同名组（「生存」）分属不同父：键用组 id → 不合并。
  const nodes: InstanceGroupNode[] = [
    gnode(1, '亚洲区', null, 0, []),
    gnode(2, '生存', 1, 0, [1]), // 亚洲区 / 生存
    gnode(3, '创造', 1, 1, [2]), // 亚洲区 / 创造
    gnode(4, '欧洲区', null, 1, [3]),
    gnode(5, '生存', 4, 0, [4]), // 欧洲区 / 生存（与 id2 同名不同父）
  ]
  const list = [
    inst(1, 1, null),
    inst(2, 1, null),
    inst(3, 1, null),
    inst(4, 1, null),
    inst(9, 1, null), // 未归属任何组
  ]

  it('键为组 id（同名不同组不合并），并重建多级层级', () => {
    const groups = groupInstancesByGroupTree(list, nodes)
    // 顶层为两个根组（亚洲区 / 欧洲区），各自带子组；未分组恒末尾。
    expect(groups.map((g) => g.key)).toEqual([groupTreeKey(1), groupTreeKey(4), ''])
    const asia = groups.find((g) => g.key === groupTreeKey(1))!
    expect(asia.children?.map((c) => c.key)).toEqual([groupTreeKey(2), groupTreeKey(3)])
    // 同为「生存」名的两个组各自独立成组（id2 在亚洲区下、id5 在欧洲区下）。
    const eu = groups.find((g) => g.key === groupTreeKey(4))!
    expect(eu.children?.map((c) => c.key)).toEqual([groupTreeKey(5)])
    expect(groupTreeKey(2)).not.toBe(groupTreeKey(5))
  })

  it('父组 instances 为子树并集、direct 仅本组直接成员', () => {
    const groups = groupInstancesByGroupTree(list, nodes)
    const asia = groups.find((g) => g.key === groupTreeKey(1))!
    // 亚洲区无直接成员，但两个子组各 1 台 → 子树并集 2 台。
    expect(asia.direct).toEqual([])
    expect(asia.instances.map((i) => i.id).sort()).toEqual([1, 2])
    const survival = asia.children!.find((c) => c.key === groupTreeKey(2))!
    expect(survival.direct?.map((i) => i.id)).toEqual([1])
    expect(survival.instances.map((i) => i.id)).toEqual([1])
  })

  it('未归属实例落末尾 key="" 分组', () => {
    const groups = groupInstancesByGroupTree(list, nodes)
    const none = groups.find((g) => g.key === '')!
    expect(none.instances.map((i) => i.id)).toEqual([9])
  })

  it('空组树（数据源不可用）退化为全「未分组」，不借错数据源', () => {
    const groups = groupInstancesByGroupTree(list, [])
    expect(groups).toHaveLength(1)
    expect(groups[0].key).toBe('')
    expect(groups[0].instances.map((i) => i.id).sort()).toEqual([1, 2, 3, 4, 9])
  })

  it('无成员的组不出现在结果（不产生空组头）', () => {
    // 唯一实例属「欧洲区 / 生存」(id5)；亚洲区整棵子树无成员 → 不出现；未分组无实例 → 不出现。
    const groups = groupInstancesByGroupTree([inst(4, 1, null)], nodes)
    expect(groups.map((g) => g.key)).toEqual([groupTreeKey(4)])
    const eu = groups.find((g) => g.key === groupTreeKey(4))!
    expect(eu.children?.map((c) => c.key)).toEqual([groupTreeKey(5)])
    expect(eu.direct).toEqual([])
    expect(eu.instances.map((i) => i.id)).toEqual([4])
  })

  it('有成员的父组保留、无成员的兄弟子组被裁掉', () => {
    const groups = groupInstancesByGroupTree([inst(1, 1, null)], nodes)
    expect(groups.map((g) => g.key)).toEqual([groupTreeKey(1)])
    const asia = groups.find((g) => g.key === groupTreeKey(1))!
    // 亚洲区仅「生存」(id2) 子组有成员；无成员的「创造」(id3) 子组被裁掉。
    expect(asia.children?.map((c) => c.key)).toEqual([groupTreeKey(2)])
  })
})

describe('buildGroupTreeSource', () => {
  const nodes: InstanceGroupNode[] = [
    gnode(1, '亚洲区', null, 0, []),
    gnode(2, '生存', 1, 0, [10, 11]),
    gnode(3, '欧洲区', null, 1, [12]),
  ]
  it('实例→组键（id 键）与组键展示元数据（组名/祖先路径/树序）', () => {
    const src = buildGroupTreeSource(nodes)
    expect(src.keys.get(10)).toEqual([groupTreeKey(2)])
    expect(src.keys.get(11)).toEqual([groupTreeKey(2)])
    expect(src.keys.get(12)).toEqual([groupTreeKey(3)])
    expect(src.labels.get(groupTreeKey(2))).toMatchObject({ name: '生存', parent: '亚洲区', depth: 1 })
    expect(src.labels.get(groupTreeKey(3))).toMatchObject({ name: '欧洲区', depth: 0 })
    expect(src.labels.get(groupTreeKey(3))?.parent).toBeUndefined()
    // 树前序：亚洲区(0) < 生存(1) < 欧洲区(2)。
    expect(src.labels.get(groupTreeKey(1))!.order).toBeLessThan(src.labels.get(groupTreeKey(2))!.order)
    expect(src.labels.get(groupTreeKey(2))!.order).toBeLessThan(src.labels.get(groupTreeKey(3))!.order)
  })
  it('空节点列表 → 空映射（该维度退化为全未分组）', () => {
    const src = buildGroupTreeSource([])
    expect(src.keys.size).toBe(0)
    expect(src.labels.size).toBe(0)
  })
})
