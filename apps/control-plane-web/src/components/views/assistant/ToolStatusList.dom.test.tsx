import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { ToolStatusList } from '@/components/views/assistant/ToolStatusList'
import {
  TOOL_PREVIEW_MAX_CHARS,
  applyEventToToolCalls,
  toolCallPhase,
} from '@/components/views/assistant/assistant-tool-status'
import type { ToolCallView } from '@/components/views/assistant/assistant-tool-status'
import type { AssistantEventFrame } from '@/api/assistant-chat'

/** 
 * 工具状态徽标 · 受控视图测（§5，ADR-103）。
 *
 * 断言分两层，故意都用**同一份事件序列**驱动：
 *  1. 聚合层：三类事件（tool_call / tool_status / tool_result）按 callId 收敛成一行，
 *     且状态能推进、重放不倒退；
 *  2. 展示层：徽标语义、错误码、预览截断、空列表整块不渲染。
 *
 * 语言包自带（而不是引应用的 `@/i18n`）：本组件的文案键此刻可能还没进 zh.json，
 * 引用真实语言包会让断言落在「缺键兜底」这条路径上——那样一旦有人补的英文与中文默认值
 * 不一致，测试要跟着改，却测不出任何真实回归。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        assistant: {
          toolStatusTitle: '工具调用',
          toolStatusRunning: '执行中',
          toolStatusPendingConfirmation: '待确认',
          toolStatusOk: '已完成',
          toolStatusFailed: '失败',
          toolStatusTruncated: '结果已截断',
          toolStatusErrorCode: '错误码：{{code}}',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

function frame(type: string, data: unknown, seq = 1): AssistantEventFrame {
  return { seq, type, data, createdAt: '2026-10-23T00:00:00Z' }
}

function wrap(ui: ReactNode) {
  return <I18nextProvider i18n={testI18n}>{ui}</I18nextProvider>
}

/** 渲染并返回 typing-library 的句柄（需要 rerender 的用例用得到）。 */
function renderList(items: ToolCallView[]) {
  return render(wrap(<ToolStatusList items={items} />))
}

/** 取某个调用对应的那一行（行上带 data-call-id，比按文案定位稳）。 */
function rowOf(callId: string): HTMLElement {
  const row = document.querySelector<HTMLElement>(`li[data-call-id="${callId}"]`)
  if (!row) throw new Error(`找不到 callId=${callId} 的工具行`)
  return row
}

describe('applyEventToToolCalls（事件 → 聚合）', () => {
  it('多调用按 callId 各自成行，并保持首次出现顺序', () => {
    let list = applyEventToToolCalls(
      [],
      frame('tool_call', { callId: 'c1', name: 'system.agent.instance_start', argsJson: {} }),
    )
    list = applyEventToToolCalls(
      list,
      frame('tool_call', { callId: 'c2', name: 'system.agent.node_list', argsJson: {} }),
    )
    // 交错到达的状态与结果不得打乱行序，也不得让 c1 的两条事件合成两行。
    list = applyEventToToolCalls(list, frame('tool_status', { callId: 'c1', status: 'ok' }))
    list = applyEventToToolCalls(list, frame('tool_status', { callId: 'c2', status: 'running' }))
    list = applyEventToToolCalls(list, frame('tool_result', { callId: 'c1', preview: '已启动' }))

    expect(list.map((c) => c.callId)).toEqual(['c1', 'c2'])
    expect(list[0]!.name).toBe('system.agent.instance_start')
    expect(list[0]!.preview).toBe('已启动')
  })

  it('非工具事件返回同一引用（文本流不该连带重渲染）', () => {
    const list = applyEventToToolCalls([], frame('tool_call', { callId: 'c1', name: 'm' }))
    expect(applyEventToToolCalls(list, frame('text_delta', { text: '你' }))).toBe(list)
    expect(applyEventToToolCalls(list, frame('usage', { totalTokens: 12 }))).toBe(list)
  })

  it('重放同一 tool_call 不把已推进的状态打回 running', () => {
    let list = applyEventToToolCalls([], frame('tool_call', { callId: 'c1', name: 'm' }))
    list = applyEventToToolCalls(list, frame('tool_status', { callId: 'c1', status: 'ok' }))
    // 断线补读会重放 call 帧；若重置成 running，界面会像工具重跑了一次。
    list = applyEventToToolCalls(list, frame('tool_call', { callId: 'c1', name: 'm' }))
    expect(list[0]!.status).toBe('ok')
  })

  it('缺 callId 的帧被丢弃，不造出空行', () => {
    const list = applyEventToToolCalls([], frame('tool_status', { status: 'ok' }))
    expect(list).toEqual([])
  })

  it('未知状态词原样保留（前向兼容），相位的语义只认已知值', () => {
    let list = applyEventToToolCalls([], frame('tool_call', { callId: 'c1', name: 'm' }))
    list = applyEventToToolCalls(list, frame('tool_status', { callId: 'c1', status: 'streaming_partial' }))
    expect(list[0]!.status).toBe('streaming_partial')
    expect(toolCallPhase('streaming_partial')).toBeNull()
  })
})

