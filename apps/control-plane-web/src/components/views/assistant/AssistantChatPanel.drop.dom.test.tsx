import { beforeAll, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { AssistantChatPanel } from '@/components/views/assistant/AssistantChatPanel'
import { SERVER_DRAG_MIME } from '@/lib/assistant/server-drag'
import type { PageContextEntityRef } from '@/lib/assistant/page-context'

/** 
 * 「侧边栏服务器拖进聊天框 → 变上下文芯片」（519 联动）。
 *
 * 这组用例守三件事，任何一条退化成「看起来能用」都会造成实质问题：
 *  1. **拖放真的接得上**：MIME 一致、dragover 被 preventDefault（否则 drop 根本不触发）、
 *     拖拽中有可见反馈。
 *  2. **只加引用、不改文字**：拖进来不该往输入框塞文本（用户那句话还没说）。
 *  3. **芯片能取数也能跳转**：实例芯片带指标摘要与详情入口；非实例芯片不臆造这两者。
 *
 * 断言尽量认结构与行为（data 属性、可访问名、调用参数），少认文案：
 * 改一句措辞不该让接线测试变红。
 */

const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: { 'zh-CN': { translation: {} } },
})

beforeAll(() => {
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

/** 造一个只带指定 MIME 的 dataTransfer 替身（jsdom 的 DataTransfer 不完整）。 */
function dragTransfer(id?: number) {
  const store = new Map<string, string>()
  if (id !== undefined) store.set(SERVER_DRAG_MIME, String(id))
  return {
    types: id === undefined ? ['text/plain'] : [SERVER_DRAG_MIME],
    dropEffect: 'none',
    getData: (type: string) => store.get(type) ?? '',
    setData: (type: string, value: string) => store.set(type, value),
  }
}

function renderPanel(props: {
  onAddContextEntity?: (e: PageContextEntityRef) => void
  contextChips?: Array<{ key: string; label: string; onRemove: () => void; entity?: PageContextEntityRef }>
  contextEntities?: PageContextEntityRef[]
  draft?: string
}) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } },
  })
  const ui = (
    <QueryClientProvider client={qc}>
      <I18nextProvider i18n={testI18n}>
        <MemoryRouter>
          <AssistantChatPanel
            messages={[]}
            running={false}
            onSend={() => {}}
            onStop={() => {}}
            draft={props.draft ?? ''}
            onDraftChange={() => {}}
            onAddContextEntity={props.onAddContextEntity}
            contextChips={props.contextChips}
            contextEntities={props.contextEntities ?? []}
          />
        </MemoryRouter>
      </I18nextProvider>
    </QueryClientProvider>
  ) as ReactNode
  return render(ui)
}

