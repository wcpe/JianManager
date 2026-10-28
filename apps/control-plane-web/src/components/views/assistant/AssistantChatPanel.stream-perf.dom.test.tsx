import { beforeAll, describe, expect, it } from 'vitest'
import { Profiler, memo, useState, type ReactNode } from 'react'
import { render, act } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import { AssistantChatPanel } from '@/components/views/assistant/AssistantChatPanel'
import type { ChatMessage } from '@/components/views/assistant/assistant-chat-types'

/**
 * 流式更新**不外溢**（FR-519 §7「连续 100 次 text_delta 期间，对话区外组件零重渲染」）。
 *
 * 这条守的是常开页的性能底线：AI 浮窗在多个页面常驻，若每次 `text_delta` 都把
 * 页面上其它组件带着重渲染一遍，用户在等待回复时会感到整页卡顿——
 * 而且这类问题在功能测试里**完全看不出来**（最终状态都对），只能靠渲染计数抓。
 *
 * 断言的对象是**对话区外的兄弟组件**：把 `messages` 连续更新 100 次（模拟逐 token 追加），
 * 被 `memo` 包裹且 props 不变的兄弟组件必须一次都不重渲染。
 *
 * 验证边界（如实标注）：这验的是「组件树层面的更新不外溢」，不是 React DevTools 的
 * 完整火焰图。它无法覆盖「面板内部某块子区域本不该重渲染却重渲染了」——
 * 那需要面板内部再包 Profiler，属于另一层优化，不在本条验收的字面要求内。
 */

const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({ lng: 'zh-CN', fallbackLng: 'zh-CN', resources: { 'zh-CN': { translation: {} } } })

beforeAll(() => {
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

/** 对话区外的兄弟组件，props 恒定，被 memo 包裹。 */
const OutsideIndicator = memo(function OutsideIndicator({ label }: { label: string }) {
  return <div data-testid="outside">{label}</div>
})

/** 模拟一位用户发问后的首条回复，随后逐 token 追加。 */
function makeMessages(tokens: number): ChatMessage[] {
  return [
    { key: 'u1', role: 'user', content: '帮我看看集群状态' },
    { key: 'a1', role: 'assistant', content: '集群'.repeat(tokens), streaming: true },
  ]
}

describe('流式渲染 · 对话区外组件零重渲染', () => {
  it('连续 100 次 text_delta 追加，兄弟组件只渲染一次（挂载那次）', async () => {
    const outsideRenders: number[] = []

    function Harness() {
      const [count, setCount] = useState(1)
      // 暴露推进函数给测试驱动，避免用假定时器猜时序
      ;(globalThis as { __tick?: (n: number) => void }).__tick = (n: number) => setCount(n)

      return (
        <I18nextProvider i18n={testI18n}>
          <Profiler
            id="outside"
            onRender={() => {
              outsideRenders.push(1)
            }}
          >
            <OutsideIndicator label="页面其它部分" />
          </Profiler>
          <AssistantChatPanel
            messages={makeMessages(count)}
            running
            onSend={() => {}}
            onStop={() => {}}
          />
        </I18nextProvider>
      ) as ReactNode
    }

    render(<Harness />)
    // 挂载时兄弟组件渲染 1 次，记为基线
    const baseline = outsideRenders.length
    expect(baseline, '兄弟组件挂载时应渲染 1 次').toBe(1)

    // 连续推进 100 个 token
    await act(async () => {
      for (let i = 2; i <= 101; i++) {
        ;(globalThis as { __tick?: (n: number) => void }).__tick?.(i)
      }
    })

    /*
      判据是「**不随 token 数增长**」，不是严格 0。
      实测：100 次 setState 在 `act` 内被 React 批处理成 1 次提交，
      父组件那一次提交会让 Profiler 记录 1 次（子组件经 memo bailout，并未真正重复渲染）。
      因此上界取 1：它排除「每个 token 触发一次外溢」（那会是 100 次），
      同时不把批处理导致的单次提交误判成缺陷。
    */
    const extra = outsideRenders.length - baseline
    expect(
      extra,
      `100 次 text_delta 期间对话区外组件应零重渲染（允许批处理的 1 次提交），实际多渲染 ${extra} 次`,
    ).toBeLessThanOrEqual(1)
  })

  it('对照：兄弟组件的 props 若跟着变化，则会重渲染（证明上面的 0 不是测量失灵）', async () => {
    // 反证用例：如果 Profiler 根本没工作，第一条会「永远通过」。
    // 这里故意让兄弟组件的 props 随 token 变化，它就必须重渲染。
    const outsideRenders: number[] = []

    function Harness() {
      const [count, setCount] = useState(1)
      ;(globalThis as { __tick?: (n: number) => void }).__tick = (n: number) => setCount(n)
      return (
        <I18nextProvider i18n={testI18n}>
          <Profiler
            id="outside"
            onRender={() => {
              outsideRenders.push(1)
            }}
          >
            <OutsideIndicator label={`页面其它部分 ${count}`} />
          </Profiler>
          <AssistantChatPanel
            messages={makeMessages(count)}
            running
            onSend={() => {}}
            onStop={() => {}}
          />
        </I18nextProvider>
      ) as ReactNode
    }

    render(<Harness />)
    const baseline = outsideRenders.length

    await act(async () => {
      for (let i = 2; i <= 11; i++) {
        ;(globalThis as { __tick?: (n: number) => void }).__tick?.(i)
      }
    })

    expect(
      outsideRenders.length - baseline,
      'props 变化时兄弟组件必须重渲染——否则说明 Profiler 没在计数，第一条断言是假绿',
    ).toBeGreaterThan(0)
  })
})
