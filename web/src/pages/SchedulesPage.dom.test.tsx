import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import { mockInject } from '@/mocks/inject'
import SchedulesPage from './SchedulesPage'

/**
 * 定时任务页（FR-207 域簇）。三条强断言：
 * ① 渲染出 seed 任务；② 切换启用（PUT 写）→ 开关状态联动翻转；③ 注入 500 → 显空态（不崩溃）。
 * SchedulesPage 同时拉 /instances（属实例域，本域不重定义），测试内用 server.use 临时桩。
 */
describe('SchedulesPage（mock）', () => {
  beforeEach(() => {
    loginMockUser() // 受 requireAuth 保护，渲染前置已登录态
    // /instances 由实例域负责；本域测试只需一个最小桩供 instanceName 展示。
    server.use(
      http.get(API('/instances'), () =>
        HttpResponse.json([{ id: 1, name: 'survival', uuid: 'i-1' }]),
      ),
    )
  })

  it('渲染 seed 定时任务', async () => {
    renderWithProviders(<SchedulesPage />)
    expect(await screen.findByText('每晚重启')).toBeInTheDocument()
    expect(screen.getByText('每日备份')).toBeInTheDocument()
    // cron 表达式原样展示（list 视图）。
    expect(screen.getByText('0 4 * * *')).toBeInTheDocument()
  })

  it('切换启用状态 → 开关联动翻转（PUT 写联动）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<SchedulesPage />)
    await screen.findByText('每晚重启')

    const row = screen.getByRole('row', { name: /每晚重启/ })
    const toggle = within(row).getByRole('switch')
    expect(toggle).toHaveAttribute('aria-checked', 'true') // seed 为启用

    await user.click(toggle)

    // PUT /schedules/1 写入 enabled=false → 列表失效重取 → 开关翻转为关闭。
    await waitFor(() => {
      const after = within(screen.getByRole('row', { name: /每晚重启/ })).getByRole('switch')
      expect(after).toHaveAttribute('aria-checked', 'false')
    })
  })

  it('注入 500 → 显示空态而非崩溃', async () => {
    mockInject('get', '/schedules', { kind: 'status', status: 500 })
    renderWithProviders(<SchedulesPage />)
    // 列表查询失败 → schedules 为 undefined → 渲染空态文案，页面不崩。
    expect(await screen.findByText('暂无定时任务')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByText('每晚重启')).not.toBeInTheDocument()
    })
  })
})
