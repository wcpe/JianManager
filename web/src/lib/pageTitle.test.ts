import { describe, it, expect } from 'vitest'
import { consoleTitleKey } from './pageTitle'

describe('consoleTitleKey', () => {
  it('根路径映射到仪表盘', () => {
    expect(consoleTitleKey('/')).toBe('nav.dashboard')
    expect(consoleTitleKey('')).toBe('nav.dashboard')
  })

  it('顶层路由映射到对应区标题', () => {
    expect(consoleTitleKey('/nodes')).toBe('nav.nodes')
    expect(consoleTitleKey('/instances')).toBe('nav.allInstances')
    expect(consoleTitleKey('/system-update')).toBe('nav.systemUpdate')
    expect(consoleTitleKey('/client-channels')).toBe('nav.clientChannels')
  })

  it('子路由按首段归并到所属区', () => {
    expect(consoleTitleKey('/instances/123')).toBe('nav.allInstances')
    expect(consoleTitleKey('/nodes/7/detail')).toBe('nav.nodes')
  })

  it('未知路由返回空串（调用方回退通用标题）', () => {
    expect(consoleTitleKey('/totally-unknown')).toBe('')
  })
})
