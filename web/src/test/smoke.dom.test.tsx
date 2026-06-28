import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'

/** render harness 冒烟：证明 jsdom + testing-library + Provider 链可渲染并断言（FR-196）。 */
function Hello() {
  return <h1>mock 基座就绪</h1>
}

describe('render harness 冒烟（FR-196）', () => {
  it('能渲染组件并按可访问名断言', () => {
    renderWithProviders(<Hello />)
    expect(screen.getByRole('heading', { name: 'mock 基座就绪' })).toBeInTheDocument()
  })
})
