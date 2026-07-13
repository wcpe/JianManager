import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { db } from '@/mocks/db'
import type { MockInstance } from '@/mocks/handlers/domains/instance'
import InstanceConsolePage from './InstanceConsolePage'

/**
 * FR-312：实例启动失败原因可见性——控制台页失败原因横幅。
 * 展示条件只看 statusReason 非空、不看 status（Worker 心跳会把 CRASHED 冲回 STOPPED），
 * 再次启动后 CP transition 清空 reason，横幅随查询刷新消失（纯数据驱动、无本地残留态）。
 */
describe('InstanceConsolePage 失败原因横幅（FR-312）', () => {
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

  it('再次点启动后 reason 被清空，横幅随查询刷新消失', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })
    await screen.findByRole('alert')

    await user.click(screen.getByRole('button', { name: '启动' }))

    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
  })
})
