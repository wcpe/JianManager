import { describe, it, expect } from 'vitest'
import {
  HEADER_RIGHT_SLOTS,
  NOTIFICATIONS_SLOT,
  searchBoxClass,
  slotVisibility,
  visibilityClass,
  type HeaderSlot,
} from './header-layout'

describe('header-layout（FR-179 全局页眉右对齐 + FR-216 通知铃铛合并）', () => {
  it('右侧操作区按「搜索→集群徽标→通知铃铛→账户」固定顺序', () => {
    expect(HEADER_RIGHT_SLOTS).toEqual(['search', 'clusterBadges', 'notifications', 'account'])
  })

  it('搜索靠右：并入右侧操作区且排在最前（不再居中铺中部）', () => {
    expect(HEADER_RIGHT_SLOTS[0]).toBe('search')
    expect(HEADER_RIGHT_SLOTS).toContain('search')
  })

  it('FR-216：通知铃铛为单一入口（合并原 收件箱 + 告警铃铛），紧邻账户之前', () => {
    const notifIdx = HEADER_RIGHT_SLOTS.indexOf('notifications')
    const accountIdx = HEADER_RIGHT_SLOTS.indexOf('account')
    expect(notifIdx).toBeGreaterThanOrEqual(0)
    expect(notifIdx).toBe(accountIdx - 1)
    expect(NOTIFICATIONS_SLOT).toBe('notifications')
    // 不再有独立的 inbox / alertBell 槽位。
    expect(HEADER_RIGHT_SLOTS).not.toContain('inbox' as HeaderSlot)
    expect(HEADER_RIGHT_SLOTS).not.toContain('alertBell' as HeaderSlot)
  })

  it('账户菜单沉最右', () => {
    expect(HEADER_RIGHT_SLOTS[HEADER_RIGHT_SLOTS.length - 1]).toBe('account')
  })

  it('窄屏（<md）仅保留通知铃铛 + 账户，确保不翻屏', () => {
    const alwaysVisible = HEADER_RIGHT_SLOTS.filter((s) => slotVisibility(s) === 'always')
    expect(alwaysVisible).toEqual(['notifications', 'account'])
  })

  it('搜索 ≥md 可见、集群徽标 ≥lg 可见、通知铃铛常驻', () => {
    expect(slotVisibility('search')).toBe('md')
    expect(slotVisibility('clusterBadges')).toBe('lg')
    expect(slotVisibility('notifications')).toBe('always')
  })

  it('每个槽位都有确定的可见性档位（穷尽）', () => {
    const slots: HeaderSlot[] = ['search', 'clusterBadges', 'notifications', 'account']
    for (const s of slots) {
      expect(['always', 'md', 'lg']).toContain(slotVisibility(s))
    }
  })

  it('可见性档位映射到正确的 Tailwind 显隐类', () => {
    expect(visibilityClass('md')).toBe('hidden md:block')
    expect(visibilityClass('lg')).toBe('hidden lg:flex')
    expect(visibilityClass('always')).toBe('')
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
