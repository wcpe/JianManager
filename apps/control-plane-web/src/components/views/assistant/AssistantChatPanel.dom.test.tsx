import { beforeAll, describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { AssistantChatPanel } from '@/components/views/assistant/AssistantChatPanel'
import type { ToolCallView } from '@/components/views/assistant/assistant-tool-status'

/**
 * 聊天面板的**工具状态接入点**（FR-519 §5）。
 *
 * 这里只守一件事：面板确实把容器给的 `toolCalls` 渲染在消息流**内部末尾**
 * （= 当前 run 之后），而不是漏渲染或塞到别处。聚合与徽标细节由
 * `ToolStatusList.dom.test.tsx` 覆盖，此处不重复。
 *
 * 断言只认结构（`data-slot` / `data-call-id`），不认文案：文案键由语言包统一维护，
 * 改一句措辞不该让这个「接线测试」变红。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({ lng: 'zh-CN', fallbackLng: 'zh-CN', resources: { 'zh-CN': { translation: {} } } })

beforeAll(() => {
  // jsdom 没有布局，面板里滚动跟随的赋值对 scrollTop 不敏感，无需额外垫片；
  // 这里只补 ResizeObserver（Radix Tooltip 等可能需要，缺失会抛出未捕获异常）。
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

function renderPanel(toolCalls?: ToolCallView[]) {
  const ui = (
    <I18nextProvider i18n={testI18n}>
      <AssistantChatPanel
        messages={[{ key: 'run-1', role: 'assistant', content: '我先查一下。' }]}
        running
        onSend={() => {}}
        onStop={() => {}}
        toolCalls={toolCalls}
      />
    </I18nextProvider>
  ) as ReactNode
  return render(ui)
}

describe('AssistantChatPanel · 工具状态接入', () => {
  it('本轮工具调用渲染在消息流内部（紧跟当前 run）', () => {
    const calls: ToolCallView[] = [
      { callId: 'c1', name: 'system.agent.instance_start', status: 'ok', preview: '已启动' },
    ]
    renderPanel(calls)

    const log = screen.getByRole('log')
    const list = log.querySelector('[data-slot="tool-status-list"]')
    expect(list).not.toBeNull()
    expect(list!.querySelectorAll('li[data-call-id="c1"]')).toHaveLength(1)
    // 消息本身也要还在：工具块是追加，不是替换。
    expect(screen.getByText('我先查一下。')).toBeInTheDocument()
  })

  it('没有工具调用时不多出一块空壳', () => {
    renderPanel()
    expect(screen.getByRole('log').querySelector('[data-slot="tool-status-list"]')).toBeNull()
  })
})

/**
 * 读屏播报的**语义属性**（FR-519 §7「消息完成时读屏播报整条，逐 token 不播报」）。
 *
 * 【验证边界，必须如实标注】屏幕阅读器的实际播报行为无法在无头浏览器里断言——
 * 那需要真机辅助技术（VoiceOver/NVDA）。这里锁的是**实现方式**：
 *  - 容器必须是 `role="log"` + `aria-live="polite"`：log 语义表示「追加型日志」，
 *    polite 表示「等用户空闲再播报」，两者合起来才是「整条播报、不打断」的正确语义；
 *  - **绝不能是 `assertive`**：那会逐条打断用户正在听的内容，正是验收要防的刷屏；
 *  - 也不能没有 `aria-live`：那样屏幕阅读器根本不会主动播报新消息。
 *
 * 这条用例的价值在于：把「有意的取舍」（播报粒度交给浏览器按内容稳定自行决定）
 * 固定在代码里，防止后来者换成 assertive 或删掉属性时无人发觉。
 */
describe('读屏播报 · 语义属性', () => {
  it('消息区是 role=log + aria-live=polite（不是 assertive）', () => {
    const { container } = renderPanel()
    const log = container.querySelector('[role="log"]')
    expect(log, '消息流应有 role="log"（追加型日志语义）').toBeTruthy()
    expect(log!.getAttribute('aria-live'), '应为 polite，等用户空闲再播报').toBe('polite')
    expect(log!.getAttribute('aria-live'), '不得为 assertive（会逐条打断）').not.toBe('assertive')
    expect(log!.getAttribute('aria-label'), '可访问名应存在').toBeTruthy()
  })
})
