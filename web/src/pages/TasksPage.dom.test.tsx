import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import TasksPage from './TasksPage'

/**
 * TasksPage 强断言（FR-208）：验种子任务渲染、展开看任务日志（详情联动）、错误注入显错误态。
 */
describe('TasksPage（mock 假后端）', () => {
  it('① 渲染出种子任务行', async () => {
    loginMockUser()
    renderWithProviders(<TasksPage />)
    expect(await screen.findByText('安装 JDK Temurin 21')).toBeInTheDocument()
    expect(screen.getByText('备份实例 survival')).toBeInTheDocument()
    expect(screen.getByText('安装便携运行时')).toBeInTheDocument()
  })

  it('② 展开任务 → 懒查详情日志联动出现', async () => {
    loginMockUser()
    renderWithProviders(<TasksPage />)
    const row = await screen.findByRole('button', { name: /安装 JDK Temurin 21/ })
    await userEvent.click(row)
    // 展开后 GET /tasks/task-jdk-1 拉到滚动日志
    expect(await screen.findByText(/解压到 \/opt\/jdk\/temurin-21/)).toBeInTheDocument()
  })

  it('② 失败任务展开 → 显示错误原因', async () => {
    loginMockUser()
    renderWithProviders(<TasksPage />)
    const row = await screen.findByRole('button', { name: /安装便携运行时/ })
    await userEvent.click(row)
    expect(await screen.findByText(/sha256 不匹配/)).toBeInTheDocument()
  })

  it('③ 注入 500 → 显示加载任务失败错误态', async () => {
    loginMockUser()
    mockInject('get', '/tasks', { kind: 'status', status: 500 })
    renderWithProviders(<TasksPage />)
    expect(await screen.findByText('加载任务失败')).toBeInTheDocument()
  })

  it('空态：注入空列表 → 显示暂无任务', async () => {
    loginMockUser()
    mockInject('get', '/tasks', { kind: 'empty' })
    renderWithProviders(<TasksPage />)
    expect(await screen.findByText('暂无任务')).toBeInTheDocument()
  })
})
