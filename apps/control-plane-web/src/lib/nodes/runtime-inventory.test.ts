import { describe, expect, it } from 'vitest'
import {
  buildRuntimeInventory,
  countByType,
  deleteBlockedText,
  describeRefs,
  filterRuntimeInventory,
  type JDKInventoryInput,
  type OtherRuntimeInput,
} from './runtime-inventory'

const jdk = (over: Partial<JDKInventoryInput> = {}): JDKInventoryInput => ({
  id: 1,
  vendor: 'Temurin',
  majorVersion: 21,
  version: '21.0.4',
  arch: 'x64',
  path: '/opt/jdks/temurin-21',
  managed: true,
  instances: [],
  refCount: 0,
  deleteBlocked: false,
  deleteBlockedReason: '',
  ...over,
})

const node = (over: Partial<OtherRuntimeInput> = {}): OtherRuntimeInput => ({
  id: 9,
  type: 'nodejs',
  name: 'Node.js 22',
  majorVersion: 22,
  version: '22.11.0',
  arch: 'x64',
  path: '/opt/runtimes/nodejs-22',
  managed: true,
  ...over,
})

describe('buildRuntimeInventory：合并', () => {
  it('JDK 与 Node 进同一张表，类型只作为列值（不再互斥过滤）', () => {
    const rows = buildRuntimeInventory([jdk()], [node()])
    expect(rows).toHaveLength(2)
    expect(rows.map((r) => r.type)).toEqual(['jdk', 'nodejs'])
  })

  it('key 前缀带类型：两表 id 可能重复，不能只用 id 当 key', () => {
    const rows = buildRuntimeInventory([jdk({ id: 5 })], [node({ id: 5 })])
    expect(rows.map((r) => r.key)).toEqual(['jdk-5', 'nodejs-5'])
  })

  it('后端若在 runtimes 里混入 type=jdk，不重复呈现（JDK 侧已带全量引用信息）', () => {
    const rows = buildRuntimeInventory([jdk()], [node({ id: 2, type: 'jdk', name: 'Temurin' })])
    expect(rows).toHaveLength(1)
    expect(rows[0]?.type).toBe('jdk')
  })

  it('排序：类型优先（JDK 在前），同类型按大版本倒序', () => {
    const rows = buildRuntimeInventory(
      [jdk({ id: 1, majorVersion: 17 }), jdk({ id: 2, majorVersion: 21 })],
      [node()],
    )
    expect(rows.map((r) => `${r.type}-${r.majorVersion}`)).toEqual(['jdk-21', 'jdk-17', 'nodejs-22'])
  })

  it('引用信息从 JDK 侧透传：refCount / deleteBlocked / refs 三项都要保留', () => {
    const rows = buildRuntimeInventory(
      [
        jdk({
          instances: [{ id: 7, uuid: 'u', name: 'survival', status: 'RUNNING', binding: 'direct' }],
          refCount: 1,
          deleteBlocked: true,
          deleteBlockedReason: 'direct',
        }),
      ],
      [],
    )
    expect(rows[0]?.refCount).toBe(1)
    expect(rows[0]?.deleteBlocked).toBe(true)
    expect(rows[0]?.refs[0]?.name).toBe('survival')
  })

  it('非 JDK 行的引用恒为空且不可被删拦（它们没有实例绑定语义）', () => {
    const rows = buildRuntimeInventory([], [node()])
    expect(rows[0]?.refs).toEqual([])
    expect(rows[0]?.deleteBlocked).toBe(false)
  })
})

describe('filterRuntimeInventory', () => {
  const rows = buildRuntimeInventory(
    [jdk({ id: 1, managed: true }), jdk({ id: 2, managed: false, vendor: 'Zulu', majorVersion: 17, version: '17.0.9', path: '/opt/jdks/zulu-17' })],
    [node()],
  )

  it('按类型筛选', () => {
    expect(filterRuntimeInventory(rows, { type: 'nodejs', source: 'all', query: '' })).toHaveLength(1)
    expect(filterRuntimeInventory(rows, { type: 'all', source: 'all', query: '' })).toHaveLength(3)
  })

  it('按来源筛选：托管 / 外部', () => {
    expect(filterRuntimeInventory(rows, { type: 'all', source: 'managed', query: '' })).toHaveLength(2)
    const ext = filterRuntimeInventory(rows, { type: 'all', source: 'external', query: '' })
    expect(ext).toHaveLength(1)
    expect(ext[0]?.name).toBe('Zulu')
  })

  it('关键词匹配厂商 / 版本 / 架构 / 路径，且大小写不敏感', () => {
    expect(filterRuntimeInventory(rows, { type: 'all', source: 'all', query: 'zulu' })).toHaveLength(1)
    expect(filterRuntimeInventory(rows, { type: 'all', source: 'all', query: '/opt/runtimes' })).toHaveLength(1)
    expect(filterRuntimeInventory(rows, { type: 'all', source: 'all', query: '21.0.4' })).toHaveLength(1)
    expect(filterRuntimeInventory(rows, { type: 'all', source: 'all', query: 'X64' })).toHaveLength(3)
  })

  it('空白关键词不筛掉任何行', () => {
    expect(filterRuntimeInventory(rows, { type: 'all', source: 'all', query: '   ' })).toHaveLength(3)
  })
})

describe('countByType', () => {
  it('计数按全量算（含 all），否则选中某类型后其它类型会恒为 0', () => {
    const rows = buildRuntimeInventory([jdk({ id: 1 }), jdk({ id: 2 })], [node()])
    expect(countByType(rows)).toEqual({ all: 3, jdk: 2, nodejs: 1 })
  })
})

describe('describeRefs / deleteBlockedText', () => {
  it('引用摘要分别统计直接绑定与大版本绑定', () => {
    expect(
      describeRefs([
        { id: 1, uuid: 'a', name: 'a', status: 'RUNNING', binding: 'direct' },
        { id: 2, uuid: 'b', name: 'b', status: 'STOPPED', binding: 'direct' },
        { id: 3, uuid: 'c', name: 'c', status: 'STOPPED', binding: 'major' },
      ]),
    ).toBe('2 直接 · 1 大版本')
  })

  it('引用摘要为空时返回空串（不显示「0 直接」这种噪声）', () => {
    expect(describeRefs([])).toBe('')
  })

  it('删除原因按后端 reason 分支；未被拦时返回 null', () => {
    const base = buildRuntimeInventory([jdk()], [])[0]!
    expect(deleteBlockedText(base)).toBeNull()
    expect(deleteBlockedText({ ...base, deleteBlocked: true, deleteBlockedReason: 'direct', refCount: 3 })).toBe(
      '被 3 个实例直接绑定',
    )
    expect(deleteBlockedText({ ...base, deleteBlocked: true, deleteBlockedReason: 'major' })).toContain(
      '大版本依赖',
    )
  })
})
