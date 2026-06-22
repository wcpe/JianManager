import { describe, it, expect } from 'vitest'
import { dirOf, baseNameOf, groupDiscovered, type DiscoveredConfig } from './discover'

describe('dirOf / baseNameOf', () => {
  it('根文件', () => {
    expect(dirOf('server.properties')).toBe('')
    expect(baseNameOf('server.properties')).toBe('server.properties')
  })
  it('嵌套', () => {
    expect(dirOf('plugins/Foo/config.yml')).toBe('plugins/Foo')
    expect(baseNameOf('plugins/Foo/config.yml')).toBe('config.yml')
  })
})

describe('groupDiscovered', () => {
  const files: DiscoveredConfig[] = [
    { path: 'plugins/Foo/config.yml', format: 'yaml', supported: false },
    { path: 'server.properties', format: 'properties', supported: true },
    { path: 'plugins/Bar/a.yml', format: 'yaml', supported: false },
    { path: 'bukkit.yml', format: 'yaml', supported: true },
  ]

  it('根目录组排最前', () => {
    const groups = groupDiscovered(files)
    expect(groups[0].dir).toBe('')
  })

  it('根目录组内按文件名排序', () => {
    const groups = groupDiscovered(files)
    expect(groups[0].files.map((f) => f.path)).toEqual(['bukkit.yml', 'server.properties'])
  })

  it('子目录组按目录字典序', () => {
    const groups = groupDiscovered(files)
    const dirs = groups.map((g) => g.dir)
    expect(dirs).toEqual(['', 'plugins/Bar', 'plugins/Foo'])
  })

  it('空输入返回空', () => {
    expect(groupDiscovered([])).toEqual([])
  })
})
