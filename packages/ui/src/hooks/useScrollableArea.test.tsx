import * as React from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { useScrollableArea, type ScrollAxis, type ScrollKeyboardAccess } from './useScrollableArea'

/**
 * 滚动区域键盘可达性测试（FR-496 阶段 6）。
 *
 * jsdom 没有布局，`scrollHeight/clientHeight` 恒为 0，因此用例显式注入尺寸并触发
 * 一次 resize（hook 的重测入口之一）——这不是绕过实现，而是把浏览器里"改窗口大小后
 * 重新测量"这条真实路径跑一遍。
 *
 * 锁定三类契约：
 * ① **停靠点归属**：只在真正溢出时才多占一个 Tab 停靠点；内容自带停靠点时容器让位；
 * ② **按键归属**：只有焦点在容器本身时才消费方向键/翻页键，内容里的输入框不受影响；
 * ③ **状态与钩子**：`data-scroll-*` 钩子、调用方 props/ref 不丢失。
 */

type Metrics = {
  scrollHeight?: number
  clientHeight?: number
  scrollWidth?: number
  clientWidth?: number
  scrollTop?: number
  scrollLeft?: number
}

/** 给元素注入伪布局尺寸（jsdom 无布局引擎）。 */
function applyMetrics(node: HTMLElement, metrics: Metrics) {
  for (const [key, value] of Object.entries(metrics)) {
    Object.defineProperty(node, key, { configurable: true, writable: true, value })
  }
}

function ScrollRegion({
  keyboardAccess,
  axis,
  label = '实例表格',
  onKeyDown,
  contentRef,
  children,
}: {
  keyboardAccess?: ScrollKeyboardAccess
  axis?: ScrollAxis
  label?: string
  onKeyDown?: React.KeyboardEventHandler<HTMLDivElement>
  contentRef?: React.Ref<HTMLDivElement>
  children?: React.ReactNode
}) {
  const { getViewportProps, getContentProps, state } = useScrollableArea({
    keyboardAccess,
    axis,
    label,
  })

  return (
    <div {...getViewportProps({ 'data-testid': 'viewport', onKeyDown })}>
      <div {...getContentProps({ 'data-testid': 'content', ref: contentRef })}>
        {children ?? <span>纯文本内容</span>}
      </div>
      <span data-testid="state-probe">{state.isScrollable ? 'scrollable' : 'fixed'}</span>
    </div>
  )
}

/** 渲染后注入尺寸并触发重测，返回视口元素与滚动调用桩。 */
function renderScrollable(
  dims: Metrics = { scrollHeight: 800, clientHeight: 300 },
  options: Parameters<typeof ScrollRegion>[0] = {},
) {
  const view = render(<ScrollRegion {...options} />)
  const viewport = screen.getByTestId('viewport')
  const scrollBy = vi.fn()
  const scrollTo = vi.fn()
  Object.defineProperty(viewport, 'scrollBy', { configurable: true, value: scrollBy })
  Object.defineProperty(viewport, 'scrollTo', { configurable: true, value: scrollTo })
  applyMetrics(viewport, { scrollTop: 0, scrollLeft: 0, ...dims })
  fireEvent(window, new Event('resize'))
  return { view, viewport, scrollBy, scrollTo }
}

describe('滚动区域停靠点归属', () => {
  it('内容溢出且不含可聚焦元素时，容器自己成为键盘停靠点', () => {
    renderScrollable(undefined, { label: '日志输出' })
    const viewport = screen.getByTestId('viewport')

    expect(viewport).toHaveAttribute('data-scrollable', 'true')
    expect(viewport).toHaveAttribute('tabindex', '0')
    expect(viewport).toHaveAttribute('role', 'region')
    expect(viewport).toHaveAttribute('aria-label', '日志输出')
  })

  it('未溢出时不占停靠点（不留空 Tab 位）', () => {
    renderScrollable({ scrollHeight: 200, clientHeight: 300 })
    const viewport = screen.getByTestId('viewport')

    expect(viewport).not.toHaveAttribute('data-scrollable')
    expect(viewport).not.toHaveAttribute('tabindex')
    expect(viewport).not.toHaveAttribute('role')
    expect(screen.getByTestId('state-probe')).toHaveTextContent('fixed')
  })

  it('auto：内容自带可聚焦元素时让出停靠点（内容自己能 Tab 到）', () => {
    renderScrollable(undefined, {
      children: (
        <button type="button" data-testid="inner">
          内部按钮
        </button>
      ),
    })
    const viewport = screen.getByTestId('viewport')

    expect(viewport).toHaveAttribute('data-scrollable', 'true')
    expect(viewport).not.toHaveAttribute('tabindex')
    expect(viewport).not.toHaveAttribute('role')
  })

  it('keyboardAccess="viewport"：即使内容可聚焦也保留容器停靠点', () => {
    renderScrollable(undefined, {
      keyboardAccess: 'viewport',
      children: (
        <button type="button" data-testid="inner">
          内部按钮
        </button>
      ),
    })

    expect(screen.getByTestId('viewport')).toHaveAttribute('tabindex', '0')
  })

  it('keyboardAccess="content"：容器永不成为停靠点', () => {
    renderScrollable(undefined, { keyboardAccess: 'content' })

    const viewport = screen.getByTestId('viewport')
    expect(viewport).toHaveAttribute('data-scrollable', 'true')
    expect(viewport).not.toHaveAttribute('tabindex')
  })

  it('未给可访问名时不设 role（避免产出无名地标）', () => {
    renderScrollable(undefined, { label: '' })

    const viewport = screen.getByTestId('viewport')
    expect(viewport).toHaveAttribute('tabindex', '0')
    expect(viewport).not.toHaveAttribute('role')
    expect(viewport).not.toHaveAttribute('aria-label')
  })
})

