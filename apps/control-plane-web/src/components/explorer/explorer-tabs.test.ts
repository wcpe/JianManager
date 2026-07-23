import { describe, it, expect, beforeEach } from 'vitest'
import {
  emptyTabsState,
  openTab,
  closeTab,
  activateTab,
  floatTab,
  dockTab,
  updateTabContext,
  titleFromPath,
  resetTabIdSeq,
  MAX_EXPLORER_TABS,
  MAX_EXPLORER_FLOATS,
} from './explorer-tabs'

describe('explorer-tabs（FR-376）', () => {
  beforeEach(() => {
    resetTabIdSeq(0)
  })

  it('empty 含一个根标签', () => {
    const s = emptyTabsState()
    expect(s.tabs).toHaveLength(1)
    expect(s.tabs[0].currentDir).toBe('')
    expect(s.activeId).toBe(s.tabs[0].id)
  })

  it('openTab 激活新签；达上限失败', () => {
    let s = emptyTabsState()
    for (let i = 1; i < MAX_EXPLORER_TABS; i++) {
      const r = openTab(s, { currentDir: `d${i}` })
      expect(r.ok).toBe(true)
      if (r.ok) s = r.state
    }
    expect(s.tabs).toHaveLength(MAX_EXPLORER_TABS)
    const fail = openTab(s)
    expect(fail.ok).toBe(false)
    if (!fail.ok) expect(fail.reason).toBe('max_tabs')
  })

  it('closeTab 切到邻签；仅一签时 no-op', () => {
    let s = emptyTabsState()
    const a = s.tabs[0].id
    const r = openTab(s, { currentDir: 'plugins' })
    expect(r.ok).toBe(true)
    if (r.ok) s = r.state
    const b = s.activeId
    s = closeTab(s, b)
    expect(s.tabs).toHaveLength(1)
    expect(s.activeId).toBe(a)
    expect(closeTab(s, a)).toBe(s)
  })

  it('activateTab / float / dock', () => {
    let s = emptyTabsState()
    const r = openTab(s, { currentDir: 'world' })
    if (r.ok) s = r.state
    const id2 = s.activeId
    const id1 = s.tabs[0].id
    s = activateTab(s, id1)
    expect(s.activeId).toBe(id1)

    const f = floatTab(s, id1)
    expect(f.ok).toBe(true)
    if (f.ok) {
      s = f.state
      expect(s.tabs.find((t) => t.id === id1)?.floated).toBe(true)
      // 弹出后激活未浮动签
      expect(s.activeId).toBe(id2)
    }
    s = dockTab(s, id1)
    expect(s.tabs.find((t) => t.id === id1)?.floated).toBe(false)
    expect(s.activeId).toBe(id1)
  })

  it('浮动达上限失败', () => {
    let s = emptyTabsState()
    for (let i = 0; i < MAX_EXPLORER_FLOATS; i++) {
      if (i > 0) {
        const r = openTab(s)
        if (r.ok) s = r.state
      }
      const id = s.tabs[i].id
      const f = floatTab(s, id)
      expect(f.ok).toBe(true)
      if (f.ok) s = f.state
    }
    const extra = openTab(s)
    if (extra.ok) s = extra.state
    const fail = floatTab(s, s.tabs[s.tabs.length - 1].id)
    expect(fail.ok).toBe(false)
    if (!fail.ok) expect(fail.reason).toBe('max_floats')
  })

  it('updateTabContext 更新标题与 dirty', () => {
    let s = emptyTabsState()
    const id = s.activeId
    s = updateTabContext(s, id, { dir: 'plugins', file: 'plugins/a.yml', dirty: true })
    const t = s.tabs[0]
    expect(t.currentDir).toBe('plugins')
    expect(t.openFilePath).toBe('plugins/a.yml')
    expect(t.title).toBe('a.yml')
    expect(t.dirty).toBe(true)
  })

  it('titleFromPath', () => {
    expect(titleFromPath('')).toBe('/')
    expect(titleFromPath('plugins/Essentials')).toBe('Essentials')
    expect(titleFromPath('plugins', 'plugins/config.yml')).toBe('config.yml')
  })
})
