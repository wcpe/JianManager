import { describe, it, expect } from 'vitest'
import { breadcrumbTrail } from './breadcrumb'

describe('breadcrumbTrail', () => {
  it('根路径 → 单节点总览（无 to）', () => {
    expect(breadcrumbTrail('/')).toEqual([{ labelKey: 'nav.dashboard' }])
    expect(breadcrumbTrail('')).toEqual([{ labelKey: 'nav.dashboard' }])
  })

  it('顶层列表页 → [域(无 to), 页面(无 to，当前页)]', () => {
    expect(breadcrumbTrail('/instances')).toEqual([
      { labelKey: 'nav.cluster' },
      { labelKey: 'nav.allInstances' },
    ])
    expect(breadcrumbTrail('/alerts')).toEqual([
      { labelKey: 'nav.monitor' },
      { labelKey: 'nav.alerts' },
    ])
  })

  it('监控页归「监控」域', () => {
    expect(breadcrumbTrail('/monitor')).toEqual([
      { labelKey: 'nav.monitor' },
      { labelKey: 'nav.monitoring' },
    ])
  })

  it('子路由 → 页面节点可点回列表（末级名称由调用方补）', () => {
    expect(breadcrumbTrail('/instances/42')).toEqual([
      { labelKey: 'nav.cluster' },
      { labelKey: 'nav.allInstances', to: '/instances' },
    ])
  })

  it('系统域页面归「系统」', () => {
    expect(breadcrumbTrail('/users')).toEqual([
      { labelKey: 'nav.system' },
      { labelKey: 'nav.users' },
    ])
    expect(breadcrumbTrail('/licenses')).toEqual([
      { labelKey: 'nav.system' },
      { labelKey: 'licenses.title' },
    ])
  })

  it('未知首段 → 空数组（调用方回退）', () => {
    expect(breadcrumbTrail('/totally-unknown')).toEqual([])
  })
})
