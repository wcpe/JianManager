import { describe, it, expect } from 'vitest'
import { searchPalette, type PaletteSources } from './command-palette'

const src: PaletteSources = {
  instances: [
    { id: 1, name: 'lobby', uuid: 'aaaa1111-x', status: 'RUNNING' },
    { id: 2, name: 'survival', uuid: 'bbbb2222-y', status: 'STOPPED' },
  ],
  nodes: [
    { id: 10, name: 'node-tokyo', host: '10.0.0.1' },
    { id: 11, name: 'node-osaka', host: '10.0.0.2' },
  ],
  pages: [
    { to: '/nodes', label: '节点' },
    { to: '/instances', label: '全部实例' },
  ],
  commands: [{ id: 'refresh', label: '刷新当前数据' }],
}

describe('searchPalette', () => {
  it('空查询返回各类默认（实例→节点→页面→操作有序）', () => {
    const r = searchPalette('', src)
    expect(r.map((e) => e.kind)).toEqual(['instance', 'instance', 'node', 'node', 'page', 'page', 'command'])
    expect(r[0].key).toBe('instance:1')
  })

  it('按实例名子串命中（大小写不敏感）', () => {
    const r = searchPalette('LOBBY', src)
    expect(r).toHaveLength(1)
    expect(r[0]).toMatchObject({ kind: 'instance', key: 'instance:1', label: 'lobby', status: 'RUNNING' })
  })

  it('按实例 UUID 子串命中', () => {
    const r = searchPalette('bbbb2222', src)
    expect(r.map((e) => e.key)).toEqual(['instance:2'])
  })

  it('按节点 host 子串命中', () => {
    const r = searchPalette('10.0.0.2', src)
    expect(r.map((e) => e.key)).toEqual(['node:11'])
  })

  it('按页面文案命中', () => {
    const r = searchPalette('全部实例', src)
    expect(r.map((e) => e.key)).toEqual(['page:/instances'])
  })

  it('每类按 limitPer 截断', () => {
    const many: PaletteSources = {
      instances: Array.from({ length: 20 }, (_, i) => ({ id: i, name: `inst${i}`, uuid: `u${i}`, status: 'RUNNING' })),
      nodes: [],
      pages: [],
      commands: [],
    }
    expect(searchPalette('inst', many, 5)).toHaveLength(5)
  })
})
