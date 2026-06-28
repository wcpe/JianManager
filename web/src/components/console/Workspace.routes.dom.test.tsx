import { describe, it, expect, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useConsoleStore } from '@/stores/console'
import Workspace from './Workspace'

/**
 * Workspace 路由（FR-215）：观测·统计占位页 + 同义旧路径重定向兼容（不 404）。
 * 用 /statistics 系（轻量占位页，无后端依赖）验证渲染与 /stats→/statistics 重定向。
 */
describe('Workspace 观测路由与重定向（FR-215）', () => {
  beforeEach(() => {
    loginMockUser()
    useConsoleStore.setState({ openInstanceId: null })
  })

  it('/statistics 渲染统计占位页（FR-220 待补）', async () => {
    renderWithProviders(<Workspace />, { route: '/statistics' })
    expect(await screen.findByText('统计页建设中（FR-220）')).toBeInTheDocument()
  })

  it('/stats 重定向到 /statistics（旧链接不 404）', async () => {
    renderWithProviders(<Workspace />, { route: '/stats' })
    expect(await screen.findByText('统计页建设中（FR-220）')).toBeInTheDocument()
    expect(window.location.pathname).toBe('/statistics')
  })
})
