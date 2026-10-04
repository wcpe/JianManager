import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import {
  FOCUSABLE_SELECTOR,
  attemptFocus,
  getFocusableElements,
  hasFocusableContent,
  isVisiblyFocusable,
} from './focusable'
import { useFocusTrap } from './useFocusTrap'

/**
 * 焦点陷阱测试（FR-496 阶段 6）。
 *
 * 焦点陷阱的 bug 都不会抛错，只会让键盘用户体验诡异——Tab 从末尾跑到遮罩背后的页面、
 * 关闭弹窗后焦点掉到 body、鼠标点出去反被拽回来。故这里逐条锁定这些**方向性契约**：
 * ① Tab / Shift+Tab 首尾环绕；
 * ② 焦点在容器边界上时先进入内容；
 * ③ 键盘带出去的焦点拉回、鼠标点出去的焦点不抢；
 * ④ ESC 只在调用点显式接管时才消费；
 * ⑤ 关闭时按需还原焦点，且只在"焦点已无处可去"时还原。
 */

/** 陷阱容器 + 一个容器外按钮（用于验证"跑出去"与"点出去"两种逃逸）。 */
function TrapHarness({
  isActive,
  onEscape,
  restoreFocus,
}: {
  isActive: boolean
  onEscape?: () => void
  restoreFocus?: boolean
}) {
  const { containerRef } = useFocusTrap<HTMLDivElement>({ isActive, onEscape, restoreFocus })
  return (
    <div>
      <button type="button" data-testid="outside">
        外部
      </button>
      {/* tabIndex=-1：模拟"焦点可以先落在面板边界上"的模态面板 */}
      <div ref={containerRef} data-testid="trap" tabIndex={-1}>
        <button type="button" data-testid="first">
          第一
        </button>
        <span>说明文字</span>
        <button type="button" data-testid="last">
          最后
        </button>
      </div>
    </div>
  )
}

describe('useFocusTrap 首尾环绕', () => {
  it('Tab 在最后一个元素上回到第一个', () => {
    render(<TrapHarness isActive />)

    screen.getByTestId('last').focus()
    fireEvent.keyDown(document, { key: 'Tab' })

    expect(document.activeElement).toBe(screen.getByTestId('first'))
  })

  it('Shift+Tab 在第一个元素上回到最后一个', () => {
    render(<TrapHarness isActive />)

    screen.getByTestId('first').focus()
    fireEvent.keyDown(document, { key: 'Tab', shiftKey: true })

    expect(document.activeElement).toBe(screen.getByTestId('last'))
  })

  it('中间元素上的 Tab 不被拦截（交给浏览器正常前进）', () => {
    render(<TrapHarness isActive />)

    screen.getByTestId('first').focus()
    const event = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    document.dispatchEvent(event)

    // 不 preventDefault 才说明浏览器能照常把焦点移到下一个元素
    expect(event.defaultPrevented).toBe(false)
    expect(document.activeElement).toBe(screen.getByTestId('first'))
  })

  it('焦点落在容器边界上时，Tab 先进入内容而不是离开', () => {
    render(<TrapHarness isActive />)

    screen.getByTestId('trap').focus()
    fireEvent.keyDown(document, { key: 'Tab' })

    expect(document.activeElement).toBe(screen.getByTestId('first'))

    screen.getByTestId('trap').focus()
    fireEvent.keyDown(document, { key: 'Tab', shiftKey: true })

    expect(document.activeElement).toBe(screen.getByTestId('last'))
  })
})

describe('useFocusTrap 逃逸处理', () => {
  it('键盘把焦点带出容器时拉回容器内', () => {
    render(<TrapHarness isActive />)

    // 先让 hook 知道"这次是键盘导航"，再让焦点落到容器外
    fireEvent.keyDown(document, { key: 'Tab' })
    screen.getByTestId('outside').focus()

    expect(document.activeElement).toBe(screen.getByTestId('first'))
  })

  it('鼠标造成的焦点外移不抢（否则点遮罩关闭会失灵）', () => {
    render(<TrapHarness isActive />)

    screen.getByTestId('outside').focus()

    expect(document.activeElement).toBe(screen.getByTestId('outside'))
  })

  it('isActive=false 时完全不拦截 Tab', () => {
    render(<TrapHarness isActive={false} />)

    screen.getByTestId('last').focus()
    const event = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    document.dispatchEvent(event)

    expect(event.defaultPrevented).toBe(false)
    expect(document.activeElement).toBe(screen.getByTestId('last'))
  })
})

describe('useFocusTrap ESC 归属', () => {
  it('传了 onEscape 时消费 ESC', () => {
    const onEscape = vi.fn()
    render(<TrapHarness isActive onEscape={onEscape} />)

    fireEvent.keyDown(document, { key: 'Escape' })

    expect(onEscape).toHaveBeenCalledTimes(1)
  })

  it('不传 onEscape 时按 ESC 无事发生，也不阻止事件冒泡给上层图层', () => {
    render(<TrapHarness isActive />)

    const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    document.dispatchEvent(event)

    // 嵌在 Radix 图层里时，ESC 必须继续传给图层自己裁决（一次 ESC 只关一层）
    expect(event.defaultPrevented).toBe(false)
  })

  it('isActive=false 时不消费 ESC', () => {
    const onEscape = vi.fn()
    render(<TrapHarness isActive={false} onEscape={onEscape} />)

    fireEvent.keyDown(document, { key: 'Escape' })

    expect(onEscape).not.toHaveBeenCalled()
  })
})

