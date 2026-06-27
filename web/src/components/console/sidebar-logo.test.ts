import { describe, it, expect } from 'vitest'
import { logoToggleLabelKey } from './sidebar-logo'

/**
 * 顶部 logo 折叠/展开触发器的无障碍标签解析（FR-181，增强 FR-131）。
 * logo 整体可点：展开态点击=收起（label 用 collapseSidebar），
 * 折叠态点击=展开（label 用 expandSidebar），保证 button 语义下 aria-label 描述的是「将发生的动作」。
 */
describe('logoToggleLabelKey', () => {
  it('returns the collapse label when the sidebar is expanded', () => {
    expect(logoToggleLabelKey(false)).toBe('nav.collapseSidebar')
  })

  it('returns the expand label when the sidebar is collapsed', () => {
    expect(logoToggleLabelKey(true)).toBe('nav.expandSidebar')
  })
})
