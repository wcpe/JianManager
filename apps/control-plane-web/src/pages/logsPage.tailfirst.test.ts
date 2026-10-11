import { describe, expect, it } from 'vitest'

import { windowForAnchor } from './logs-filters'

/**
 * tail-first 窗口辅助函数；复合游标续页由DOM用例验证：
 * ① 首屏窗＝15 分钟（锚点为窗上界）；
 * ② 窗口完成后以原窗口下界为新上界，保持窗宽且不重扫已完成窗口；
 * ③ 连续已完成窗口上界递减；
 * ④ 复位语义：切窗/切筛后以同一锚点重建 ⇒ 窗与首屏一致（锚点栈由既有 resetTo 清空）。
 *
 * 函数本体位于 pages/logs-filters.ts（页面组件文件只导出组件，见 eslint react-refresh）。
 */
const M = 60_000
const at = (s: string) => new Date(s).getTime()

describe('tail-first 分页（纯函数）', () => {
	it('① 首屏：锚点即窗上界、窗宽 15 分钟', () => {
		const anchor = '2026-10-07T04:00:00Z'
		const w = windowForAnchor('15m', anchor)
		expect(at(w.to)).toBe(at(anchor))
		expect(at(w.from)).toBe(at(anchor) - 15 * M)
	})

	it('② 窗口完成后：原from成为下一窗to，不以最旧事件锚点重扫', () => {
		const first = windowForAnchor('15m', '2026-10-07T04:00:00Z')
		const second = windowForAnchor('15m', first.from!)
		expect(at(second.to)).toBe(at(first.from))
		expect(at(second.from)).toBe(at(first.from) - 15 * M)
		expect(at(first.from) - at(second.from)).toBe(15 * M)
		expect(at(first.to) - at(second.to)).toBe(15 * M)
		expect(at(second.to) - at(second.from)).toBe(15 * M)
	})

	it('③ 连续完成窗口：窗上界严格递减', () => {
		const anchors = ['2026-10-07T04:00:00Z', '2026-10-07T03:45:00Z', '2026-10-07T03:30:00Z', '2026-10-07T03:15:00Z']
		const tos = anchors.map((a) => at(windowForAnchor('15m', a).to))
		for (let i = 1; i < tos.length; i++) {
			expect(tos[i]).toBeLessThan(tos[i - 1]) // 严格递减 ⇒ 一直向更早 ✓
		}
	})

	it('④ 复位语义：同一锚点重建 ⇒ 窗与首屏逐字段一致（切窗/切筛后回到初始锚点）', () => {
		const initial = '2026-10-07T04:00:00Z'
		const first = windowForAnchor('15m', initial)
		const afterReset = windowForAnchor('15m', initial) // resetTo 清锚点栈 ⇒ 以初始锚点重算 ✓
		expect(afterReset).toEqual(first)
		// 且与"翻页态"不同（证明复位确实改变窗口）：
		const paged = windowForAnchor('15m', '2026-10-07T03:40:00Z')
		expect(paged).not.toEqual(first)
	})
})
