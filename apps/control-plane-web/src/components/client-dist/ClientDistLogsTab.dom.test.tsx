import { describe, it, expect, beforeAll } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import ClientDistLogsTab from './ClientDistLogsTab'

/**
 * ClientDistLogsTab（FR-430 / ADR-088 去重合并）：单一入口按 `type` 双视图。
 * - `type=request`（缺省）：加厚请求表 + 脱敏详情（FR-357/FR-265）。
 * - `type=all`/其余 5 类：安全聚合多类型表（hello/risk/action/request/runtime/telemetry）。
 */
beforeAll(() => {
  if (!('ResizeObserver' in globalThis)) {
    class RO {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = RO
  }
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
  if (!Element.prototype.setPointerCapture) Element.prototype.setPointerCapture = () => {}
})

describe('ClientDistLogsTab', () => {
  it('缺省 type=request：出加厚请求表，可打开脱敏详情（X-Client-Key 仅 present，无明文）', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(
      <ClientDistLogsTab channelId={undefined} range="7d" enabled link={{}} onClearLink={() => {}} />,
      { route: '/client-dist-ops?tab=logs' },
    )

    expect(await screen.findByText('分发请求日志')).toBeInTheDocument()
    expect(await screen.findByRole('columnheader', { name: '错误码' })).toBeInTheDocument()
    // 加厚列（FR-357）
    expect(screen.getByRole('columnheader', { name: '玩家名' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'Core 版本' })).toBeInTheDocument()
    expect(screen.getByText('INVALID_CLIENT_KEY')).toBeInTheDocument()

    const detailButtons = await screen.findAllByRole('button', { name: '详情' })
    await user.click(detailButtons[0])

    expect(await screen.findByText('请求脱敏详情')).toBeInTheDocument()
    expect(screen.getByText('请求头（已脱敏）')).toBeInTheDocument()
    expect(screen.getByText('X-Client-Key')).toBeInTheDocument()
    expect(screen.getByText('present')).toBeInTheDocument()
    expect(screen.queryByText('secret')).not.toBeInTheDocument()
  })

  it('type=all：出安全聚合多类型表（含 hello/telemetry）', async () => {
    loginMockUser()
    renderWithProviders(
      <ClientDistLogsTab channelId={undefined} range="7d" enabled link={{}} onClearLink={() => {}} />,
      { route: '/client-dist-ops?tab=logs&type=all' },
    )

    expect(await screen.findByText('全量日志详情')).toBeInTheDocument()
    const table = await screen.findByRole('table')
    expect(within(table).getByText('Security Hello')).toBeInTheDocument()
    expect(within(table).getAllByText('更新遥测').length).toBeGreaterThan(0)
    expect(within(table).getByRole('columnheader', { name: '对象' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: '状态 / 错误' })).toBeInTheDocument()
  })

  it('联动过滤态展示脱敏标签与「打开安全中心 / 打开频道工作台」页内切换链接', async () => {
    loginMockUser()
    renderWithProviders(
      <ClientDistLogsTab
        channelId={undefined}
        range="7d"
        enabled
        link={{ machineId: 'm-aaaa', ip: '203.0.113.1', errCode: 'INVALID_CLIENT_KEY' }}
        onClearLink={() => {}}
      />,
      { route: '/client-dist-ops?tab=logs&channelId=skyblock-s1' },
    )

    expect(await screen.findByText('已联动过滤日志：')).toBeInTheDocument()
    expect(screen.getByText('machine=m-aaaa')).toBeInTheDocument()
    const securityLink = screen.getByRole('link', { name: '打开安全中心' })
    expect(securityLink).toHaveAttribute('href', expect.stringContaining('/client-dist-ops?'))
    expect(securityLink).toHaveAttribute('href', expect.stringContaining('type=all'))
    const channelLink = screen.getByRole('link', { name: '打开频道工作台' })
    expect(channelLink).toHaveAttribute('href', expect.stringContaining('/client-channels?'))
  })
})
