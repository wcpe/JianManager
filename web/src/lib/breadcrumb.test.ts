import { describe, it, expect } from 'vitest'
import { breadcrumbTrail } from './breadcrumb'

describe('breadcrumbTrail', () => {
  it('根路径 → 平台首页（无 to）', () => {
    expect(breadcrumbTrail('/')).toEqual([{ labelKey: 'nav.platformHome' }])
    expect(breadcrumbTrail('')).toEqual([{ labelKey: 'nav.platformHome' }])
  })

  it('顶层列表页 → [域(无 to), 页面(无 to，当前页)]', () => {
    expect(breadcrumbTrail('/instances')).toEqual([
      { labelKey: 'nav.servers' },
      { labelKey: 'nav.allInstances' },
    ])
    // 告警管理页随平台管理域收口。
    expect(breadcrumbTrail('/alerts')).toEqual([
      { labelKey: 'nav.platformManagement' },
      { labelKey: 'nav.alerts' },
    ])
  })

  it('服务器域 超级工作台 / 导播台 与导航文案一致（FR-254）', () => {
    expect(breadcrumbTrail('/super')).toEqual([
      { labelKey: 'nav.servers' },
      { labelKey: 'nav.superWorkbench' },
    ])
    expect(breadcrumbTrail('/director')).toEqual([
      { labelKey: 'nav.servers' },
      { labelKey: 'nav.director' },
    ])
  })

  it('观测域 客户端分发监控 与导航文案一致（FR-245 补漏）', () => {
    expect(breadcrumbTrail('/client-dist-monitor')).toEqual([
      { labelKey: 'nav.observability' },
      { labelKey: 'nav.clientDistMonitor' },
    ])
  })

  it('通知中心归「平台管理」域（FR-254）', () => {
    expect(breadcrumbTrail('/notifications')).toEqual([
      { labelKey: 'nav.platformManagement' },
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

  it('任务中心归「平台管理」域（FR-254）', () => {
    expect(breadcrumbTrail('/tasks')).toEqual([
      { labelKey: 'nav.platformManagement' },
      { labelKey: 'nav.tasks' },
    ])
  })

  it('子路由 → 页面节点可点回列表（末级名称由调用方补）', () => {
    expect(breadcrumbTrail('/instances/42')).toEqual([
      { labelKey: 'nav.servers' },
      { labelKey: 'nav.allInstances', to: '/instances' },
    ])
  })

  it('平台管理域页面归「平台管理」', () => {
    expect(breadcrumbTrail('/users')).toEqual([
      { labelKey: 'nav.platformManagement' },
      { labelKey: 'nav.users' },
    ])
    expect(breadcrumbTrail('/licenses')).toEqual([
      { labelKey: 'nav.platformManagement' },
      { labelKey: 'licenses.title' },
    ])
  })

  it('未知首段 → 空数组（调用方回退）', () => {
    expect(breadcrumbTrail('/totally-unknown')).toEqual([])
  })
})
