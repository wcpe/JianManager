import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import '@/i18n'
import { SessionBots } from './SessionBots'

/**
 * 会话 Bot 明细表的错误态回归锁（FR-371）。
 *
 * 取数失败原先没有任何错误态（容器有 `isError` 但没传），失败会被渲染成「无 Bot」——
 * 即把一次故障读成「这一批没有起 Bot」，是压测排查里最危险的假信号。
 */
describe('SessionBots 错误态', () => {
  const base = {
    filter: { page: 1 },
    onFilterChange: () => {},
  }

  it('取数失败：显示加载失败而不是「无 Bot」', () => {
    const { getByText, queryByText } = render(
      <SessionBots {...base} data={{ items: [], total: 0 }} isLoading={false} isError />,
    )
    expect(getByText('加载运行详情失败')).toBeInTheDocument()
    expect(queryByText('无 Bot')).not.toBeInTheDocument()
  })

  it('取数成功但确实没有 Bot：仍显示「无 Bot」', () => {
    const { getByText, queryByText } = render(
      <SessionBots {...base} data={{ items: [], total: 0 }} isLoading={false} isError={false} />,
    )
    expect(getByText('无 Bot')).toBeInTheDocument()
    expect(queryByText('加载运行详情失败')).not.toBeInTheDocument()
  })

  it('状态列走 bots.status_* 文案（未知枚举回退原值）', () => {
    const { getByText } = render(
      <SessionBots
        {...base}
        isLoading={false}
        data={{
          total: 2,
          items: [
            { id: 1, uuid: 'u-1', name: 'bot-a', status: 'connected', reconnectCount: 0 },
            { id: 2, uuid: 'u-2', name: 'bot-b', status: 'weird-state', reconnectCount: 0 },
          ],
        }}
      />,
    )
    expect(getByText('已连接')).toBeInTheDocument()
    expect(getByText('weird-state')).toBeInTheDocument()
  })
})
