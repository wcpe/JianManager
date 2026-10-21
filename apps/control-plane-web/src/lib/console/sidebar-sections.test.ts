/**
 * @file sidebar-sections 单测：分段、文件树与「虚拟化扁平行模型」。
 *
 * 重点覆盖虚拟化的两条硬约束（护栏要求）：
 * 1. 行高常量必须与真实渲染高度一一对应（否则窗口位置逐行漂移）；
 * 2. 铺平后的行序列必须与 `sections` / `treeLines` **逐项等价**（顺序、数量、层级缩进），
 *    否则虚拟化会把某一段静默吞掉。
 */
import { describe, expect, it } from 'vitest'

import {
  GROUP_HEAD_HEIGHT,
  SERVER_ROW_HEIGHT,
  buildGroupTreeLines,
  buildSections,
  flattenSidebarLines,
  type PanelRow,
} from './sidebar-sections'
import type { InstanceInfo } from '@/lib/instances/instance-types'
import type { InstanceGroupNode } from '@/lib/instances/instance-group'

/** 造一台最小可用实例（只填本模块读到的字段）。 */
function makeInstance(id: number, name: string, nodeId = 1): InstanceInfo {
  return {
    id,
    uuid: `u-${id}`,
    nodeId,
    name,
    type: 'minecraft_java',
    role: 'backend',
    processType: 'direct',
    status: 'RUNNING',
    startCommand: 'java -jar server.jar',
    workDir: `/srv/${name}`,
    serverPort: 25565,
    autoStart: false,
    autoRestart: true,
    tags: null,
    createdAt: '2024-01-01T00:00:00Z',
  } as InstanceInfo
}

const row = (inst: InstanceInfo): PanelRow => ({ kind: 'instance', inst })

function makeGroup(id: number, name: string, parentId: number | null, members: number[]): InstanceGroupNode {
  return {
    id,
    name,
    parentId,
    sort: id,
    memberInstanceIds: members,
    instanceCount: members.length,
  } as InstanceGroupNode
}

describe('侧栏虚拟化行高常量', () => {
  it('服务器行 32px、组头 26px（与两行紧凑内容 / 单行小字的实际高度一致）', () => {
    // 这两个数字直接决定 `useVirtualRows` 的窗口计算：改动 CSS 行高时必须同步改这里，
    // 否则千行列表滚到底部会看到空白带（漂移累积）。
    // 32 = 名称 14px + 备注 12px + 上下内边距 6px（用户要求比 44 / 36 更紧凑）。
    expect(SERVER_ROW_HEIGHT).toBe(32)
    expect(GROUP_HEAD_HEIGHT).toBe(26)
  })
})

