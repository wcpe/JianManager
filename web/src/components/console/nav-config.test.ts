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

  it('adds database and system update only for platform administrators', () => {
    const operatorTargets = flatNavItems(1).map((item) => item.to)
    const adminTargets = flatNavItems(10).map((item) => item.to)
    const adminGroup = navGroupsForRole(10).find((item) => item.key === 'platformManagement')
    const adminSection = adminGroup?.sections?.find((section) => section.labelKey === 'nav.admin')

    expect(operatorTargets).not.toContain('/database')
    expect(operatorTargets).not.toContain('/system-update')
    expect(adminTargets).toContain('/database')
    expect(adminTargets).toContain('/system-update')
    expect(adminSection?.children.map((item) => [item.labelKey, item.to])).toEqual([
      ['nav.database', '/database'],
      ['nav.systemUpdate', '/system-update'],
    ])
  })
})
