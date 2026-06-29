import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'

import { renderWithProviders } from '@/test/render'
import { TopLoadingBar } from './TopLoadingBar'

describe('TopLoadingBar（FR-243 加载进度条）', () => {
  it('渲染顶部加载进度条', () => {
    renderWithProviders(<TopLoadingBar />, { route: '/instances' })
    expect(screen.getByTestId('top-loading-bar')).toBeInTheDocument()
  })
})
