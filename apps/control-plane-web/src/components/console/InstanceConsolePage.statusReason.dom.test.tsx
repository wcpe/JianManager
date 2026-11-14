import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { db } from '@jianmanager/devmock/db'
import { useAuthStore } from '@/stores/auth'
import type { MockInstance } from '@jianmanager/devmock/handlers/domains/instance'
import InstanceConsolePage from './InstanceConsolePage'

/** 
 *：实例启动失败原因可见性——控制台页失败原因横幅。
 * 展示条件只看 statusReason 非空、不看 status（Worker 心跳会把 CRASHED 冲回 STOPPED），
 * 再次启动后 CP transition 清空 reason，横幅随查询刷新消失（纯数据驱动、无本地残留态）。
 */
describe('InstanceConsolePage 失败原因横幅', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('statusReason 非空（CRASHED）时顶部显示「上次启动失败」横幅与原因全文', async () => {
    // creative-1（id=3）种子即 CRASHED + statusReason。
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })

    const banner = await screen.findByRole('alert')
    expect(banner).toHaveTextContent('上次启动失败')
    expect(banner).toHaveTextContent('实例未绑定 JDK，启动委托失败')
  })

  it('状态被心跳冲回 STOPPED 后横幅仍在（不以 CRASHED 为前置条件）', async () => {
    db<MockInstance>('instances').update(3, { status: 'STOPPED' })
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })

    const banner = await screen.findByRole('alert')
    expect(banner).toHaveTextContent('上次启动失败')
    expect(banner).toHaveTextContent('实例未绑定 JDK，启动委托失败')
  })

  it('启动中的阶段说明不显示为上次启动失败', async () => {
    db<MockInstance>('instances').update(3, { status: 'STARTING', statusReason: '正在等待上一代实例退出' })
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })
    const banner = await screen.findByTestId('instance-lifecycle-banner')
    expect(banner).toHaveAttribute('role', 'status')
    expect(banner).toHaveTextContent('正在等待上一代实例退出')
    expect(banner).not.toHaveTextContent('上次启动失败')
  })

  it('结果待确认单独显示为进行中状态，不能宣称失败', async () => {
    db<MockInstance>('instances').update(3, { status: 'STARTING', statusReason: '操作结果待确认，状态查询/心跳收敛: 连接断开' })
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })
    const banner = await screen.findByTestId('instance-lifecycle-banner')
    expect(banner).toHaveAttribute('role', 'status')
    expect(banner).toHaveTextContent('操作结果待确认')
    expect(banner).not.toHaveTextContent('上次启动失败')
  })

  it('再次点启动后 reason 被清空，横幅随查询刷新消失', async () => {
    // 启停现在要二次确认（与列表页同口径）。DangerConfirm 的角色门禁读 authStore.role，
    // 而 mock token 解不出 role（为 null 时一律拒），所以这里显式给平台管理员角色。
    useAuthStore.setState({ role: 10 })
    const user = userEvent.setup()
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })
    await screen.findByRole('alert')

    await user.click(screen.getByRole('button', { name: '启动' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '启动' }))

    // 只断言「上次启动失败」横幅消失。不能用 queryByRole('alert')——
    // 确认弹窗自身（以及其它提示）也带 role=alert，会把结果误判成未消失。
    await waitFor(() => expect(screen.queryByText('上次启动失败')).not.toBeInTheDocument())
  })
})
