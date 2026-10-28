import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

// 被测页面依赖一组数据 hook：这里整体替换为可控实现，
// 让用例只断言页面自己的行为（空态 / 结果 / 回退确认文案），不打后端。
const searchState = {
  data: [] as unknown[],
  isLoading: false,
  isError: false,
}
const messagesState = {
  data: [] as unknown[],
  isLoading: false,
  isError: false,
}
/** 历史工具回显（FR-519）：按 messageId 分组，键为字符串。 */
const toolCallsState = {
  data: {} as Record<string, unknown[]>,
  isLoading: false,
  isError: false,
}
const editMutate = vi.fn().mockResolvedValue({})
const rollbackMutate = vi.fn().mockResolvedValue({ superseded: 2 })

vi.mock('@/api/assistant-chat', () => ({
  useAssistantConversationSearch: () => searchState,
  useAssistantMessages: () => messagesState,
  useAssistantToolCalls: () => toolCallsState,
  useEditAssistantMessage: () => ({ mutateAsync: editMutate }),
  useRollbackAssistantConversation: () => ({ mutateAsync: rollbackMutate }),
}))

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import AssistantHistoryPage from '@/pages/AssistantHistoryPage'

const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: { 'zh-CN': { translation: {} } },
  interpolation: { escapeValue: false },
})

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return (
    <QueryClientProvider client={qc}>
      <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
    </QueryClientProvider>
  )
}

function reset() {
  searchState.data = []
  searchState.isLoading = false
  searchState.isError = false
  messagesState.data = []
  messagesState.isLoading = false
  messagesState.isError = false
  toolCallsState.data = {}
  editMutate.mockClear()
  rollbackMutate.mockClear()
}

/**
 * 对话历史页（FR-542 / FR-543）。
 *
 * 三条断言对应三类不可退让的行为：
 *  1) 空关键词**不检索**（否则首屏会把全部历史拖进浏览器）；
 *  2) 检索失败要显式报错，不能显示成「没有结果」——两者处置完全不同；
 *  3) 回退确认必须写明「不撤销已发生的操作」，这是用户选定方案 A 的边界。
 */
