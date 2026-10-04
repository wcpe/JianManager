import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { DataPanelSkeleton, ListSkeleton, PageSkeleton } from './PageSkeleton'

/**
 * 页面骨架测试（FR-496 阶段 6 补丁）。
 *
 * 骨架的职责是「在数据/chunk 到达前占住真实内容的形状」，所以这里的断言分两类：
 * ① 结构钩子（`data-slot` / `data-variant`）——页面与 E2E 靠它判断「骨架已就位」；
 * ② 无数据时不渲染任何真实内容，且不产生可被误读为内容的文本（纯视觉占位，`aria-hidden`）。
 * 高度只断言「同量级」：精确像素归浏览器实测，jsdom 不做布局，钉死数值只会变成假契约。
 */
describe('PageSkeleton 壳态', () => {
  it('默认壳态：套 PageShell（保留留白/滚动模型）并暴露 page-skeleton 钩子', () => {
    const { container } = render(<PageSkeleton data-testid="skeleton" />)
    const root = screen.getByTestId('skeleton')
    expect(root).toHaveAttribute('data-slot', 'page-skeleton')
    expect(root).toHaveAttribute('data-variant', 'default')
    expect(root).toHaveAttribute('aria-busy', 'true')
    // 与真实页同壳：page-skeleton 就套在 page-shell 上，留白/间隙不会在替换时变化。
    expect(container.querySelector('[data-slot="page-skeleton"][data-variant="default"]')).toHaveClass('px-[25px]', 'py-[22px]', 'overflow-auto')
  })

  it('fixed 壳态：固定视口、内部区域自行滚动', () => {
    render(<PageSkeleton variant="fixed" data-testid="skeleton" />)
    const root = screen.getByTestId('skeleton')
    expect(root).toHaveAttribute('data-variant', 'fixed')
    expect(root).toHaveClass('overflow-hidden')
  })

  it('tool 壳态：去掉外层留白与页头，只铺满数据区', () => {
    const { container } = render(<PageSkeleton variant="tool" data-testid="skeleton" />)
    const root = screen.getByTestId('skeleton')
    expect(root).toHaveAttribute('data-variant', 'tool')
    expect(root).toHaveClass('p-0', 'gap-0')
    // 工具页自带布局，骨架不得再冒出页头占位（否则会多出一段高度）。
    expect(container.querySelector('[data-slot="page-skeleton-header"]')).toBeNull()
  })

  it('minimal 壳态：不套页面壳，只给首屏一个居中占位', () => {
    const { container } = render(<PageSkeleton variant="minimal" data-testid="skeleton" />)
    const root = screen.getByTestId('skeleton')
    expect(root).toHaveAttribute('data-variant', 'minimal')
    expect(root).toHaveClass('min-h-dvh')
    // 首屏外壳还没挂载，套 page-shell 会连背景/留白一起错位，故此处不应出现。
    expect(container.querySelector('[data-slot="page-shell"]')).toBeNull()
  })
})

describe('PageSkeleton 结构（与真实页面同构）', () => {
  it('三段结构齐备：页头占位 + 筛选条占位 + 数据区', () => {
    const { container } = render(<PageSkeleton />)
    expect(container.querySelector('[data-slot="page-skeleton-header"]')).toBeInTheDocument()
    expect(container.querySelector('[data-slot="data-skeleton"]')).toBeInTheDocument()
    expect(container.querySelectorAll('[data-slot="skeleton-row"]').length).toBeGreaterThan(0)
    // 页头占位必须是壳的第一个子节点，与「任何内容页第一个子元素都是 PageHeader」一致。
    // （骨架把 `data-slot` 覆写成 page-skeleton，故这里用骨架自己的钩子取壳根。）
    const shell = container.querySelector('[data-slot="page-skeleton"]')
    expect(shell?.firstElementChild).toHaveAttribute('data-slot', 'page-skeleton-header')
  })

  it('占位行数可控（rows），默认给出一屏量级', () => {
    const { container, rerender } = render(<PageSkeleton rows={3} />)
    expect(container.querySelectorAll('[data-slot="skeleton-row"]')).toHaveLength(3)

    rerender(<PageSkeleton />)
    expect(container.querySelectorAll('[data-slot="skeleton-row"]').length).toBeGreaterThanOrEqual(5)
  })

  it('不渲染任何可被读成内容的文本（纯视觉占位）', () => {
    const { container } = render(<PageSkeleton />)
    expect(container.textContent).toBe('')
    // 占位块本身对辅助技术不可见；「正在加载」由调用方经 aria-label 提供（组件库不写死文案）。
    expect(container.querySelectorAll('[aria-hidden="true"]').length).toBeGreaterThan(0)
  })

  it('透传 aria-label：调用方负责「加载中」这类文案（组件库不内置语言）', () => {
    render(<PageSkeleton aria-label="加载中" />)
    expect(screen.getByLabelText('加载中')).toHaveAttribute('data-slot', 'page-skeleton')
  })

  it('className 与默认类合并而非替换', () => {
    render(<PageSkeleton className="custom-x" data-testid="skeleton" />)
    const root = screen.getByTestId('skeleton')
    expect(root.className).toContain('custom-x')
    expect(root.className).toContain('px-[25px]')
  })
})

describe('数据区骨架', () => {
  it('DataPanelSkeleton 与 Panel 同壳（圆角/描边/卡片底色）并带列头占位', () => {
    const { container } = render(<DataPanelSkeleton rows={4} />)
    const panel = container.querySelector('[data-slot="data-skeleton"]')
    expect(panel).toBeInTheDocument()
    expect(panel?.className).toContain('rounded-lg')
    expect(panel?.className).toContain('bg-card/95')
    expect(container.querySelectorAll('[data-slot="skeleton-row"]')).toHaveLength(4)
  })

  it('ListSkeleton 独立可用：页头/筛选条照常渲染时只占数据区', () => {
    const { container } = render(<ListSkeleton rows={2} />)
    expect(container.querySelector('[data-slot="list-skeleton"]')).toBeInTheDocument()
    expect(container.querySelectorAll('[data-slot="skeleton-row"]')).toHaveLength(2)
    // 不含任何列表外框：它要被塞进页内已有的 Panel 里，不能再自带一层壳。
    expect(container.querySelector('[data-slot="data-skeleton"]')).toBeNull()
  })

  it('骨架与真实数据互斥：数据就绪后由调用方换成真实内容，骨架不残留', () => {
    const { container, rerender } = render(<DataPanelSkeleton rows={2} />)
    expect(container.querySelector('[data-slot="data-skeleton"]')).toBeInTheDocument()

    rerender(<div data-slot="audit-table">真实内容</div>)
    expect(container.querySelector('[data-slot="data-skeleton"]')).toBeNull()
    expect(screen.getByText('真实内容')).toBeInTheDocument()
  })
})
