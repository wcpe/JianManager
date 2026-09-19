import { describe, it, expect } from 'vitest'
import { breadcrumbTrail } from './breadcrumb'

describe('breadcrumbTrail（FR-431 六域）', () => {
  it('根路径 → 平台首页（无 to）', () => {
    expect(breadcrumbTrail('/')).toEqual([{ labelKey: 'nav.platformHome' }])
    expect(breadcrumbTrail('')).toEqual([{ labelKey: 'nav.platformHome' }])
  })

  it('服务器域列表页', () => {
    expect(breadcrumbTrail('/instances')).toEqual([
      { labelKey: 'nav.servers' },
      { labelKey: 'nav.allInstances' },
    ])
  })

  it('工作台独立域：超级工作台 / 导播台', () => {
    expect(breadcrumbTrail('/super')).toEqual([
      { labelKey: 'nav.workbench' },
      { labelKey: 'nav.superWorkbench' },
    ])
    expect(breadcrumbTrail('/director')).toEqual([
      { labelKey: 'nav.workbench' },
      { labelKey: 'nav.director' },
    ])
  })

  it('客户端分发独立域', () => {
    expect(breadcrumbTrail('/client-dist-ops')).toEqual([
      { labelKey: 'nav.clientDistribution' },
      { labelKey: 'nav.clientDistOps' },
    ])
    expect(breadcrumbTrail('/client-channels')).toEqual([
      { labelKey: 'nav.clientDistribution' },
      { labelKey: 'nav.clientChannels' },
    ])
  })

  it('身份与权限：permissions 父级 identityAccess', () => {
    expect(breadcrumbTrail('/permissions')).toEqual([
      { labelKey: 'nav.identityAccess' },
      { labelKey: 'nav.permissions' },
    ])
    expect(breadcrumbTrail('/users')).toEqual([
      { labelKey: 'nav.identityAccess' },
      { labelKey: 'nav.users' },
    ])
  })

  it('templates 归平台设置', () => {
    expect(breadcrumbTrail('/templates')).toEqual([
      { labelKey: 'nav.platformSettings' },
      { labelKey: 'nav.templates' },
    ])
  })

  it('观测域含通知中心', () => {
    expect(breadcrumbTrail('/notifications')).toEqual([
      { labelKey: 'nav.observability' },
      { labelKey: 'nav.notifications' },
    ])
    expect(breadcrumbTrail('/monitor')).toEqual([
      { labelKey: 'nav.observability' },
      { labelKey: 'nav.monitoring' },
    ])
  })

  it('系统维护分节', () => {
    expect(breadcrumbTrail('/database')).toEqual([
      { labelKey: 'nav.systemMaintenance' },
      { labelKey: 'nav.database' },
    ])
  })

  it('子路由 → 页面节点可点回列表', () => {
    expect(breadcrumbTrail('/instances/42')).toEqual([
      { labelKey: 'nav.servers' },
      { labelKey: 'nav.allInstances', to: '/instances' },
    ])
  })

  it('未知首段 → 空数组', () => {
    expect(breadcrumbTrail('/totally-unknown')).toEqual([])
  })
})
