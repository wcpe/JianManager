import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
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
    expect(screen.getAllByText('CPU 过载告警').length).toBeGreaterThan(0)
    expect(screen.getAllByText('实例崩溃告警').length).toBeGreaterThan(0)
  })

  it('② 按类型筛选「消息」→ 只剩站内信、无告警', async () => {
    loginMockUser()
    renderWithProviders(<NotificationCenterPage />)
    await screen.findByText('JDK 安装完成')

    // 来源筛选已收敛为分段控件（Segments/Segment）：每段是 role=tab，不再是 button。
    await userEvent.click(screen.getByRole('tab', { name: '消息' }))
    expect(await screen.findByText('JDK 安装完成')).toBeInTheDocument()
    // 告警规则名不应再出现。
    expect(screen.queryAllByText('CPU 过载告警')).toHaveLength(0)
    expect(screen.queryAllByText('实例崩溃告警')).toHaveLength(0)
  })

  it('② 按类型筛选「告警」→ 只剩告警、无站内信', async () => {
    loginMockUser()
    renderWithProviders(<NotificationCenterPage />)
    await screen.findAllByText('CPU 过载告警')

    // 来源筛选已收敛为分段控件（Segments/Segment）：每段是 role=tab，不再是 button。
    await userEvent.click(screen.getByRole('tab', { name: '告警' }))
    expect((await screen.findAllByText('CPU 过载告警')).length).toBeGreaterThan(0)
    expect(screen.queryAllByText('JDK 安装完成')).toHaveLength(0)
    expect(screen.queryAllByText('备份失败')).toHaveLength(0)
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
    await screen.findAllByText('CPU 过载告警')

    await userEvent.type(screen.getByPlaceholderText('搜索标题或内容…'), 'CPU')
    expect((await screen.findAllByText('CPU 过载告警')).length).toBeGreaterThan(0)
    // 关键字走 300ms 防抖（容器取数侧），过滤结果不会在按键当帧就到——等到不含关键字的条目消失。
    await waitFor(() => expect(screen.queryByText('JDK 安装完成')).toBeNull())
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
    const [alertTitle] = await screen.findAllByText('CPU 过载告警')
    const alertRow = alertTitle.closest('li') as HTMLElement
    expect(within(alertRow).getByText('查看告警详情')).toBeInTheDocument()
  })

  it('任务类站内信附「查看任务」入口（FR-226 通知-任务联动）', async () => {
    loginMockUser()
    renderWithProviders(<NotificationCenterPage />)
    // 「JDK 安装完成」站内信带 taskId（task-jdk-1）→ 行内出现「查看任务」一键跳任务中心。
    const taskRow = (await screen.findByText('JDK 安装完成')).closest('li') as HTMLElement
    expect(within(taskRow).getByText('查看任务')).toBeInTheDocument()
    // 非任务站内信（无 taskId，如「备份失败」）不出现「查看任务」。
    const backupRow = screen.getByText('备份失败').closest('li') as HTMLElement
    expect(within(backupRow).queryByText('查看任务')).toBeNull()
  })

  it('三态①：首屏取数在途显示加载态，而不是「暂无通知」', async () => {
    loginMockUser()
    // 延迟 200ms 才回响应：给「在途」态一个可观测窗口。
    mockInject('get', '/notifications/feed', { kind: 'delay', ms: 200, then: { kind: 'empty' } })
    renderWithProviders(<NotificationCenterPage />)

    expect(await screen.findByText('加载中...')).toBeInTheDocument()
    expect(screen.queryByText('暂无通知')).toBeNull()
    // 数据回来后落到空态。
    await waitFor(() => expect(screen.getByText('暂无通知')).toBeInTheDocument())
  })

  it('三态②：取数失败显示错误态，绝不能显示成「暂无通知」', async () => {
    loginMockUser()
    mockInject('get', '/notifications/feed', { kind: 'status', status: 500 })
    renderWithProviders(<NotificationCenterPage />)

    expect(await screen.findByRole('alert')).toHaveTextContent('错误')
    // 关键回归：失败不得被演成「你没有消息」。
    expect(screen.queryByText('暂无通知')).toBeNull()
  })

  it('未读态不只靠颜色：附带仅读屏可见的「未读」文本', async () => {
    loginMockUser()
    renderWithProviders(<NotificationCenterPage />)
    const row = (await screen.findByText('备份失败')).closest('li') as HTMLElement
    expect(within(row).getByText('未读')).toBeInTheDocument()
  })

  it('关键字防抖：连敲三个字符只多发一次带最终关键字的请求', async () => {
    loginMockUser()
    const urls: string[] = []
    server.use(
      http.get(API('/notifications/feed'), ({ request }) => {
        urls.push(request.url)
        return HttpResponse.json({ items: [], total: 0 })
      }),
    )
    renderWithProviders(<NotificationCenterPage />)
    await screen.findByText('暂无通知')
    expect(urls).toHaveLength(1) // 首屏（无关键字）

    await userEvent.type(screen.getByPlaceholderText('搜索标题或内容…'), 'CPU')

    await waitFor(() => expect(urls.some((u) => u.includes('keyword=CPU'))).toBe(true))
    // 关键不变量：带关键字的请求只有一次，且是最终值——中间的 C / CP 不许各发一次请求。
    const keywordUrls = urls.filter((u) => u.includes('keyword='))
    expect(keywordUrls).toHaveLength(1)
    expect(keywordUrls[0]).toContain('keyword=CPU')
    // 另有一次不带关键字的请求：首次键入会把页码归 1（容器的「改筛选即回第 1 页」），与防抖无关。
    expect(urls.filter((u) => !u.includes('keyword='))).toHaveLength(2)
  })
})
