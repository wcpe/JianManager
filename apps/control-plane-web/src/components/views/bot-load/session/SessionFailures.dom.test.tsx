import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import '@/i18n'
import { SessionFailures } from './SessionFailures'
import type { BotLoadFailure } from '@/lib/bot-load/bot-load-types'

/** 
 * 会话失败明细的错误态回归锁。
 *
 * 压测判定正是靠失败明细下结论，故「取数失败」必须与「确实没有失败」区分开：
 * 原先容器有 `isError` 却没传下来，任何取数故障都会被渲染成「无失败记录」。
 */
describe('SessionFailures 错误态', () => {
  const base = {
    filter: { page: 1 },
    onFilterChange: () => {},
    failureSummary: {},
    onRetry: () => {},
  }

  const failure: BotLoadFailure = {
    id: 'f-1',
    runUuid: 'run-1',
    botUuid: 'bot-uuid-1',
    category: 'target',
    errorCode: 'CONNECT_REFUSED',
    message: '拒绝连接',
    retryable: true,
    occurredAt: '2026-09-01T00:00:00Z',
  }

  it('取数失败：显示加载失败而不是「无失败记录」', () => {
    const { getByText, queryByText } = render(
      <SessionFailures {...base} data={{ items: [], total: 0 }} isLoading={false} isError />,
    )
    expect(getByText('加载运行详情失败')).toBeInTheDocument()
    expect(queryByText('无失败记录')).not.toBeInTheDocument()
  })

  it('取数成功但确实没有失败：仍显示「无失败记录」', () => {
    const { getByText, queryByText } = render(
      <SessionFailures {...base} data={{ items: [], total: 0 }} isLoading={false} isError={false} />,
    )
    expect(getByText('无失败记录')).toBeInTheDocument()
    expect(queryByText('加载运行详情失败')).not.toBeInTheDocument()
  })

  it('行内勾选框带 Bot 标识的可访问名（读屏能分辨勾的是哪个 Bot）', () => {
    const { getByRole } = render(
      <SessionFailures {...base} data={{ items: [failure], total: 1 }} isLoading={false} />,
    )
    expect(getByRole('checkbox', { name: 'bot-uuid-1' })).toBeInTheDocument()
  })
})
