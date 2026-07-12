import { describe, it, expect } from 'vitest'
import { depKey, filterByName, partitionDeps, type DepLike } from './licenses'

const make = (over: Partial<DepLike>): DepLike => ({
  name: 'pkg',
  version: '1.0.0',
  scope: 'web',
  type: 'runtime',
  license: 'MIT',
  ...over,
})

describe('depKey', () => {
  it('由 scope|name|version 组合唯一键（区分跨源同名包）', () => {
    expect(depKey(make({ scope: 'web', name: 'react', version: '19.0.0' }))).toBe('web|react|19.0.0')
    expect(depKey(make({ scope: 'bot-worker', name: 'react', version: '19.0.0' }))).toBe(
      'bot-worker|react|19.0.0',
    )
  })
})

describe('filterByName', () => {
  const deps = [
    make({ name: 'react' }),
    make({ name: 'react-dom' }),
    make({ name: 'axios' }),
  ]

  it('空查询返回原数组（同一引用，不复制）', () => {
    expect(filterByName(deps, '')).toBe(deps)
    expect(filterByName(deps, '   ')).toBe(deps)
  })

  it('按包名子串大小写不敏感过滤', () => {
    expect(filterByName(deps, 'REACT').map((d) => d.name)).toEqual(['react', 'react-dom'])
    expect(filterByName(deps, 'ax').map((d) => d.name)).toEqual(['axios'])
  })

  it('无命中返回空数组', () => {
    expect(filterByName(deps, 'zzz')).toEqual([])
  })
})

describe('partitionDeps', () => {
  it('按 type 分运行时/开发并给计数', () => {
    const deps = [
      make({ type: 'runtime', name: 'a' }),
      make({ type: 'dev', name: 'b' }),
      make({ type: 'runtime', name: 'c' }),
      make({ type: 'dev', name: 'd' }),
      make({ type: 'dev', name: 'e' }),
    ]
    const r = partitionDeps(deps)
    expect(r.runtime.map((d) => d.name)).toEqual(['a', 'c'])
    expect(r.dev.map((d) => d.name)).toEqual(['b', 'd', 'e'])
    expect(r.runtimeCount).toBe(2)
    expect(r.devCount).toBe(3)
    expect(r.total).toBe(5)
    expect(r.licenseCount).toBe(1)
  })

  it('许可证计数按非空去重统计（忽略空与重复）', () => {
    const deps = [
      make({ license: 'MIT' }),
      make({ license: 'MIT' }),
      make({ license: 'Apache-2.0' }),
      make({ license: '' }),
    ]
    expect(partitionDeps(deps).licenseCount).toBe(2)
  })

  it('空输入计数全 0', () => {
    const r = partitionDeps([])
    expect(r).toMatchObject({ runtimeCount: 0, devCount: 0, total: 0, licenseCount: 0 })
    expect(r.runtime).toEqual([])
    expect(r.dev).toEqual([])
  })
})
