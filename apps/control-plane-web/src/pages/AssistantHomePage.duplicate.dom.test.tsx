import { describe, expect, it, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { usePermissionsStore } from '@/stores/permissions'
import AssistantHomePage from './AssistantHomePage'

/**
 * 一轮问答**只能出现一次**（真实供应商链路得到的缺陷回归）。
 *
 * 背景：消息列表是「服务端历史 + 本地流式」直接拼接。`run_end` 时会 refetch 历史，
 * 但流式项没被清掉，于是同一条回复出现两遍——本用例用真实供应商验收时肉眼看到
 * 「提问显示两次、回复显示两次」才发现的。
 *
 * 断言方式：让 mock 在提交后**真的有**这轮消息（模拟服务端已落库），
 * 然后数界面上该文案出现几次。修复前是 2 次。
 */
function asAdmin() {
  usePermissionsStore.setState({
    loaded: true,
    isPlatformAdmin: true,
    nodes: new Set(['instance.read']),
  })
  server.use(
    http.get(API('/auth/me'), () =>
      HttpResponse.json({
        userId: 1,
        username: 'tester',
        role: 10,
        roleKey: 'platform_admin',
        isPlatformAdmin: true,
        nodes: ['instance.read'],
      }),
    ),
  )
}

const QUESTION = '回归用问题：这条只应出现一次'
const REPLY = '回归用回复：这条也只应出现一次'

describe('AI 首页：一轮问答不重复显示', () => {
  beforeEach(() => {
    loginMockUser()
    asAdmin()
  })

  it('run 结束后历史已包含本轮时，界面只显示一条提问与一条回复', async () => {
    // 提交探针：没有它，用例可能在「根本没发出去」的情况下也绿。
    let submitted = false
    // 服务端已落库这轮问答（run_end 后 refetch 拿到的就是它）
    server.use(
      http.get(API('/assistant/conversations/1/messages'), () =>
        HttpResponse.json({
          items: [
            { id: 1, conversationId: 1, role: 'user', content: QUESTION, createdAt: new Date().toISOString() },
            { id: 2, conversationId: 1, role: 'assistant', content: REPLY, createdAt: new Date().toISOString() },
          ],
        }),
      ),
      http.get(API('/assistant/conversations'), () =>
        HttpResponse.json({
          items: [
            {
              id: 1,
              title: '回归会话',
              selectedModelId: null,
              createdAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
            },
          ],
          nextCursor: '',
        }),
      ),
    )

    renderWithProviders(<AssistantHomePage />)

    // 历史先到位（这是纯历史渲染，本来就该只有一条）
    await screen.findByText(QUESTION)
    expect(screen.getAllByText(QUESTION)).toHaveLength(1)
    expect(screen.getAllByText(REPLY)).toHaveLength(1)

    // 再提交一轮**内容相同**的问题：此时乐观插入 + 历史里那条会短暂并存，
    // run_end 清理后必须回到「各一条」。这条正是缺陷现场。
    server.use(
      http.post(API('/assistant/conversations/1/runs'), () => {
        submitted = true
        return HttpResponse.json({ conversationId: 1, runId: 9, status: 'queued' }, { status: 202 })
      }),
      // 立即给出终态事件：run_end 触发 refetch + 清流式
      http.get(API('/assistant/runs/9/stream'), () =>
        new HttpResponse(
          `data: ${JSON.stringify({ seq: 1, type: 'run_end', data: { status: 'completed' }, createdAt: new Date().toISOString() })}\n\n`,
          { headers: { 'Content-Type': 'text/event-stream' } },
        ),
      ),
    )

    // 用可访问名定位控件（面板给输入框与发送键都上了 aria-label）：
    // 之前的写法靠 DOM 结构猜按钮，实际点了个空，用例因此「永远绿」——
    // 回退修复后依然通过，是假测试。改用 aria-label 后能真正发出这一轮。
    const user = userEvent.setup()
    const box = screen.getByLabelText('输入消息')
    await user.type(box, QUESTION)
    await user.click(screen.getByLabelText('发送'))

    // 先确认这一轮真的发出去了（否则下面的「各一条」毫无意义）。
    await waitFor(() => expect(submitted).toBe(true), { timeout: 4000 })

    // 关键断言：稳定后仍然各一条（修复前这里会是 2）。
    await waitFor(
      () => {
        expect(screen.getAllByText(QUESTION)).toHaveLength(1)
        expect(screen.getAllByText(REPLY)).toHaveLength(1)
      },
      { timeout: 4000 },
    )
  })
})