describe('useFocusTrap 焦点还原', () => {
  it('关闭后把焦点还给激活前的元素', () => {
    const { rerender } = render(<TrapHarness isActive={false} />)

    screen.getByTestId('outside').focus()
    rerender(<TrapHarness isActive />)
    screen.getByTestId('last').focus()

    rerender(<TrapHarness isActive={false} />)

    expect(document.activeElement).toBe(screen.getByTestId('outside'))
  })

  it('restoreFocus=false 时不还原（交由调用点自己决定焦点去哪）', () => {
    const { rerender } = render(<TrapHarness isActive={false} restoreFocus={false} />)

    screen.getByTestId('outside').focus()
    rerender(<TrapHarness isActive restoreFocus={false} />)
    screen.getByTestId('last').focus()

    rerender(<TrapHarness isActive={false} restoreFocus={false} />)

    expect(document.activeElement).not.toBe(screen.getByTestId('outside'))
  })

  it('焦点已被移到别处时不抢夺', () => {
    const { rerender } = render(<TrapHarness isActive={false} />)

    screen.getByTestId('outside').focus()
    rerender(<TrapHarness isActive />)
    screen.getByTestId('last').focus()

    // 模拟调用点在关闭时已经自己把焦点交给了另一个控件
    screen.getByTestId('first').focus()
    screen.getByTestId('outside').focus()

    rerender(<TrapHarness isActive={false} />)

    expect(document.activeElement).toBe(screen.getByTestId('outside'))
  })
})

describe('focusable 判定', () => {
  it('选择器覆盖朴素选择器会漏掉的成员', () => {
    // 可编辑区/媒体/iframe/展开的 details 都是真 Tab 停靠点，漏掉它们会让 Tab 逃出陷阱
    expect(FOCUSABLE_SELECTOR).toContain('[contenteditable]:not([contenteditable="false"])')
    expect(FOCUSABLE_SELECTOR).toContain('audio[controls]')
    expect(FOCUSABLE_SELECTOR).toContain('video[controls]')
    expect(FOCUSABLE_SELECTOR).toContain('iframe')
    expect(FOCUSABLE_SELECTOR).toContain('details > summary:first-child')
    // tabIndex=-1 是编程聚焦点，不属于 Tab 序列
    expect(FOCUSABLE_SELECTOR).toContain('[tabindex]:not([tabindex="-1"])')
  })

  it('按文档顺序返回可见可聚焦元素，并跳过禁用项', () => {
    render(
      <div data-testid="root">
        <button type="button" data-testid="a">
          A
        </button>
        <button type="button" disabled data-testid="disabled">
          禁用
        </button>
        <input data-testid="b" />
      </div>,
    )
    const root = screen.getByTestId('root')

    expect(getFocusableElements(root).map((el) => el.dataset.testid)).toEqual(['a', 'b'])
  })

  it('跳过被 aria-hidden / hidden / display:none 遮住的元素', () => {
    render(
      <div data-testid="root">
        <div aria-hidden="true">
          <button type="button" data-testid="aria-hidden">
            AT 不可见却可 Tab
          </button>
        </div>
        <div hidden>
          <button type="button" data-testid="hidden">
            隐藏
          </button>
        </div>
        <div style={{ display: 'none' }}>
          <button type="button" data-testid="display-none">
            不显示
          </button>
        </div>
        <button type="button" data-testid="visible">
          可见
        </button>
      </div>,
    )
    const root = screen.getByTestId('root')

    expect(getFocusableElements(root).map((el) => el.dataset.testid)).toEqual(['visible'])
    // 单元素判定同理：aria-hidden 子树里的可聚焦内容必须被排除（WCAG 4.1.2）
    expect(isVisiblyFocusable(screen.getByTestId('aria-hidden'))).toBe(false)
    expect(isVisiblyFocusable(screen.getByTestId('display-none'))).toBe(false)
    expect(isVisiblyFocusable(screen.getByTestId('visible'))).toBe(true)
  })

  it('hasFocusableContent 反映容器内是否还有 Tab 停靠点', () => {
    render(
      <div>
        <div data-testid="empty">
          <span>纯文本</span>
        </div>
        <div data-testid="with-button">
          <button type="button">可点</button>
        </div>
      </div>,
    )

    expect(hasFocusableContent(screen.getByTestId('empty'))).toBe(false)
    expect(hasFocusableContent(screen.getByTestId('with-button'))).toBe(true)
  })

  it('attemptFocus 以 document.activeElement 为准报告成败', () => {
    render(
      <div>
        <button type="button" data-testid="ok">
          可聚焦
        </button>
        <button type="button" disabled data-testid="no">
          禁用
        </button>
      </div>,
    )

    expect(attemptFocus(screen.getByTestId('ok'))).toBe(true)
    // 禁用控件的 focus() 是静默失败的：调用方必须拿到 false 才能继续找下一个
    expect(attemptFocus(screen.getByTestId('no'))).toBe(false)
  })
})
