/**
 * 日志可视化条的行为断言（级别堆叠条 / Top 来源 / 时间分布）。
 *
 * 这三块是纯展示组件（只吃数据、无取数、无交互状态），但有两个容易写错的地方值得钉住：
 *   ① 比例分母：分母错了会把“占比”画错，而图形看上去仍然“正常”；
 *   ② 空/退化输入：全部落在同一毫秒、单一值、空数组——不得报错也不得画出误导性图形。
 */
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { LevelDistributionBar, TimeDistributionBars, TopSourcesBars } from './LogsInsightsViz'

describe('LevelDistributionBar', () => {
  it('按计数比例分段，而非等分', () => {
    render(
      <LevelDistributionBar
        facets={[
          { value: 'INFO', count: 75 },
          { value: 'WARN', count: 25 },
        ]}
      />,
    )
    const bar = screen.getByTestId('logs-level-distribution')
    const segs = Array.from(bar.children) as HTMLElement[]
    expect(segs).toHaveLength(2)
    // 75% / 25%：如果实现里用等分或忘了除总数，这里会立刻暴。
    expect(segs[0]?.style.width).toBe('75%')
    expect(segs[1]?.style.width).toBe('25%')
  })

  it('空数据不渲染（不画一个空框）', () => {
    const { container } = render(<LevelDistributionBar facets={[]} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('分子分母都为 0 时不渲染（避免 0/0）', () => {
    const { container } = render(
      <LevelDistributionBar facets={[{ value: 'INFO', count: 0 }]} />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it('计数与级别落到 DOM 上（读屏与测试都能拿到事实数据）', () => {
    render(<LevelDistributionBar facets={[{ value: 'ERROR', count: 3 }]} uncertain />)
    // 不用 aria-label 断言：它经由 i18n 插值，测试环境未加载翻译资源时会退化成 key。
    // data-level-detail 是不经翻译的事实数据，图形不可读时它是唯一可验证的来源。
    expect(screen.getByTestId('logs-level-distribution')).toHaveAttribute(
      'data-level-detail',
      'ERROR:3',
    )
  })
})

describe('TopSourcesBars', () => {
  it('按条数降序，且截到 limit', () => {
    render(
      <TopSourcesBars
        limit={2}
        items={[
          { id: 'inst:1/file', count: 10 },
          { id: 'inst:2/file', count: 99 },
          { id: 'node:1/file', count: 50 },
        ]}
      />,
    )
    const rows = screen.getByTestId('logs-top-sources').children
    expect(rows).toHaveLength(2)
    // 最大的排最前：inst:2(99) → node:1(50)
    expect(rows[0]?.textContent).toContain('inst:2/file')
    expect(rows[1]?.textContent).toContain('node:1/file')
  })

  it('空列表不渲染', () => {
    const { container } = render(<TopSourcesBars items={[]} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('最长条为满宽，其余按比例', () => {
    render(
      <TopSourcesBars
        items={[
          { id: 'a', count: 100 },
          { id: 'b', count: 50 },
        ]}
      />,
    )
    const rows = Array.from(screen.getByTestId('logs-top-sources').children) as HTMLElement[]
    const barOf = (row: HTMLElement) => row.querySelector('.bg-status-info') as HTMLElement
    expect(barOf(rows[0]!).style.width).toBe('100%')
    expect(barOf(rows[1]!).style.width).toBe('50%')
  })
})

describe('TimeDistributionBars', () => {
  const T0 = new Date('2026-11-11T00:00:00Z').getTime()

  it('时间跨度正常时画出分桶', () => {
    render(<TimeDistributionBars timestamps={[T0, T0 + 1000, T0 + 2000, T0 + 3000]} />)
    const viz = screen.getByTestId('logs-time-distribution')
    expect(viz.children.length).toBe(24)
  })

  it('不足两个样本不渲染（单点没有“分布”）', () => {
    const { container } = render(<TimeDistributionBars timestamps={[T0]} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('全部同一毫秒不渲染（跨度为 0，分桶会除零）', () => {
    const { container } = render(<TimeDistributionBars timestamps={[T0, T0, T0]} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('有数据的桶不得因为高度四舍五入而消失', () => {
    // 16 条落在同一时刻、1 条落在末尾：峰值 16，后者比例 6.25%。
    // 若实现里直接 Math.round 或忘了最小高度，小值桶会变成 0px 高（看上去没数据）。
    const stamps = [...Array(16)].map(() => T0).concat([T0 + 10_000])
    render(<TimeDistributionBars timestamps={stamps} />)
    const bars = Array.from(screen.getByTestId('logs-time-distribution').children) as HTMLElement[]
    const nonZero = bars.filter((b) => !b.className.includes('bg-muted'))
    expect(nonZero.length).toBeGreaterThanOrEqual(2)
    for (const b of nonZero) {
      expect(parseFloat(b.style.height)).toBeGreaterThan(0)
    }
  })
})
