import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'

import { renderWithProviders } from '@/test/render'
import { TopLoadingBar } from './TopLoadingBar'

describe('TopLoadingBar（FR-243 加载进度条）', () => {
  it('渲染顶部加载进度条', () => {
    renderWithProviders(<TopLoadingBar />, { route: '/instances' })
    expect(screen.getByTestId('top-loading-bar')).toBeInTheDocument()
  })

  it('进度条固定在视口顶部，并暴露当前加载状态', () => {
    renderWithProviders(<TopLoadingBar />, { route: '/instances' })

    const track = screen.getByTestId('top-loading-track')
    const bar = screen.getByTestId('top-loading-bar')

    expect(track).toHaveAttribute('data-slot', 'top-loading-track')
    expect(track).toHaveClass('fixed')
    expect(bar).toHaveAttribute('data-loading', 'false')
  })

  it('空闲初始状态不播放进度动画，避免顶部闪烁', () => {
    renderWithProviders(<TopLoadingBar />, { route: '/instances' })

    const track = screen.getByTestId('top-loading-track')
    const bar = screen.getByTestId('top-loading-bar')

    expect(track).toHaveAttribute('data-visible', 'false')
    expect(bar).toHaveAttribute('data-visible', 'false')
  })
})
