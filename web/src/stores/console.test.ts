import { describe, it, expect, beforeEach } from 'vitest'
import { useConsoleStore } from './console'

/**
 * 侧栏折叠态切换（FR-131 / FR-181）。
 * FR-181 让顶部 logo 整体复用既有 `toggleSidebar` action 触发收缩/展开，
 * 折叠态下 logo 仍可点回展开。这里直接验证被复用的 store action：
 * 反复切换须在「展开 ⇄ 折叠」间严格取反，保证 logo 点击行为可预期。
 */
describe('useConsoleStore.toggleSidebar', () => {
  beforeEach(() => {
    // 每例从展开态起步，互不污染。
    useConsoleStore.setState({ sidebarCollapsed: false })
  })

  it('collapses an expanded sidebar', () => {
    useConsoleStore.getState().toggleSidebar()
    expect(useConsoleStore.getState().sidebarCollapsed).toBe(true)
  })

  it('expands a collapsed sidebar (logo 折叠态仍可点回展开)', () => {
    useConsoleStore.setState({ sidebarCollapsed: true })
    useConsoleStore.getState().toggleSidebar()
    expect(useConsoleStore.getState().sidebarCollapsed).toBe(false)
  })

  it('round-trips back to the original state after two toggles', () => {
    const before = useConsoleStore.getState().sidebarCollapsed
    useConsoleStore.getState().toggleSidebar()
    useConsoleStore.getState().toggleSidebar()
    expect(useConsoleStore.getState().sidebarCollapsed).toBe(before)
  })
})
