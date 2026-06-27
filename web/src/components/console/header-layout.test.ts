import { describe, it, expect } from 'vitest'
import {
  HEADER_RIGHT_SLOTS,
  INBOX_SLOT,
  INBOX_SLOT_FR,
  searchBoxClass,
  slotVisibility,
  visibilityClass,
  type HeaderSlot,
} from './header-layout'

describe('header-layout（FR-179 全局页眉右对齐 + 站内信挂载点）', () => {
  it('右侧操作区按「搜索→集群徽标→收件箱→铃铛→账户」固定顺序', () => {
    expect(HEADER_RIGHT_SLOTS).toEqual(['search', 'clusterBadges', 'inbox', 'alertBell', 'account'])
  })

  it('搜索靠右：并入右侧操作区且排在最前（不再居中铺中部）', () => {
    expect(HEADER_RIGHT_SLOTS[0]).toBe('search')
    expect(HEADER_RIGHT_SLOTS).toContain('search')
  })

  it('FR-183 站内信收件箱挂载点紧邻告警铃铛之前', () => {
    const inboxIdx = HEADER_RIGHT_SLOTS.indexOf('inbox')
    const bellIdx = HEADER_RIGHT_SLOTS.indexOf('alertBell')
    expect(inboxIdx).toBeGreaterThanOrEqual(0)
    expect(inboxIdx).toBe(bellIdx - 1)
    expect(INBOX_SLOT).toBe('inbox')
    expect(INBOX_SLOT_FR).toBe('FR-183')
  })

  it('账户菜单沉最右', () => {
    expect(HEADER_RIGHT_SLOTS[HEADER_RIGHT_SLOTS.length - 1]).toBe('account')
  })

  it('窄屏（<md）仅保留铃铛 + 账户，确保不翻屏', () => {
    const alwaysVisible = HEADER_RIGHT_SLOTS.filter((s) => slotVisibility(s) === 'always')
    expect(alwaysVisible).toEqual(['alertBell', 'account'])
  })

  it('搜索 ≥md 可见、集群徽标 ≥lg 可见、收件箱为预留挂载点', () => {
    expect(slotVisibility('search')).toBe('md')
    expect(slotVisibility('clusterBadges')).toBe('lg')
    expect(slotVisibility('inbox')).toBe('reserved')
  })

  it('每个槽位都有确定的可见性档位（穷尽）', () => {
    const slots: HeaderSlot[] = ['search', 'clusterBadges', 'inbox', 'alertBell', 'account']
    for (const s of slots) {
      expect(['always', 'md', 'lg', 'reserved']).toContain(slotVisibility(s))
    }
  })

  it('可见性档位映射到正确的 Tailwind 显隐类', () => {
    expect(visibilityClass('md')).toBe('hidden md:block')
    expect(visibilityClass('lg')).toBe('hidden lg:flex')
    expect(visibilityClass('always')).toBe('')
    // 预留挂载点本期不渲染可见内容，容器不加显隐类
    expect(visibilityClass('reserved')).toBe('')
  })

  it('搜索框容器为靠右固定上限宽度（不再 flex-1 max-w-md 铺满中部）', () => {
    const cls = searchBoxClass()
    expect(cls).not.toContain('flex-1')
    expect(cls).not.toContain('max-w-md')
    expect(cls).not.toContain('mx-2')
    // 靠右：固定/上限宽度 + 窄屏隐藏
    expect(cls).toContain('md:block')
    expect(cls).toMatch(/\bhidden\b/)
  })
})
