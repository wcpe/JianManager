import { describe, it, expect, beforeEach } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import InstanceConsolePage from './InstanceConsolePage'

/**
 * FR-313：崩溃诊断卡——实例控制台概览区的崩溃快照列表。
 * 数据来自 GET /instances/:id/crash-snapshots（devmock 种子：实例 3 两条、实例 2 无），
 * 覆盖列表渲染（倒序 + 退出码/信号/时长）、尾部输出展开（等宽 pre）、空态。
 */
describe('InstanceConsolePage 崩溃诊断卡（FR-313）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('渲染快照列表：倒序（最新在前）、含退出码/信号/运行时长', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })

    const card = await screen.findByTestId('crash-diagnostics')
    expect(within(card).getByText('崩溃诊断')).toBeInTheDocument()

    const rows = await within(card).findAllByRole('button', { expanded: false })
    expect(rows).toHaveLength(2)
    // 种子里 07-12（exit 137）晚于 07-10（exit 1），倒序应最新在前。
    expect(rows[0]).toHaveTextContent('退出码 137')
    expect(rows[0]).toHaveTextContent('信号 killed')
    expect(rows[0]).toHaveTextContent('运行时长 12m 34s')
    expect(rows[1]).toHaveTextContent('退出码 1')
    expect(rows[1]).toHaveTextContent('运行时长 2.3s')
    // 非信号退出不显示信号 pill。
    expect(rows[1]).not.toHaveTextContent('信号')
  })

  it('点击行展开尾部输出（等宽字体 pre），再点收起', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })

    const card = await screen.findByTestId('crash-diagnostics')
    const rows = await within(card).findAllByRole('button', { expanded: false })

    // 展开前尾部输出不可见。
    expect(within(card).queryByText(/Unable to access jarfile/)).not.toBeInTheDocument()

    await user.click(rows[1])
    expect(rows[1]).toHaveAttribute('aria-expanded', 'true')
    const output = within(card).getByText(/Unable to access jarfile/)
    // 等宽字体承载（spec §2）：尾部输出渲染在 font-mono 的 pre 中。
    expect(output.tagName).toBe('PRE')
    expect(output.className).toContain('font-mono')

    // 再点收起。
    await user.click(rows[1])
    expect(within(card).queryByText(/Unable to access jarfile/)).not.toBeInTheDocument()
  })

  it('无快照实例显示空态文案', async () => {
    // 实例 2（lobby-proxy）种子无崩溃快照。
    renderWithProviders(<InstanceConsolePage instanceId={2} />, { route: '/instances/2' })

    const card = await screen.findByTestId('crash-diagnostics')
    expect(await within(card).findByText('暂无崩溃记录，进程非正常退出时会自动留存现场')).toBeInTheDocument()
  })
})
