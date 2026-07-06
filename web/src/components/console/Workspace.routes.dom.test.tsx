import { describe, it, expect, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import Workspace from './Workspace'

vi.mock('@/pages/StatisticsPage', () => ({
  default: () => <h1>统计</h1>,
}))

vi.mock('@/pages/InstanceDetailPage', () => ({
  default: () => <h1>服务器控制台 / survival-1</h1>,
}))

/**
 * Workspace 路由（FR-215 IA + FR-220 统计页）：观测·统计页 + 同义旧路径重定向兼容（不 404）。
 * 用 /statistics 系验证渲染与 /stats→/statistics 重定向。FR-220 后统计页已是平台级聚合页，
 * 以稳定标题「统计」断言（非管理员态：节点/实例维度照常，分发区块降级，不依赖后端 seed）。
 */
describe('Workspace 观测路由与重定向（FR-215/FR-220）', () => {
  it('/statistics 渲染统计页（FR-220 平台聚合）', async () => {
    const { container } = renderWithProviders(<Workspace />, { route: '/statistics' })
    expect(await screen.findByRole('heading', { name: '统计' })).toBeInTheDocument()
    expect(container.querySelector('[data-slot="workspace-route-transition"]')).toBeInTheDocument()
  })

  it('/stats 重定向到 /statistics（旧链接不 404）', async () => {
    renderWithProviders(<Workspace />, { route: '/stats' })
    expect(await screen.findByRole('heading', { name: '统计' })).toBeInTheDocument()
    expect(window.location.pathname).toBe('/statistics')
  })

  it('实例深链直接渲染实例工作区，不依赖临时桥接状态', async () => {
    renderWithProviders(<Workspace />, { route: '/instances/1' })

    expect(await screen.findByText(/服务器控制台 \/ survival-1/)).toBeInTheDocument()
    expect(window.location.pathname).toBe('/instances/1')
  })
})
