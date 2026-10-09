import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'

import { TopLoadingBar } from '@/components/views/TopLoadingBar'

/**
 * TopLoadingBar（FR-243 加载进度条）· 业务视图包。
 *
 * 【为什么直接 render 而不套 renderWithProviders】本组件已受控：它不再自行读取
 * 路由与请求状态，因此**不需要** Router 与 QueryClient 这两层 Provider。
 * 这正是从主控台迁入本包时要保留的性质——若哪天有人给它加回 `useLocation`
 * 或 `useIsFetching`，这些用例会立即因缺少 Provider 而失败，从而挡住回退。
 */
function renderBar(props: { routeKey?: string; pendingCount?: number } = {}) {
  return render(<TopLoadingBar routeKey={props.routeKey ?? '/instances'} pendingCount={props.pendingCount ?? 0} />)
}

describe('TopLoadingBar（FR-243 加载进度条）', () => {
  it('渲染顶部加载进度条', () => {
    renderBar()
    expect(screen.getByTestId('top-loading-bar')).toBeInTheDocument()
  })

  it('进度条固定在视口顶部，并暴露当前加载状态', () => {
    renderBar()

    const track = screen.getByTestId('top-loading-track')
    const bar = screen.getByTestId('top-loading-bar')

    expect(track).toHaveAttribute('data-slot', 'top-loading-track')
    expect(track).toHaveClass('fixed')
    expect(bar).toHaveAttribute('data-loading', 'false')
  })

  it('空闲初始状态不播放进度动画，避免顶部闪烁', () => {
    renderBar()

    const track = screen.getByTestId('top-loading-track')
    const bar = screen.getByTestId('top-loading-bar')

    expect(track).toHaveAttribute('data-visible', 'false')
    expect(bar).toHaveAttribute('data-visible', 'false')
  })

  it('pendingCount > 0 时标记为加载中（受控：由外壳注入的忙碌数驱动）', () => {
    renderBar({ pendingCount: 2 })

    expect(screen.getByTestId('top-loading-track')).toHaveAttribute('data-loading', 'true')
    expect(screen.getByTestId('top-loading-bar')).toHaveAttribute('data-mode', 'route')
  })
})