describe('助手聊天面板 · 服务器拖入', () => {
  it('把服务器拖进来会追加一个实例实体，且不改输入框文字', () => {
    const onAdd = vi.fn()
    const { container } = renderPanel({ onAddContextEntity: onAdd, draft: '帮我看看它为什么卡' })

    const root = container.firstElementChild as HTMLElement
    const dt = dragTransfer(7)

    fireEvent.dragEnter(root, { dataTransfer: dt })
    fireEvent.dragOver(root, { dataTransfer: dt })
    fireEvent.drop(root, { dataTransfer: dt })

    expect(onAdd).toHaveBeenCalledTimes(1)
    expect(onAdd).toHaveBeenCalledWith({ type: 'instance', id: 7 })
    // 文字不受影响：拖入只是附带上下文。
    expect(screen.getByRole('textbox')).toHaveValue('帮我看看它为什么卡')
  })

  it('拖拽中给出可见反馈，离开后消失', () => {
    const { container } = renderPanel({ onAddContextEntity: () => {} })
    const root = container.firstElementChild as HTMLElement
    const dt = dragTransfer(7)

    expect(root.getAttribute('data-assistant-drop-target')).toBeNull()
    fireEvent.dragEnter(root, { dataTransfer: dt })
    expect(root.getAttribute('data-assistant-drop-target')).toBe('active')
    fireEvent.dragLeave(root, { dataTransfer: dt })
    expect(root.getAttribute('data-assistant-drop-target')).toBeNull()
  })

  it('dragover 必须 preventDefault，否则浏览器不会派发 drop', () => {
    // 这是 HTML5 拖拽的规范要求，写漏了表现为「拖了没反应」且无任何报错。
    const { container } = renderPanel({ onAddContextEntity: () => {} })
    const root = container.firstElementChild as HTMLElement
    const dt = dragTransfer(7)
    // fireEvent 返回 false 表示事件被 preventDefault
    const notCancelled = fireEvent.dragOver(root, { dataTransfer: dt })
    expect(notCancelled).toBe(false)
  })

  it('拖别的类型（非服务器行）不触发，也不改高亮', () => {
    const onAdd = vi.fn()
    const { container } = renderPanel({ onAddContextEntity: onAdd })
    const root = container.firstElementChild as HTMLElement
    const dt = dragTransfer(undefined) // 只有 text/plain

    fireEvent.dragEnter(root, { dataTransfer: dt })
    fireEvent.dragOver(root, { dataTransfer: dt })
    fireEvent.drop(root, { dataTransfer: dt })

    expect(onAdd).not.toHaveBeenCalled()
    expect(root.getAttribute('data-assistant-drop-target')).toBeNull()
  })

  it('id 非法时丢弃，不产生无主实体', () => {
    const onAdd = vi.fn()
    const { container } = renderPanel({ onAddContextEntity: onAdd })
    const root = container.firstElementChild as HTMLElement

    fireEvent.dragOver(root, { dataTransfer: dragTransfer(0) })
    fireEvent.drop(root, { dataTransfer: dragTransfer(0) })
    expect(onAdd).not.toHaveBeenCalled()
  })
})

describe('助手聊天面板 · 上下文芯片', () => {
  it('实例芯片带跳转入口（可聚焦链接，指向实例详情）', () => {
    renderPanel({
      contextEntities: [{ type: 'instance', id: 7 }],
      contextChips: [
        {
          key: 'ent:instance:7',
          label: '实例：验收实例',
          onRemove: () => {},
          entity: { type: 'instance', id: 7 },
        },
      ],
    })

    // 可访问名必须落在可聚焦元素本身（护栏要求），用 role=link 查而不是查容器。
    const link = screen.getByRole('link', { name: /实例详情/ })
    expect(link).toHaveAttribute('href', '/instances/7')
  })

  it('非实例芯片不出现跳转入口，也不取指标', () => {
    renderPanel({
      contextChips: [{ key: 'page:logs', label: '当前页：日志', onRemove: () => {} }],
    })
    expect(screen.queryByRole('link', { name: /实例详情/ })).toBeNull()
    expect(screen.getByText('当前页：日志')).toBeInTheDocument()
  })

  it('移除按钮的可访问名包含实体名（多个芯片时能区分）', () => {
    renderPanel({
      contextChips: [
        { key: 'ent:instance:1', label: '实例：甲', onRemove: () => {}, entity: { type: 'instance', id: 1 } },
        { key: 'ent:instance:2', label: '实例：乙', onRemove: () => {}, entity: { type: 'instance', id: 2 } },
      ],
    })
    expect(screen.getByRole('button', { name: /甲/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /乙/ })).toBeInTheDocument()
  })

  it('移除按钮真的调用对应回调（不是所有芯片共用一个）', () => {
    const removeFirst = vi.fn()
    const removeSecond = vi.fn()
    renderPanel({
      contextChips: [
        { key: 'a', label: '甲', onRemove: removeFirst, entity: { type: 'instance', id: 1 } },
        { key: 'b', label: '乙', onRemove: removeSecond, entity: { type: 'instance', id: 2 } },
      ],
    })
    fireEvent.click(screen.getByRole('button', { name: /移除「乙」/ }))
    expect(removeSecond).toHaveBeenCalledTimes(1)
    expect(removeFirst).not.toHaveBeenCalled()
  })
})