describe('flattenSidebarLines', () => {
  const instances = [makeInstance(1, 'alpha'), makeInstance(2, 'beta', 2), makeInstance(3, 'gamma')]
  const rows = instances.map(row)
  const labels = { ungrouped: '未分组', pinned: '置顶' }
  const nodes = new Map([[1, 'node-main'], [2, 'node-east']])
  const nodeOrder = new Map([[1, 0], [2, 1]])

  it('分段模式：每段先出组头再出该段的服务器行（不丢行、顺序一致）', () => {
    const sections = buildSections({
      rows,
      mode: 'node',
      nodeLabelOf: nodes,
      nodeOrder,
      networkKeyOf: new Map(),
      favoriteOrder: new Map(),
      groupTree: [],
      labels,
    })
    const lines = flattenSidebarLines({ mode: 'node', sections, treeLines: [] })

    // 铺平后服务器的总数必须等于原始行数——少一台就说明虚拟化把某段吞了。
    expect(lines.filter((line) => line.kind === 'server')).toHaveLength(rows.length)
    // 组头数与段数一致，且每个组头都紧接在本段第一行之前。
    expect(lines.filter((line) => line.kind === 'section-head')).toHaveLength(sections.length)
    let cursor = 0
    for (const section of sections) {
      if (section.label !== '') {
        const head = lines[cursor]
        expect(head.kind).toBe('section-head')
        expect(head.kind === 'section-head' && head.label).toBe(section.label)
        expect(head.kind === 'section-head' && head.count).toBe(section.rows.length)
        cursor += 1
      }
      for (const expected of section.rows) {
        const line = lines[cursor]
        expect(line.kind).toBe('server')
        // 分段模式下缩进一律为 0：层级只由组头表达，行本身不再叠加缩进。
        expect(line.kind === 'server' && line.row).toBe(expected)
        expect(line.kind === 'server' && line.indent).toBe(0)
        cursor += 1
      }
    }
    expect(cursor).toBe(lines.length)
  })

  it('排序与分组正交：同一分组下按创建时间排序，段头不变、段内顺序改变', () => {
    // 「按创建时间排序」不再是分组维度（不再是单段无组头），而是段内比较器。
    const sections = buildSections({
      rows,
      mode: 'node',
      nodeLabelOf: nodes,
      nodeOrder,
      networkKeyOf: new Map(),
      labels,
      sort: 'created',
    })
    // 分组结果与默认排序时相同（两台在 node-main、一台在 node-east）。
    expect(sections).toHaveLength(2)
    const mainSection = sections.find((section) => section.label === 'node-main')
    expect(mainSection).toBeDefined()
    // beta/alpha/gamma 的 createdAt 全同（造数据所致），比较器退化为名称序 —— 断言它确实是**排过序**的。
    const names = mainSection!.rows.map((row) => (row.kind === 'instance' ? row.inst.name : ''))
    expect(names).toEqual([...names].sort((a, b) => a.localeCompare(b)))
  })

  it('排序：按状态把异常排在前面（运维扫侧栏先看出问题的）', () => {
    const mixed = [
      row({ ...makeInstance(1, 'alpha'), status: 'STOPPED' }),
      row({ ...makeInstance(2, 'beta'), status: 'RUNNING' }),
      row({ ...makeInstance(3, 'gamma'), status: 'CRASHED' }),
    ]
    const sections = buildSections({
      rows: mixed,
      mode: 'node',
      nodeLabelOf: nodes,
      nodeOrder,
      networkKeyOf: new Map(),
      labels,
      sort: 'status',
    })
    const mainSection = sections.find((section) => section.label === 'node-main')
    const order = mainSection!.rows.map((r) => (r.kind === 'instance' ? r.inst.status : ''))
    expect(order[0]).toBe('CRASHED')
    expect(order[order.length - 1]).toBe('STOPPED')
  })

  it('文件树模式：组头带层级与子树计数，实例行缩进随层级递进', () => {
    const groupTree = [
      makeGroup(1, '华东', null, [1]),
      makeGroup(2, '小区A', 1, [2]),
      makeGroup(3, '空组', null, []),
    ]
    const treeLines = buildGroupTreeLines({ rows, groupTree })
    const lines = flattenSidebarLines({ mode: 'groupTree', sections: [], treeLines })

    // 与树逐项对齐：数量、键、内容全部一致（虚拟化不得改变树序）。
    expect(lines).toHaveLength(treeLines.length)
    treeLines.forEach((expected, index) => {
      const line = lines[index]
      expect(line?.key).toBe(expected.key)
      if (expected.kind === 'group') {
        expect(line?.kind).toBe('group-branch')
        expect(line?.kind === 'group-branch' && line.label).toBe(expected.node.name)
        expect(line?.kind === 'group-branch' && line.depth).toBe(expected.depth)
        expect(line?.kind === 'group-branch' && line.count).toBe(expected.count)
      } else {
        expect(line?.kind).toBe('server')
        expect(line?.kind === 'server' && line.row).toBe(expected.row)
        // 根组下的实例缩进 1，子组下的缩进 2：层级阶梯读得出来。
        expect(line?.kind === 'server' && line.indent).toBe(expected.node.parentId === null ? 1 : 2)
      }
    })

    // 空组整枝不渲染（没有成员的组头只是噪音）。
    expect(lines.some((line) => line.kind === 'group-branch' && line.label === '空组')).toBe(false)
  })

  it('文件树为空时铺平结果为空（不产生占位行）', () => {
    expect(flattenSidebarLines({ mode: 'groupTree', sections: [], treeLines: [] })).toEqual([])
  })
})
