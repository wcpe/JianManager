import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { ResourceGauge } from './gauge'

/**
 * 环的几何约束：描边外沿不得超出 SVG 视口。
 *
 * 【为何值得单独盯这一条】SVG 默认 `overflow: hidden`，描边一旦超出视口
 * 就被切掉，而**没有任何报错**——看上去只是「环的上下左右各少一块」。
 * 这个缺陷在静止时很可能看不出来，只有调用方把描边加粗（悬停/聚焦）时才显形，
 * 所以必须在几何层直接卡死，而不是靠截图目测。
 */

function geometry(container: HTMLElement) {
  const svg = container.querySelector('svg') as SVGSVGElement
  const circles = [...svg.querySelectorAll('circle')]
  return {
    size: Number(svg.getAttribute('width')),
    circles: circles.map((c) => ({
      r: Number(c.getAttribute('r')),
      sw: Number(c.getAttribute('stroke-width')),
    })),
  }
}

describe('ResourceGauge 环几何', () => {
  it('静止态描边外沿不超出视口', () => {
    const { container } = render(<ResourceGauge value={40} size={92} thickness={8} />)
    const { size, circles } = geometry(container)
    const half = size / 2

    expect(circles.length).toBeGreaterThan(0)
    for (const c of circles) {
      expect(c.r + c.sw / 2).toBeLessThanOrEqual(half)
    }
  })

  it('描边加粗到 10px 仍不越界（悬停/聚焦会这么干）', () => {
    // OverviewPageView 在 hover 与 focus-visible 时把描边从 8 加到 10。
    // 旧实现 r=(size-thickness)/2 在 92/8 下得到 r=42，加粗后外沿 47 > 46 被切。
    const { container } = render(<ResourceGauge value={40} size={92} thickness={8} />)
    const { size, circles } = geometry(container)
    const half = size / 2

    for (const c of circles) {
      // 模拟调用方的加粗：用组件实际渲染的 r 重新算外沿
      expect(c.r + 10 / 2).toBeLessThanOrEqual(half)
    }
  })

  it('小尺寸与粗描边组合也不越界', () => {
    // 尺寸越小、描边占比越大，余量越容易被吃光。
    const { container } = render(<ResourceGauge value={75} size={56} thickness={8} />)
    const { size, circles } = geometry(container)
    const half = size / 2

    for (const c of circles) {
      expect(c.r + c.sw / 2).toBeLessThanOrEqual(half)
    }
  })

  it('环仍居中：圆心在视口中心', () => {
    // 收缩半径时若只改 r 而不动 cx/cy，环会偏心。
    const { container } = render(<ResourceGauge value={40} size={92} thickness={8} />)
    const svg = container.querySelector('svg') as SVGSVGElement
    const circles = [...svg.querySelectorAll('circle')]

    for (const c of circles) {
      expect(Number(c.getAttribute('cx'))).toBe(46)
      expect(Number(c.getAttribute('cy'))).toBe(46)
    }
  })
})
