import { describe, it, expect } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { db } from '@jianmanager/devmock/db'
import TasksPage from './TasksPage'

/**
 * TasksPage 强断言（FR-208）：验种子任务渲染、展开看任务日志（详情联动）、错误注入显错误态；
 * 分页信封（FR-337）：共 N 条/已加载数、「加载更多」增长窗口、筛选变化复位窗口。
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

  it('② 网络类下载失败展开 → 显示代理/镜像可操作入口（FR-279）', async () => {
    loginMockUser()
    renderWithProviders(<TasksPage />)
    const row = await screen.findByRole('button', { name: /受限网络下装 JDK 21（网络失败）/ })
    await userEvent.click(row)
    expect(await screen.findByText(/出站网络不可达/)).toBeInTheDocument()
    const proxyLink = screen.getByRole('link', { name: /去配置出站代理/ })
    expect(proxyLink).toHaveAttribute('href', '/settings')
    const mirrorLink = screen.getByRole('link', { name: /更换下载源\/镜像/ })
    expect(mirrorLink).toHaveAttribute('href', '/runtime-assets')
  })

  it('② 非网络类失败不显示网络引导入口（FR-279 不误报）', async () => {
    loginMockUser()
    renderWithProviders(<TasksPage />)
    const row = await screen.findByRole('button', { name: /安装便携运行时/ })
    await userEvent.click(row)
    await screen.findByText(/sha256 不匹配/)
    expect(screen.queryByRole('link', { name: /去配置出站代理/ })).not.toBeInTheDocument()
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

  it('④ 分页信封（FR-337）：共 N 条/已加载数、加载更多扩窗且保持筛选、筛选变化复位窗口', async () => {
    loginMockUser()
    // 直接从假后端集合算期望值，不硬编码种子规模（数百任务种子，见 observ.ts MOCK_TASK_COUNT）。
    const allTasks = db<{ title: string }>('tasks').list()
    const seededTotal = allTasks.length
    const filteredTotal = allTasks.filter((t) => t.title.includes('批量任务')).length
    expect(seededTotal).toBeGreaterThan(200) // 前置：种子须超过两窗，否则下方断言失真
    renderWithProviders(<TasksPage />)

    // 首窗：limit 缺省 100，顶部「共 N 条 · 已加载 100」。
    expect(await screen.findByText(`共 ${seededTotal} 条 · 已加载 100`)).toBeInTheDocument()

    // 「加载更多」→ 窗口扩到 200，总数不变。
    await userEvent.click(screen.getByRole('button', { name: '加载更多' }))
    expect(await screen.findByText(`共 ${seededTotal} 条 · 已加载 200`)).toBeInTheDocument()

    // 改关键词筛选 → 窗口复位 100，total 随筛选口径收窄。
    fireEvent.change(screen.getByPlaceholderText('搜索标题 / 详情'), { target: { value: '批量任务' } })
    expect(await screen.findByText(`共 ${filteredTotal} 条 · 已加载 100`)).toBeInTheDocument()

    // 再「加载更多」→ 在同一筛选结果集内扩窗（加载更多保持筛选参数）。
    await userEvent.click(screen.getByRole('button', { name: '加载更多' }))
    expect(await screen.findByText(`共 ${filteredTotal} 条 · 已加载 200`)).toBeInTheDocument()
  })
})