describe('ToolStatusList（工具状态徽标）', () => {
  it('多条调用聚合成多行，工具名与状态各自呈现', () => {
    let list = applyEventToToolCalls(
      [],
      frame('tool_call', { callId: 'c1', name: 'system.agent.instance_start' }),
    )
    list = applyEventToToolCalls(list, frame('tool_call', { callId: 'c2', name: 'system.agent.node_list' }))
    list = applyEventToToolCalls(list, frame('tool_status', { callId: 'c1', status: 'running' }))
    list = applyEventToToolCalls(list, frame('tool_status', { callId: 'c2', status: 'ok' }))
    renderList(list)

    const region = screen.getByLabelText('工具调用')
    expect(within(region).getAllByRole('listitem')).toHaveLength(2)
    expect(rowOf('c1').dataset.status).toBe('running')
    expect(within(rowOf('c1')).getByText('system.agent.instance_start')).toBeInTheDocument()
    expect(within(rowOf('c1')).getByText('执行中')).toBeInTheDocument()
    expect(within(rowOf('c2')).getByText('已完成')).toBeInTheDocument()
  })

  it('状态推进 running → ok 后徽标随之改变', () => {
    let list = applyEventToToolCalls([], frame('tool_call', { callId: 'c1', name: 'system.agent.instance_start' }))
    list = applyEventToToolCalls(list, frame('tool_status', { callId: 'c1', status: 'running' }))
    const { rerender } = renderList(list)
    expect(within(rowOf('c1')).getByText('执行中')).toBeInTheDocument()

    list = applyEventToToolCalls(list, frame('tool_status', { callId: 'c1', status: 'ok' }))
    rerender(wrap(<ToolStatusList items={list} />))

    expect(rowOf('c1').dataset.status).toBe('ok')
    expect(within(rowOf('c1')).getByText('已完成')).toBeInTheDocument()
    expect(within(rowOf('c1')).queryByText('执行中')).toBeNull()
    // chip 变体跟着状态走：成功用 chip-ok（失败才是 chip-bad）。
    expect(within(rowOf('c1')).getByText('已完成')).toHaveAttribute('data-variant', 'chip-ok')
  })

  it('待确认状态单独呈现，且比执行中更醒目（chip-neutral）', () => {
    let list = applyEventToToolCalls([], frame('tool_call', { callId: 'c1', name: 'system.agent.instance_stop' }))
    list = applyEventToToolCalls(list, frame('tool_status', { callId: 'c1', status: 'pending_confirmation' }))
    renderList(list)

    const badge = within(rowOf('c1')).getByText('待确认')
    expect(badge).toHaveAttribute('data-variant', 'chip-neutral')
  })

  it('失败态显示错误码（只说「失败」等于让用户去猜原因）', () => {
    let list = applyEventToToolCalls([], frame('tool_call', { callId: 'c1', name: 'system.agent.instance_start' }))
    list = applyEventToToolCalls(
      list,
      frame('tool_status', { callId: 'c1', status: 'failed', errorCode: 'upstream_timeout' }),
    )
    list = applyEventToToolCalls(list, frame('tool_result', { callId: 'c1', preview: '工具执行失败：超时' }))
    renderList(list)

    expect(within(rowOf('c1')).getByText('失败')).toHaveAttribute('data-variant', 'chip-bad')
    expect(within(rowOf('c1')).getByText('错误码：upstream_timeout')).toBeInTheDocument()
    // 失败时的结果预览同样要能看到（后端把失败说明也放在 preview 里）。
    expect(within(rowOf('c1')).getByText('工具执行失败：超时')).toBeInTheDocument()
  })

  it('过长的结果预览被截断，完整内容留在 title 上', () => {
    const long = 'x'.repeat(TOOL_PREVIEW_MAX_CHARS + 60)
    let list = applyEventToToolCalls([], frame('tool_call', { callId: 'c1', name: 'm' }))
    list = applyEventToToolCalls(list, frame('tool_result', { callId: 'c1', preview: long, truncated: false }))
    renderList(list)

    const shown = within(rowOf('c1')).getByTitle(long)
    expect(shown.textContent).toHaveLength(TOOL_PREVIEW_MAX_CHARS + 1) // 截断后追加一个省略号
    expect(shown.textContent?.endsWith('…')).toBe(true)
    expect(within(rowOf('c1')).getByText('结果已截断')).toBeInTheDocument()
  })

  it('短预览原样显示，不出现「已截断」提示', () => {
    let list = applyEventToToolCalls([], frame('tool_call', { callId: 'c1', name: 'm' }))
    list = applyEventToToolCalls(list, frame('tool_result', { callId: 'c1', preview: '运行中 3 台实例' }))
    renderList(list)

    expect(within(rowOf('c1')).getByText('运行中 3 台实例')).toBeInTheDocument()
    expect(within(rowOf('c1')).queryByText('结果已截断')).toBeNull()
  })

  it('后端声明结果被截断时给出提示（即使预览本身不长）', () => {
    let list = applyEventToToolCalls([], frame('tool_call', { callId: 'c1', name: 'm' }))
    list = applyEventToToolCalls(list, frame('tool_result', { callId: 'c1', preview: '前 64 KiB', truncated: true }))
    renderList(list)

    expect(within(rowOf('c1')).getByText('结果已截断')).toBeInTheDocument()
  })

  it('未知状态词按原词显示，不假装成已完成', () => {
    let list = applyEventToToolCalls([], frame('tool_call', { callId: 'c1', name: 'm' }))
    list = applyEventToToolCalls(list, frame('tool_status', { callId: 'c1', status: 'streaming_partial' }))
    renderList(list)

    expect(within(rowOf('c1')).getByText('streaming_partial')).toBeInTheDocument()
    expect(within(rowOf('c1')).queryByText('已完成')).toBeNull()
  })

  it('空列表与未传列表都不渲染（不留空壳）', () => {
    const { container, rerender } = renderList([])
    expect(container).toBeEmptyDOMElement()

    rerender(wrap(<ToolStatusList />))
    expect(container).toBeEmptyDOMElement()
  })
})

/** 
 * `unverified` 状态的展示（§7）。
 *
 * 后端把「写操作已发出但回执未经核实」记为 `unverified`（`AssistantReceiptUnverified`）。
 * 这个状态**必须与「已完成」区分开**：若显示成成功，用户会以为操作确实生效了，
 * 而实际上它可能根本没落地——这是会造成真实误操作的差别。
 *
 * 也不能直接把后端原文丢到界面上（用户会看到英文 "unverified"）。
 */
describe('工具状态 · unverified 不显示为成功', () => {
  it('显示「结果待核实」而不是英文原词或成功态', () => {
    const { container } = render(
      <I18nextProvider i18n={testI18n}>
        <ToolStatusList
          items={[
            {
              callId: 'c-unv',
              name: 'bot_send_command',
              status: 'unverified',
            } as unknown as ToolCallView,
          ]}
        />
      </I18nextProvider>,
    )
    const text = container.textContent ?? ''
    expect(text, '应显示中文「结果待核实」').toContain('结果待核实')
    expect(text, '不应出现后端原始状态词').not.toContain('unverified')
    expect(text, '不能显示为「已完成」').not.toContain('已完成')
  })
})
