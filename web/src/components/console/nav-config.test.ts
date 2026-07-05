import { describe, expect, it } from 'vitest'
import { NAV_GROUPS } from './nav-config'

describe('console nav config', () => {
  it('routes group network entries to distinct list and topology surfaces', () => {
    const group = NAV_GROUPS.find((item) => item.key === 'groupNetwork')
    const links = group?.children?.map((item) => [item.labelKey, item.to])

    expect(links).toContainEqual(['nav.groupManagement', '/networks'])
    expect(links).toContainEqual(['nav.networkTopology', '/networks/topology'])
  })
})
