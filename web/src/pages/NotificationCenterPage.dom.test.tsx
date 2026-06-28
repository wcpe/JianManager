import { describe, it, expect } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import NotificationCenterPage from './NotificationCenterPage'

/**
 * NotificationCenterPage 强断言（FR-216）：统一通知流消费 mock 假后端聚合
 * （3 站内信 + 2 告警事件）；验合并渲染、按类型筛选[消息/告警]、仅未读、全部已读。
 */
describe('NotificationCenterPage（mock 假后端，统一通知流）', () => {
  it('① 合并渲染站内信 + 告警条目', async () => {
    loginMockUser()
    renderWithProviders(<NotificationCenterPage />)
    // 站内信（message）：种子标题。
    expect(await screen.findByText('JDK 安装完成')).toBeInTheDocument()
    expect(screen.getByText('备份失败')).toBeInTheDocument()
    // 告警（alert）：标题取规则名。
    expect(screen.getByText('CPU 过载告警')).toBeInTheDocument()
    expect(screen.getByText('实例崩溃告警')).toBeInTheDocument()
  })

  it('② 按类型筛选「消息」→ 只剩站内信、无告警', async () => {
    loginMockUser()
    renderWithProviders(<NotificationCenterPage />)
    await screen.findByText('JDK 安装完成')

    await userEvent.click(screen.getByRole('button', { name: '消息' }))
    expect(await screen.findByText('JDK 安装完成')).toBeInTheDocument()
    // 告警规则名不应再出现。
    expect(screen.queryByText('CPU 过载告警')).toBeNull()
    expect(screen.queryByText('实例崩溃告警')).toBeNull()
  })

  it('② 按类型筛选「告警」→ 只剩告警、无站内信', async () => {
    loginMockUser()
    renderWithProviders(<NotificationCenterPage />)
    await screen.findByText('CPU 过载告警')

    await userEvent.click(screen.getByRole('button', { name: '告警' }))
    expect(await screen.findByText('CPU 过载告警')).toBeInTheDocument()
    expect(screen.queryByText('JDK 安装完成')).toBeNull()
    expect(screen.queryByText('备份失败')).toBeNull()
  })

  it('③ 仅未读筛选 → 已读条目（节点已上线）不显示', async () => {
    loginMockUser()
    renderWithProviders(<NotificationCenterPage />)
    // 种子里「节点已上线」是已读站内信。
    expect(await screen.findByText('节点已上线')).toBeInTheDocument()

    await userEvent.click(screen.getByLabelText('仅未读'))
    // 未读筛选后已读条目消失，未读条目仍在。
    expect(await screen.findByText('备份失败')).toBeInTheDocument()
    expect(screen.queryByText('节点已上线')).toBeNull()
  })

  it('④ 关键字查询命中告警正文', async () => {
    loginMockUser()
    renderWithProviders(<NotificationCenterPage />)
    await screen.findByText('CPU 过载告警')

    await userEvent.type(screen.getByPlaceholderText('搜索标题或内容…'), 'CPU')
    expect(await screen.findByText('CPU 过载告警')).toBeInTheDocument()
    expect(screen.queryByText('JDK 安装完成')).toBeNull()
  })

  it('⑤ 全部已读 → 行内「标记已读」按钮消失', async () => {
    loginMockUser()
    renderWithProviders(<NotificationCenterPage />)
    await screen.findByText('备份失败')
    // 有未读条目时存在「标记已读」按钮。
    expect(screen.getAllByText('标记已读').length).toBeGreaterThan(0)

    await userEvent.click(screen.getByRole('button', { name: '全部已读' }))
    // 全读后不再有任何「标记已读」按钮。
    await screen.findByText('备份失败')
    expect(screen.queryByText('标记已读')).toBeNull()
  })

  it('空态：注入空列表 → 显示暂无通知', async () => {
    loginMockUser()
    mockInject('get', '/notifications/feed', { kind: 'status', status: 200, body: { items: [], total: 0 } })
    renderWithProviders(<NotificationCenterPage />)
    expect(await screen.findByText('暂无通知')).toBeInTheDocument()
  })

  it('告警条目附「查看告警详情」入口', async () => {
    loginMockUser()
    renderWithProviders(<NotificationCenterPage />)
    const alertRow = (await screen.findByText('CPU 过载告警')).closest('li') as HTMLElement
    expect(within(alertRow).getByText('查看告警详情')).toBeInTheDocument()
  })
})
