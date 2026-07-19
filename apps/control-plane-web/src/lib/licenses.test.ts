import { describe, it, expect } from 'vitest'
import licensesManifest from '../../public/licenses.json'
import { depKey, filterByName, partitionDeps, type DepLike } from './licenses'

const make = (over: Partial<DepLike>): DepLike => ({
  name: 'pkg',
  version: '1.0.0',
  scope: 'web',
  type: 'runtime',
  license: 'MIT',
  ...over,
})

describe('生成许可证清单', () => {
  const dependencies = licensesManifest.dependencies

  it.each(['web', 'bot-worker', 'go', 'client-updater', 'serverprobe'])('%s 来源非空', (scope) => {
    expect(dependencies.some((dependency) => dependency.scope === scope)).toBe(true)
  })

  it('保留完整依赖计数与运行时/开发分类', () => {
    const runtime = dependencies.filter((dependency) => dependency.type === 'runtime')
    const dev = dependencies.filter((dependency) => dependency.type === 'dev')

    expect(dependencies).toHaveLength(944)
    expect(runtime.length).toBeGreaterThan(0)
    expect(dev.length).toBeGreaterThan(0)
    expect(runtime.length + dev.length).toBe(dependencies.length)
  })

  it('许可证全文不公开 Codecov token、徽章或 README 内容', () => {
    const licenseTexts = dependencies.map((dependency) => dependency.licenseText).join('\n')

    expect(licenseTexts).not.toMatch(/codecov|badge|shields\.io|\[!\[|README(?:\.md)?/i)
  })

  it('包含 Java 发行物的实际打包依赖', () => {
    expect(
      dependencies.some(
        (dependency) => dependency.scope === 'client-updater' && dependency.name.endsWith(':zstd-jni'),
      ),
    ).toBe(true)
    expect(
      dependencies.some(
        (dependency) => dependency.scope === 'serverprobe' && dependency.name === 'org.ow2.asm:asm',
      ),
    ).toBe(true)
  })
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
