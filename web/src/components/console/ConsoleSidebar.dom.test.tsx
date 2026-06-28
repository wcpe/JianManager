import { describe, it, expect, beforeEach } from 'vitest'
import { screen, within } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import ConsoleSidebar from './ConsoleSidebar'

/**
 * ConsoleSidebar 信息架构（FR-215）：「观测」域含 监控总览/日志/统计；任务中心迁「系统」；
 * 告警过渡留位仍可达。验侧栏渲染出正确 IA、链接指向正确路由。
 */
describe('ConsoleSidebar 观测导航重构（FR-215）', () => {
  beforeEach(() => {
    // 已登录态：折叠态侧栏在「集群」域内嵌实例树/节点切换会触发 useInstances/useNodes，
    // 登录后这些查询命中假后端正常返回（否则触发刷新而无 refreshToken 抛未处理拒绝）。
    loginMockUser()
    // 展开态；折叠「集群」域避免内嵌实例树/节点切换的异步副作用干扰 IA 断言。
    useConsoleStore.setState({ sidebarCollapsed: false, collapsedGroups: { cluster: true } })
    // 普通用户即可（平台管理员仅额外注入 数据库/系统更新，不影响本 IA 断言）。
    useAuthStore.setState({ role: 1 })
  })

  it('出现「观测」一级域，且不再有一级「监控」域名', () => {
    renderWithProviders(<ConsoleSidebar />)
    // 「观测」域头（可展开分组按钮）。
    expect(screen.getByRole('button', { name: '观测' })).toBeInTheDocument()
    // 不应再有「监控」作为一级域按钮（监控仅作为观测下子项「监控总览」存在）。
    expect(screen.queryByRole('button', { name: '监控' })).toBeNull()
  })

  it('观测域下含 监控总览/日志/统计 三子项，链接正确', () => {
    renderWithProviders(<ConsoleSidebar />)
    expect(screen.getByRole('link', { name: '监控总览' })).toHaveAttribute('href', '/monitor')
    expect(screen.getByRole('link', { name: '日志' })).toHaveAttribute('href', '/logs')
    expect(screen.getByRole('link', { name: '统计' })).toHaveAttribute('href', '/statistics')
  })

  it('任务中心迁到「系统」，不在「观测」域', () => {
    renderWithProviders(<ConsoleSidebar />)
    const tasks = screen.getByRole('link', { name: '任务中心' })
    expect(tasks).toHaveAttribute('href', '/tasks')

    // 任务中心不应落在「观测」分组容器内。
    const obsHeader = screen.getByRole('button', { name: '观测' })
    const obsGroup = obsHeader.parentElement as HTMLElement
    expect(within(obsGroup).queryByRole('link', { name: '任务中心' })).toBeNull()

    // 任务中心应落在「系统」分组容器内。
    const sysHeader = screen.getByRole('button', { name: '系统' })
    const sysGroup = sysHeader.parentElement as HTMLElement
    expect(within(sysGroup).getByRole('link', { name: '任务中心' })).toBeInTheDocument()
  })

  it('告警过渡留位仍在观测域内可达（FR-216 接手前）', () => {
    renderWithProviders(<ConsoleSidebar />)
    const alerts = screen.getByRole('link', { name: '告警' })
    expect(alerts).toHaveAttribute('href', '/alerts')
    const obsHeader = screen.getByRole('button', { name: '观测' })
    const obsGroup = obsHeader.parentElement as HTMLElement
    expect(within(obsGroup).getByRole('link', { name: '告警' })).toBeInTheDocument()
  })
})