describe('滚动区域的键盘委托', () => {
  it('焦点在容器上时方向键滚动容器并阻止默认行为', () => {
    const { viewport, scrollBy } = renderScrollable()
    const event = new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true })
    viewport.dispatchEvent(event)

    expect(event.defaultPrevented).toBe(true)
    expect(scrollBy).toHaveBeenCalledWith({ top: 48 })
  })

  it('PageUp / PageDown 按视口高度一屏滚动', () => {
    const { viewport, scrollBy } = renderScrollable()

    fireEvent.keyDown(viewport, { key: 'PageDown' })
    expect(scrollBy).toHaveBeenLastCalledWith({ top: 270 })

    fireEvent.keyDown(viewport, { key: 'PageUp' })
    expect(scrollBy).toHaveBeenLastCalledWith({ top: -270 })
  })

  it('Home / End 滚到两端', () => {
    const { viewport, scrollTo } = renderScrollable()

    fireEvent.keyDown(viewport, { key: 'Home' })
    expect(scrollTo).toHaveBeenLastCalledWith({ top: 0 })

    fireEvent.keyDown(viewport, { key: 'End' })
    // 800 - 300 = 500
    expect(scrollTo).toHaveBeenLastCalledWith({ top: 500 })
  })

  it('内容里控件的按键不被劫持（打字时方向键属于光标）', () => {
    const { viewport, scrollBy } = renderScrollable(undefined, {
      children: <input data-testid="inner-input" />,
    })

    const input = screen.getByTestId('inner-input')
    const event = new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true })
    input.dispatchEvent(event)

    expect(scrollBy).not.toHaveBeenCalled()
    expect(event.defaultPrevented).toBe(false)
    // 事件确实冒泡到了容器（否则上面的"没被劫持"会因事件没送达而假绿）
    expect(viewport.contains(input)).toBe(true)
  })

  it('横向轴只消费左右键，不吃上下键', () => {
    const { viewport, scrollBy } = renderScrollable(
      { scrollWidth: 900, clientWidth: 300 },
      { axis: 'horizontal' },
    )

    fireEvent.keyDown(viewport, { key: 'ArrowRight' })
    expect(scrollBy).toHaveBeenLastCalledWith({ left: 48 })

    scrollBy.mockClear()
    const down = new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true })
    viewport.dispatchEvent(down)
    expect(scrollBy).not.toHaveBeenCalled()
    expect(down.defaultPrevented).toBe(false)
  })

  it('Tab：容器与内容都可达时先进入内容', () => {
    const { viewport } = renderScrollable(undefined, {
      keyboardAccess: 'viewport',
      children: (
        <>
          <button type="button" data-testid="inner-first">
            第一个
          </button>
          <button type="button" data-testid="inner-last">
            最后一个
          </button>
        </>
      ),
    })

    viewport.focus()
    expect(document.activeElement).toBe(viewport)

    fireEvent.keyDown(viewport, { key: 'Tab' })
    expect(document.activeElement).toBe(screen.getByTestId('inner-first'))

    viewport.focus()
    fireEvent.keyDown(viewport, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(screen.getByTestId('inner-last'))
  })

  it('调用方的 onKeyDown 先执行，且可用 preventDefault 让出按键', () => {
    const onKeyDown = vi.fn((event: React.KeyboardEvent<HTMLDivElement>) => {
      event.preventDefault()
    })
    const { viewport, scrollBy } = renderScrollable(undefined, { onKeyDown })

    fireEvent.keyDown(viewport, { key: 'ArrowDown' })

    expect(onKeyDown).toHaveBeenCalledTimes(1)
    expect(scrollBy).not.toHaveBeenCalled()
  })
})

describe('滚动区域状态与钩子', () => {
  it('data-scroll-at-* 反映两端状态，滚动后更新', () => {
    const { viewport } = renderScrollable()

    expect(viewport).toHaveAttribute('data-scroll-at-start', 'true')
    expect(viewport).toHaveAttribute('data-scroll-at-end', 'false')

    viewport.scrollTop = 500
    fireEvent.scroll(viewport)

    expect(viewport).toHaveAttribute('data-scroll-at-start', 'false')
    expect(viewport).toHaveAttribute('data-scroll-at-end', 'true')
  })

  it('内容区带 data-scroll-content 钩子，调用方 props 与 ref 都不丢', () => {
    const contentRef = React.createRef<HTMLDivElement>()
    renderScrollable(undefined, { contentRef })

    const content = screen.getByTestId('content')
    expect(content).toHaveAttribute('data-scroll-content', 'true')
    expect(contentRef.current).toBe(content)
    expect(content).toHaveTextContent('纯文本内容')
  })

  it('视口带 data-scroll-axis，供 CSS 按轴取规则', () => {
    renderScrollable()
    expect(screen.getByTestId('viewport')).toHaveAttribute('data-scroll-axis', 'vertical')
  })
})
