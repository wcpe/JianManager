import { describe, it, expect } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import AlertsPage from './AlertsPage'

/**
 * AlertsPage 强断言（FR-208 可观测日志域）：验种子规则/事件/通道渲染、写操作联动、错误注入。
 * 渲染前 loginMockUser() 让 requireAuth 保护的 alerts 端点放行。
 * RuleDialog/ChannelDialog 统一走共享 Dialog，必须有 role=dialog 并支持 Esc 关闭。
 * 事件筛选栏统一走 @jianmanager/ui 的 Select（Radix 组合控件），原生 select 直选不适用。
 */
describe('AlertsPage（mock 假后端）', () => {
  /** 打开 Radix Select 并选中指定选项（triggers 传可见文本节点即可，内部会向上找 button）。 */
  async function pickSelectOption(trigger: HTMLElement, optionName: string) {
    HTMLElement.prototype.hasPointerCapture ??= () => false
    HTMLElement.prototype.setPointerCapture ??= () => {}
    HTMLElement.prototype.releasePointerCapture ??= () => {}
    HTMLElement.prototype.scrollIntoView ??= () => {}
    await userEvent.click(trigger.closest('button') ?? trigger)
    await userEvent.click(await screen.findByRole('option', { name: optionName }))
  }

  it('① 渲染出种子告警规则', async () => {
    loginMockUser()
    renderWithProviders(<AlertsPage />)
    expect(await screen.findByText('CPU 过载告警')).toBeInTheDocument()
    expect(screen.getByText('实例崩溃告警')).toBeInTheDocument()
  })

  it('② 事件页：渲染种子事件，级别筛选 critical 后联动收敛', async () => {
    loginMockUser()
    renderWithProviders(<AlertsPage />)
    await screen.findByText('CPU 过载告警')

    // 事件 Tab 按钮名含未读角标数字（如「事件1」），用前缀匹配。
    await userEvent.click(screen.getByRole('tab', { name: /^事件/ }))
    expect(await screen.findByText(/CPU 使用率 91\.5%/)).toBeInTheDocument()
    expect(screen.getAllByText(/异常退出/).length).toBeGreaterThan(0)

    // 级别下拉（组件库 Select）选 critical → 仅崩溃事件保留，CPU 事件消失（跨 endpoint 联动）。
    await pickSelectOption(screen.getByText('全部级别'), '严重')
    await waitFor(() => expect(screen.queryByText(/CPU 使用率 91\.5%/)).not.toBeInTheDocument())
    expect(screen.getAllByText(/异常退出/).length).toBeGreaterThan(0)
  })

  it('② 事件页：按触发类型筛选 instance_crash', async () => {
    loginMockUser()
    renderWithProviders(<AlertsPage />)
    await screen.findByText('CPU 过载告警')

    await userEvent.click(screen.getByRole('tab', { name: /^事件/ }))
    expect(await screen.findByText(/CPU 使用率 91\.5%/)).toBeInTheDocument()

    // 触发类型下拉（组件库 Select）选 instance_crash → 仅崩溃事件保留。
    await pickSelectOption(screen.getByText('全部触发类型'), '实例崩溃')

    await waitFor(() => expect(screen.queryByText(/CPU 使用率 91\.5%/)).not.toBeInTheDocument())
    expect(screen.getAllByText(/异常退出/).length).toBeGreaterThan(0)
  })

  it('② 创建规则 → 列表联动出现新规则', async () => {
    loginMockUser()
    renderWithProviders(<AlertsPage />)
    await screen.findByText('CPU 过载告警')

    await userEvent.click(screen.getByRole('button', { name: /创建规则/ }))
    const heading = await screen.findByRole('heading', { name: '创建规则' })
    const panel = heading.closest('[role="dialog"]') as HTMLElement
    expect(panel).toBeInTheDocument()
    const nameInput = within(panel).getAllByRole('textbox')[0]
    await userEvent.type(nameInput, 'TPS 过低告警')
    await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

    expect(await screen.findByText('TPS 过低告警')).toBeInTheDocument()
  })

  it('② 创建通道对话框有共享 Dialog 语义并支持 Esc 关闭', async () => {
    loginMockUser()
    renderWithProviders(<AlertsPage />)
    await screen.findByText('CPU 过载告警')

    await userEvent.click(screen.getByRole('tab', { name: /^通道/ }))
    await userEvent.click(screen.getByRole('button', { name: /新建通道/ }))
    const heading = await screen.findByRole('heading', { name: '新建通道' })
    const dialog = heading.closest('[role="dialog"]') as HTMLElement
    expect(dialog).toBeInTheDocument()

    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('heading', { name: '新建通道' })).not.toBeInTheDocument())
  })

  it('③ 注入 500 → 规则列表显示空态（非崩溃）', async () => {
    loginMockUser()
    mockInject('get', '/alerts/rules', { kind: 'status', status: 500 })
    renderWithProviders(<AlertsPage />)
    // 规则查询失败 → rules 为 undefined → 走空态文案，页面不崩溃。
    expect(await screen.findByText('暂无告警规则')).toBeInTheDocument()
  })

  it('② 通道页不再内嵌「QQ 接入」小节：群聊下线后该节整体移除', async () => {
    loginMockUser()
    renderWithProviders(<AlertsPage />)
    await screen.findByText('CPU 过载告警')

    await userEvent.click(screen.getByRole('tab', { name: /^通道/ }))
    // 该小节（分享二维码 + 网关状态 + 已发现群）整体服务于「拉机器人进群」，
    // 群聊路径因平台拒绝群主动消息而下线后，它只会误导运维（文案还写着拉进群）。
    // 扫码绑定能力保留在通道对话框内，不占通道页版面。
    expect(await screen.findByText('运维 Webhook')).toBeInTheDocument()
    expect(screen.queryByText('QQ 接入')).not.toBeInTheDocument()
    expect(screen.queryByTestId('qq-share-qrcode')).not.toBeInTheDocument()
    expect(screen.queryByText('已发现群')).not.toBeInTheDocument()
  })

})
