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
    // 告警管理页归「系统」域（FR-216：告警随通知中心收口到系统/账户与审计）。
    expect(breadcrumbTrail('/alerts')).toEqual([
      { labelKey: 'nav.system' },
      { labelKey: 'nav.alerts' },
    ])
  })

  it('通知中心归「系统」域（FR-216）', () => {
    expect(breadcrumbTrail('/notifications')).toEqual([
      { labelKey: 'nav.system' },
      { labelKey: 'nav.notifications' },
    ])
  })

  it('监控/日志/统计 页归「观测」域（FR-215）', () => {
    expect(breadcrumbTrail('/monitor')).toEqual([
      { labelKey: 'nav.observability' },
      { labelKey: 'nav.monitoring' },
    ])
    expect(breadcrumbTrail('/logs')).toEqual([
      { labelKey: 'nav.observability' },
      { labelKey: 'nav.logs' },
    ])
    expect(breadcrumbTrail('/statistics')).toEqual([
      { labelKey: 'nav.observability' },
      { labelKey: 'nav.statistics' },
    ])
  })

  it('任务中心归「系统」域（FR-215 迁出观测）', () => {
    expect(breadcrumbTrail('/tasks')).toEqual([
      { labelKey: 'nav.system' },
      { labelKey: 'nav.tasks' },
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
