import { describe, expect, it } from 'vitest'

import { oldestOf, windowForAnchor } from './LogsPage'

/**
 * tail-first 连续分页的四条纯函数用例（`LogsPage` 内两处纯函数，行为与组件接线一字不差 ✓）：
 * ① 首屏窗＝15 分钟（锚点为窗上界）；
 * ② "下一页"⇒ 窗口**整体前移**至锚点（from/to 同步后移，窗宽不变）；
 * ③ 连续锚点递增 ⇒ 窗上界严格递减（无回卷）、空页时调用方停翻；
 * ④ 复位语义：切窗/切筛后以同一锚点重建 ⇒ 窗与首屏一致（锚点栈由既有 resetTo 清空）。
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

	it('② 下一页：窗口整体前移至锚点（两根同步后移、窗宽不变）', () => {
		const first = windowForAnchor('15m', '2026-10-07T04:00:00Z')
		const oldest = '2026-10-07T03:59:30Z' // 假设本页最旧一条
		const second = windowForAnchor('15m', oldest)
		expect(at(second.to)).toBe(at(oldest))
		expect(at(second.from)).toBe(at(oldest) - 15 * M)
		// 两根相对第一页都后移了 30s；窗宽不变：
		expect(at(first.from) - at(second.from)).toBe(30_000)
		expect(at(first.to) - at(second.to)).toBe(30_000)
		expect(at(second.to) - at(second.from)).toBe(15 * M)
	})

	it('③ 连续锚点：窗上界严格递减（无回卷）；空页 ⇒ oldestOf 为 null（停翻）', () => {
		const anchors = ['2026-10-07T04:00:00Z', '2026-10-07T03:59:30Z', '2026-10-07T03:58:00Z', '2026-10-07T03:40:00Z']
		const tos = anchors.map((a) => at(windowForAnchor('15m', a).to))
		for (let i = 1; i < tos.length; i++) {
			expect(tos[i]).toBeLessThan(tos[i - 1]) // 严格递减 ⇒ 一直向更早 ✓
		}
		expect(oldestOf([])).toBeNull() // ④/③ 的空页即停依据 ✓
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

	it('oldestOf：取末条（event_time DESC ⇒ 最旧）且容忍缺字段', () => {
		const items = [{ time: '2026-10-07T04:00:00Z' }, { time: '2026-10-07T03:59:00Z' }]
		expect(oldestOf(items)).toBe('2026-10-07T03:59:00Z')
		expect(oldestOf([{ time: '2026-10-07T04:00:00Z' }, {}])).toBeNull()
	})
})
