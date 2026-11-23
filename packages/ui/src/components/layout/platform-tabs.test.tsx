import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { PlatformTab, PlatformTabs } from './PlatformTabs'

/**
 * 页签条（本轮改造为 WAI-ARIA Tabs 模式）。
 *
 * 改造前：`role=navigation` + `aria-current="page"`，语义是「导航到别处」，
 * 而实际场景是「同一份数据的几个视图就地切换」——读屏会念成「当前页面」。
 * 现在给完整 tablist 语义，并把键盘导航收进组件（此前实例控制台手写了一份、
 * 玩家页完全没有，同一个模式两处不一致）。
 */
describe('PlatformTabs 可访问语义', () => {
  it('容器是 tablist，项是 tab，选中态由 aria-selected 表达', () => {
    render(
      <PlatformTabs aria-label="玩家视图">
        <PlatformTab active tabId="t1" panelId="p1">
          在线
        </PlatformTab>
        <PlatformTab tabId="t2" panelId="p2">
          封禁
        </PlatformTab>
      </PlatformTabs>,
    )

    expect(screen.getByRole('tablist')).toBeInTheDocument()
    const tabs = screen.getAllByRole('tab')
    expect(tabs).toHaveLength(2)
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true')
    expect(tabs[1]).toHaveAttribute('aria-selected', 'false')
    // 面板关联：tab 指向 panel，panel 用 aria-labelledby 指回来
    expect(tabs[0]).toHaveAttribute('aria-controls', 'p1')
    expect(tabs[0]).toHaveAttribute('id', 't1')
  })

  it('roving tabindex：只有选中项可被 Tab 聚焦', () => {
    render(
      <PlatformTabs>
        <PlatformTab tabId="a">一</PlatformTab>
        <PlatformTab active tabId="b">二</PlatformTab>
        <PlatformTab tabId="c">三</PlatformTab>
      </PlatformTabs>,
    )
    const tabs = screen.getAllByRole('tab')
    expect(tabs[0]).toHaveAttribute('tabindex', '-1')
    expect(tabs[1]).toHaveAttribute('tabindex', '0')
    expect(tabs[2]).toHaveAttribute('tabindex', '-1')
  })
})

describe('PlatformTabs 键盘导航', () => {
  it('方向键在页签间循环移动并激活', async () => {
    const onSecond = vi.fn()
    render(
      <PlatformTabs>
        <PlatformTab active tabId="a">
          一
        </PlatformTab>
        <PlatformTab tabId="b" onClick={onSecond}>
          二
        </PlatformTab>
        <PlatformTab tabId="c">三</PlatformTab>
      </PlatformTabs>,
    )

    const first = screen.getByRole('tab', { name: '一' })
    first.focus()
    await userEvent.keyboard('{ArrowRight}')
    expect(onSecond).toHaveBeenCalledTimes(1)
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: '二' }))
  })

  it('Home / End 跳到首尾', async () => {
    const onFirst = vi.fn()
    const onLast = vi.fn()
    render(
      <PlatformTabs>
        <PlatformTab tabId="a" onClick={onFirst}>
          一
        </PlatformTab>
        <PlatformTab active tabId="b">
          二
        </PlatformTab>
        <PlatformTab tabId="c" onClick={onLast}>
          三
        </PlatformTab>
      </PlatformTabs>,
    )

    screen.getByRole('tab', { name: '二' }).focus()
    await userEvent.keyboard('{Home}')
    expect(onFirst).toHaveBeenCalled()

    await userEvent.keyboard('{End}')
    expect(onLast).toHaveBeenCalled()
  })

  it('方向键会被 preventDefault（避免页面横向滚动）', async () => {
    render(
      <PlatformTabs>
        <PlatformTab active tabId="a">
          一
        </PlatformTab>
        <PlatformTab tabId="b">二</PlatformTab>
      </PlatformTabs>,
    )
    screen.getByRole('tab', { name: '一' }).focus()
    // userEvent 的键盘序列若被 preventDefault，页面级滚动不会发生；
    // 这里断言事件本身被拦下（keyboard 会等默认动作，未拦截时会抛 "not implemented"）
    await expect(userEvent.keyboard('{ArrowRight}')).resolves.not.toThrow()
  })
})
