import { describe, expect, it } from 'vitest'
import { flatNavItems, navGroupsForRole, NAV_GROUPS } from './nav-config'

describe('console nav config', () => {
  it('routes group network entries to distinct list and topology surfaces', () => {
    const group = NAV_GROUPS.find((item) => item.key === 'groupNetwork')
    const links = group?.children?.map((item) => [item.labelKey, item.to])

    expect(links).toContainEqual(['nav.groupManagement', '/networks'])
    expect(links).toContainEqual(['nav.networkTopology', '/networks/topology'])
  })

  it('keeps FR-112 platform/runtime entries under platform management without changing URLs', () => {
    const platform = NAV_GROUPS.find((item) => item.key === 'platformManagement')
    const sections = platform?.sections ?? []
    const routeByLabel = new Map(
      sections.flatMap((section) => section.children.map((item) => [item.labelKey, item.to] as const)),
    )

    expect(platform?.labelKey).toBe('nav.platformManagement')
    expect(routeByLabel.get('nav.runtimeAssets')).toBe('/runtime-assets')
    expect(routeByLabel.get('nav.clientChannels')).toBe('/client-channels')
    expect(routeByLabel.get('nav.storage')).toBe('/storage')
    expect(routeByLabel.get('nav.users')).toBe('/users')
    expect(routeByLabel.get('nav.groups')).toBe('/groups')
    expect(routeByLabel.get('nav.audit')).toBe('/audit')
    expect(routeByLabel.get('nav.systemSettings')).toBe('/settings')
  })

  it('把 FR-272 页面纳入统一导航真源并归入指定分组', () => {
    const servers = NAV_GROUPS.find((item) => item.key === 'servers')
    const platform = NAV_GROUPS.find((item) => item.key === 'platformManagement')
    const storageRuntime = platform?.sections?.find((section) => section.labelKey === 'nav.storageRuntime')
    const taskNotification = platform?.sections?.find((section) => section.labelKey === 'nav.taskNotification')
    const flatRoutes = flatNavItems(1)

    expect(servers?.children?.map((item) => [item.labelKey, item.to])).toEqual(
      expect.arrayContaining([
        ['nav.players', '/players'],
        ['nav.bots', '/bots'],
      ]),
    )
    expect(storageRuntime?.children.map((item) => [item.labelKey, item.to])).toContainEqual(['nav.backups', '/backups'])
    expect(taskNotification?.children.map((item) => [item.labelKey, item.to])).toContainEqual(['nav.schedules', '/schedules'])
    expect(flatRoutes).toEqual(
      expect.arrayContaining([
        { labelKey: 'nav.players', to: '/players' },
        { labelKey: 'nav.bots', to: '/bots' },
        { labelKey: 'nav.schedules', to: '/schedules' },
        { labelKey: 'nav.backups', to: '/backups' },
      ]),
    )
  })

  it('平台管理按业务域分节：身份与权限 / 审计与设置 拆分', () => {
    const platform = NAV_GROUPS.find((item) => item.key === 'platformManagement')
    const sectionKeys = platform?.sections?.map((s) => s.labelKey) ?? []
    expect(sectionKeys).toEqual([
      'nav.contentDistribution',
      'nav.storageRuntime',
      'nav.taskNotification',
      'nav.identityAccess',
      'nav.auditSettings',
    ])
    // 非管理员看不到 Agent / 系统维护
    expect(sectionKeys).not.toContain('nav.agentAccess')
    expect(sectionKeys).not.toContain('nav.systemMaintenance')
  })

  it('平台管理员追加 Agent 接入 + 系统维护两节；业务路由 URL 不变', () => {
    const operatorTargets = flatNavItems(1).map((item) => item.to)
    const adminTargets = flatNavItems(10).map((item) => item.to)
    const adminGroup = navGroupsForRole(10).find((item) => item.key === 'platformManagement')
    const agentSection = adminGroup?.sections?.find((section) => section.labelKey === 'nav.agentAccess')
    const maintSection = adminGroup?.sections?.find((section) => section.labelKey === 'nav.systemMaintenance')

    expect(operatorTargets).not.toContain('/database')
    expect(operatorTargets).not.toContain('/system-update')
    expect(operatorTargets).not.toContain('/artifact-versions')
    expect(operatorTargets).not.toContain('/agent-tokens')
    expect(operatorTargets).not.toContain('/mcp-sessions')
    expect(operatorTargets).not.toContain('/agent-call-logs')

    expect(adminTargets).toContain('/database')
    expect(adminTargets).toContain('/system-update')
    expect(adminTargets).toContain('/artifact-versions')
    expect(adminTargets).toContain('/agent-tokens')
    expect(adminTargets).toContain('/mcp-sessions')
    expect(adminTargets).toContain('/agent-call-logs')

    expect(agentSection?.children.map((item) => [item.labelKey, item.to])).toEqual([
      ['nav.agentTokens', '/agent-tokens'],
      ['nav.mcpSessions', '/mcp-sessions'],
      ['nav.agentCallLogs', '/agent-call-logs'],
    ])
    expect(maintSection?.children.map((item) => [item.labelKey, item.to])).toEqual([
      ['nav.artifactVersions', '/artifact-versions'],
      ['nav.database', '/database'],
      ['nav.systemUpdate', '/system-update'],
    ])

    // 分节顺序：业务五节 + 管理员两节
    expect(adminGroup?.sections?.map((s) => s.labelKey)).toEqual([
      'nav.contentDistribution',
      'nav.storageRuntime',
      'nav.taskNotification',
      'nav.identityAccess',
      'nav.auditSettings',
      'nav.agentAccess',
      'nav.systemMaintenance',
    ])
  })
})
