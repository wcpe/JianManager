import { describe, expect, it } from 'vitest'
import { flatNavItems, navGroupsForPermissions, navGroupsForRole, NAV_GROUPS, type NavGroup } from './nav-config'
import { ALL_PERMISSION_NODE_IDS, ROLE_GROUP_VIEWER, ROLE_PLATFORM_ADMIN } from '@/lib/roles'

const adminNodes = new Set(ALL_PERMISSION_NODE_IDS)

function pathsFromGroups(groups: NavGroup[]): string[] {
  const out: string[] = []
  for (const g of groups) {
    if (g.to) out.push(g.to)
    for (const c of g.children ?? []) out.push(c.to)
    for (const s of g.sections ?? []) for (const c of s.children) out.push(c.to)
  }
  return out
}

describe('console nav config（FR-431 六域 IA）', () => {
  it('顶层六域 + 平台首页，URL 不变且无 /alerts', () => {
    const keys = NAV_GROUPS.map((g) => g.key)
    expect(keys).toEqual([
      'platformHome',
      'servers',
      'groupNetwork',
      'workbench',
      'observability',
      'clientDistribution',
      'platformSettings',
    ])
    const all = flatNavItems(ROLE_PLATFORM_ADMIN).map((i) => i.to)
    expect(all).toContain('/')
    expect(all).toContain('/instances')
    expect(all).toContain('/nodes')
    expect(all).toContain('/players')
    expect(all).toContain('/bots')
    expect(all).toContain('/networks')
    expect(all).toContain('/networks/topology')
    expect(all).toContain('/super')
    expect(all).toContain('/director')
    expect(all).toContain('/monitor')
    expect(all).toContain('/logs')
    expect(all).toContain('/statistics')
    expect(all).toContain('/notifications')
    expect(all).toContain('/client-channels')
    expect(all).toContain('/client-dist-ops')
    expect(all).toContain('/permissions')
    expect(all).toContain('/templates')
    expect(all).not.toContain('/alerts')
  })

  it('工作台独立域；templates 在平台设置而非客户端分发', () => {
    const workbench = NAV_GROUPS.find((g) => g.key === 'workbench')
    expect(workbench?.children?.map((c) => c.to)).toEqual(['/super', '/director'])

    const client = NAV_GROUPS.find((g) => g.key === 'clientDistribution')
    expect(client?.children?.map((c) => c.to)).toEqual(['/client-channels', '/client-dist-ops'])

    const settings = NAV_GROUPS.find((g) => g.key === 'platformSettings')
    const templatesSection = settings?.sections?.find((s) => s.labelKey === 'nav.contentTemplates')
    expect(templatesSection?.children.map((c) => c.to)).toEqual(['/templates'])
  })

  it('平台设置分节齐全：身份/任务/存储/模板/审计/Agent/维护', () => {
    const settings = NAV_GROUPS.find((g) => g.key === 'platformSettings')
    const sectionKeys = settings?.sections?.map((s) => s.labelKey) ?? []
    expect(sectionKeys).toEqual([
      'nav.identityAccess',
      'nav.taskSchedule',
      'nav.storageRuntime',
      'nav.contentTemplates',
      'nav.auditSettings',
      'nav.agentAccess',
      'nav.systemMaintenance',
    ])
    const identity = settings?.sections?.find((s) => s.labelKey === 'nav.identityAccess')
    expect(identity?.children.map((c) => [c.labelKey, c.to])).toEqual([
      ['nav.users', '/users'],
      ['nav.groups', '/groups'],
      ['nav.permissions', '/permissions'],
    ])
  })

  it('平台管理员（权限全开）可见系统维护与 Agent 入口', () => {
    const adminTargets = flatNavItems(adminNodes, true).map((i) => i.to)
    expect(adminTargets).toEqual(
      expect.arrayContaining([
        '/database',
        '/system-update',
        '/artifact-versions',
        '/agent-tokens',
        '/mcp-sessions',
        '/agent-call-logs',
        '/permissions',
      ]),
    )
  })

  it('group_viewer 种子：无系统/Agent/用户/客户端分发入口，实例与观测仍在', () => {
    const groups = navGroupsForRole(ROLE_GROUP_VIEWER)
    const targets = pathsFromGroups(groups)

    expect(targets).toContain('/instances')
    expect(targets).toContain('/monitor')
    expect(targets).toContain('/logs')
    expect(targets).toContain('/notifications')
    expect(targets).toContain('/backups')

    expect(targets).not.toContain('/database')
    expect(targets).not.toContain('/system-update')
    expect(targets).not.toContain('/agent-tokens')
    expect(targets).not.toContain('/users')
    expect(targets).not.toContain('/permissions')
    expect(targets).not.toContain('/client-channels')
    expect(targets).not.toContain('/templates')
    expect(targets).not.toContain('/super')
  })

  it('navGroupsForPermissions：空集合仅保留无 perm 入口；平台管理员全开', () => {
    const empty = navGroupsForPermissions(new Set(), false)
    expect(pathsFromGroups(empty)).toEqual(['/'])

    const admin = navGroupsForPermissions(null, true)
    expect(pathsFromGroups(admin).length).toBeGreaterThan(20)
  })

  it('flatNavItems 角色包装与 Set+admin 签名并存', () => {
    const byRole = flatNavItems(ROLE_PLATFORM_ADMIN).map((i) => i.to).sort()
    const byNodes = flatNavItems(adminNodes, true).map((i) => i.to).sort()
    expect(byRole).toEqual(byNodes)
  })
})
