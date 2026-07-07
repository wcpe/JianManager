import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useBots, useBotBatch, useBotSummary } from './bots'

/** FR-038：前端 hook/DOM 绑定规模化 Bot API（分页列表、聚合摘要、批量操作）。 */
function BotScaleHarness() {
  const [lastResult, setLastResult] = useState('未执行')
  const summary = useBotSummary({ groupBy: 'instance' })
  const list = useBots({ instanceId: 1, page: 1, pageSize: 2 })
  const batch = useBotBatch()

  const groups = summary.data?.groups ?? []
  const bots = list.data?.items ?? []

  return (
    <div>
      <p>总数：{summary.data?.total ?? 0}</p>
      <ul aria-label="实例聚合">
        {groups.map((group) => (
          <li key={group.key}>{group.label}:{group.total}/{group.online}</li>
        ))}
      </ul>
      <ul aria-label="分页列表">
        {bots.map((bot) => (
          <li key={bot.id}>{bot.name}:{bot.behavior}</li>
        ))}
      </ul>
      <button
        type="button"
        onClick={() => {
          batch.mutate(
            { action: 'set-behavior', filter: { instanceId: 1 }, behavior: 'follow' },
            { onSuccess: (res) => setLastResult(`成功 ${res.succeeded} 失败 ${res.failed}`) },
          )
        }}
      >
        批量设行为
      </button>
      <p>{lastResult}</p>
    </div>
  )
}

describe('FR-038 Bot 规模化 API 前端绑定', () => {
  it('读取聚合摘要和分页列表，并通过批量接口更新当前筛选范围', async () => {
    loginMockUser()
    renderWithProviders(<BotScaleHarness />)

    expect(await screen.findByText('总数：3')).toBeInTheDocument()
    expect(await screen.findByText('生存服:2/1')).toBeInTheDocument()
    expect(await screen.findByText('空岛服:1/0')).toBeInTheDocument()
    expect(await screen.findByText('GuardBot:guard')).toBeInTheDocument()
    expect(await screen.findByText('FollowBot:follow')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: '批量设行为' }))

    expect(await screen.findByText('成功 2 失败 0')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getByText('GuardBot:follow')).toBeInTheDocument()
    })
  })
})