describe('AssistantHistoryPage', () => {
  it('未输入关键词时提示先输入，且不展示结果列表', () => {
    reset()
    render(<AssistantHistoryPage />, { wrapper })
    expect(screen.getByText('输入关键词开始检索')).toBeInTheDocument()
    expect(screen.queryByText('没有匹配的对话')).not.toBeInTheDocument()
  })

  it('检索失败时显示错误而不是「没有结果」', async () => {
    reset()
    searchState.isError = true
    render(<AssistantHistoryPage />, { wrapper })
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('关键词'), '实例')
    // 防抖 300ms 后提交关键词，页面进入「已检索」分支。
    await waitFor(() => expect(screen.getByText('检索失败，请稍后重试。')).toBeInTheDocument(), {
      timeout: 2000,
    })
  })

  it('有结果时列出会话并可打开消息', async () => {
    reset()
    searchState.data = [{ id: 1, title: '排查实例崩溃', updatedAt: '2026-10-24' }]
    messagesState.data = [
      { id: 11, role: 'user', content: '实例起不来', createdAt: '2026-10-24' },
      { id: 12, role: 'assistant', content: '先看 statusReason', createdAt: '2026-10-24' },
    ]
    render(<AssistantHistoryPage />, { wrapper })
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('关键词'), '崩溃')
    await waitFor(() => expect(screen.getByText('排查实例崩溃')).toBeInTheDocument(), {
      timeout: 2000,
    })

    await user.click(screen.getByRole('button', { name: '打开会话 排查实例崩溃' }))
    expect(await screen.findByText('实例起不来')).toBeInTheDocument()
    expect(screen.getByText('先看 statusReason')).toBeInTheDocument()
  })

  it('历史消息回显当时的工具调用与状态（FR-519）', async () => {
    // 关键行为：工具状态此前只落在事件流里、没有读取面，回看历史是「纯文本」的。
    // 缺了这条回显，用户无法判断助手当时到底调没调工具、卡在哪一步、失败的是什么码。
    reset()
    searchState.data = [{ id: 1, title: '会话', updatedAt: '2026-10-24' }]
    messagesState.data = [
      { id: 21, role: 'user', content: '查一下节点', createdAt: '2026-10-24' },
      { id: 22, role: 'assistant', content: '已经查过了', createdAt: '2026-10-24' },
    ]
    // 后端按 messageId（字符串键）分组；这里挂在助手回复 22 上。
    toolCallsState.data = {
      '22': [
        {
          callId: 'call_a',
          name: 'system_agent_node_get',
          status: 'ok',
          seq: 1,
        },
        {
          callId: 'call_b',
          name: 'system_agent_instance_start',
          status: 'failed',
          errorCode: 'upstream_timeout',
          seq: 2,
        },
      ],
    }
    render(<AssistantHistoryPage />, { wrapper })
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('关键词'), '会话')
    await waitFor(() => expect(screen.getByText('会话')).toBeInTheDocument(), { timeout: 2000 })
    await user.click(screen.getByRole('button', { name: '打开会话 会话' }))

    // 工具块本身出现，两条调用各自的名字与状态都在。
    expect(await screen.findByText('工具调用')).toBeInTheDocument()
    expect(screen.getByText('system_agent_node_get')).toBeInTheDocument()
    expect(screen.getByText('system_agent_instance_start')).toBeInTheDocument()
    expect(screen.getByText('已完成')).toBeInTheDocument()
    expect(screen.getByText('失败')).toBeInTheDocument()
    // 失败必须带错误码：只说「失败」等于让用户去猜是权限、参数还是上游超时。
    // （渲染形态是「错误码：xxx」，断言用正则匹配，避免把模板文案固定死在这里。）
    expect(screen.getByText(/upstream_timeout/, { selector: 'p' })).toBeInTheDocument()
  })

  it('没有工具调用时不渲染工具块（不留空壳）', async () => {
    // 留空壳会让每条普通消息下面都挂一个「工具调用」标题，反而制造噪音。
    reset()
    searchState.data = [{ id: 1, title: '会话', updatedAt: '2026-10-24' }]
    messagesState.data = [{ id: 31, role: 'user', content: '纯聊天', createdAt: '2026-10-24' }]
    toolCallsState.data = {}
    render(<AssistantHistoryPage />, { wrapper })
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('关键词'), '会话')
    await waitFor(() => expect(screen.getByText('会话')).toBeInTheDocument(), { timeout: 2000 })
    await user.click(screen.getByRole('button', { name: '打开会话 会话' }))
    expect(await screen.findByText('纯聊天')).toBeInTheDocument()
    expect(screen.queryByText('工具调用')).not.toBeInTheDocument()
  })

  it('回退确认弹窗必须说明「不撤销已发生的操作」', async () => {    reset()
    searchState.data = [{ id: 1, title: '会话', updatedAt: '2026-10-24' }]
    messagesState.data = [{ id: 9, role: 'user', content: '启动实例 1', createdAt: '2026-10-24' }]
    render(<AssistantHistoryPage />, { wrapper })
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('关键词'), '实例')
    await waitFor(() => expect(screen.getByText('会话')).toBeInTheDocument(), { timeout: 2000 })
    await user.click(screen.getByRole('button', { name: '打开会话 会话' }))

    // 可访问名必须落在真按钮上，否则点不到（本仓踩过这个坑）。
    await user.click(await screen.findByRole('button', { name: '回退到这条消息' }))
    const desc = await screen.findByText(/回退不会撤销已经发生的操作/)
    expect(desc).toBeInTheDocument()
  })

  it('编辑是消息内原地编辑：不弹对话框，改完直接保存', async () => {
    reset()
    searchState.data = [{ id: 1, title: '会话', updatedAt: '2026-10-24' }]
    messagesState.data = [{ id: 9, role: 'user', content: '原问题', createdAt: '2026-10-24' }]
    render(<AssistantHistoryPage />, { wrapper })
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('关键词'), '实例')
    await waitFor(() => expect(screen.getByText('会话')).toBeInTheDocument(), { timeout: 2000 })
    await user.click(screen.getByRole('button', { name: '打开会话 会话' }))

    await user.click(await screen.findByRole('button', { name: '编辑这条消息' }))
    // 原地出现输入框（可访问名落在真控件上，getByLabelText 才能命中）。
    const input = screen.getByLabelText('编辑消息内容')
    expect(input).toHaveValue('原问题')
    await user.clear(input)
    await user.type(input, '更正后的问题')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() =>
      expect(editMutate).toHaveBeenCalledWith({ messageId: 9, content: '更正后的问题' }),
    )
    // 保存后退出编辑态。
    await waitFor(() => expect(screen.queryByLabelText('编辑消息内容')).not.toBeInTheDocument())
    // 关键：不得出现对话框（原地编辑而非弹窗）。
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})
