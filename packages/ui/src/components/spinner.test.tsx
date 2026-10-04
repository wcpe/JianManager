import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { Spinner } from './spinner'

/**
 * Spinner 测试（FR-496 阶段 6）。
 *
 * 契约：默认装饰性（对辅助技术隐藏，加载语义由容器的 aria-busy/文案表达），
 * 显式给 label 时才成为有名字的图像；尺寸走 data-size 钩子，便于样式与测试定位。
 */
describe('Spinner', () => {
  it('默认是装饰性的：隐藏于辅助技术且不占用角色', () => {
    render(<Spinner data-testid="spinner" />)

    const spinner = screen.getByTestId('spinner')
    expect(spinner).toHaveAttribute('data-slot', 'spinner')
    expect(spinner).toHaveAttribute('aria-hidden', 'true')
    expect(spinner).not.toHaveAttribute('role')
    expect(screen.queryByRole('img')).toBeNull()
  })

  it('无 label 时不进入可访问树（否则读屏会多念一个无名图像）', () => {
    render(<Spinner data-testid="spinner" />)

    const spinner = screen.getByTestId('spinner')
    expect(spinner.getAttribute('aria-label')).toBeNull()
    // aria-hidden 的元素对按角色查询不可见（dom-accessibility-api 会跳过）
    expect(screen.queryByRole('img')).toBeNull()
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('给了 label 时成为有名字的 img，并向辅助技术暴露', () => {
    render(<Spinner label="正在加载实例列表" />)

    const spinner = screen.getByRole('img', { name: '正在加载实例列表' })
    expect(spinner).toHaveAttribute('data-slot', 'spinner')
    expect(spinner).not.toHaveAttribute('aria-hidden')
  })

  it('尺寸走 size 与 data-size 钩子', () => {
    const { unmount } = render(<Spinner size="xs" data-testid="spinner" />)
    // SVG 元素的 className 是 SVGAnimatedString，故按 class 属性断言
    expect(screen.getByTestId('spinner')).toHaveAttribute('data-size', 'xs')
    expect(screen.getByTestId('spinner')).toHaveClass('size-3')
    unmount()

    render(<Spinner size="lg" data-testid="spinner" />)
    expect(screen.getByTestId('spinner')).toHaveAttribute('data-size', 'lg')
    expect(screen.getByTestId('spinner')).toHaveClass('size-5')
  })

  it('className 追加而非替换，且动画类始终保留', () => {
    render(<Spinner className="custom-spinner" data-testid="spinner" />)

    expect(screen.getByTestId('spinner')).toHaveClass('custom-spinner', 'animate-spin')
  })
})
