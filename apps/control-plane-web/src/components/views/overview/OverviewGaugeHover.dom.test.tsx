import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useState } from 'react'
import { screen, fireEvent, act } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { GaugeAttributionTooltip } from './OverviewPageView'
import type { OverviewAttributionSnapshot } from './OverviewPageView'

/**
 * 仪表球归因浮层的**触发方式**回归。
 *
 * 用户报障：「鼠标靠近不悬浮显示，需要点击」。改动前只有 PopoverTrigger 的
 * 点击路径，悬停完全无响应。本文件锁住四件事，每一条都是真实会退化的点：
 *
 *  1. 鼠标进球就开（不必点）—— 报障的那条主线；
 *  2. 从球移到面板**不闪**—— 两者之间有 sideOffset 空隙，若各自独立计时，
 *     穿越瞬间会先关后开，比不悬停更难用；
 *  3. 离开后**延迟**才关 —— 斜向移动会短暂扫过其他元素，立即关会乱闪；
 *  4. **点击仍可用** —— 触屏与键盘用户只能靠它（WCAG 2.1 2.1.1），
 *     换成纯悬停等于把无障碍回退路径拆了。
 *
 * 用例必须用**真 state** 驱动 active。只传固定 `active={false}` 时 Popover
 * 根本不渲染面板，任何“面板不该消失”的断言都会落在 body 上空转通过。
 */

function snapshot(): OverviewAttributionSnapshot {
  return {
    sampledAt: '2030-01-01T00:00:00Z',
    freshness: 'fresh',
    nodes: [
      { nodeId: 1, nodeUuid: 'u1', name: 'node-a', status: 'fresh', cpuPct: 40, memoryUsedBytes: 1024 },
    ],
    topInstances: [],
    topProcesses: [],
  }
}

/** 受控包装：active 由真 state 驱动，onToggle 真的会翻转它（与生产一致）。 */
function Controlled() {
  const [active, setActive] = useState(false)
  return (
    <GaugeAttributionTooltip
      gauge="cpu"
      label="CPU"
      active={active}
      onToggle={() => setActive((v) => !v)}
      data={snapshot()}
      isLoading={false}
      isError={false}
    >
      <div>gauge</div>
    </GaugeAttributionTooltip>
  )
}

function setup() {
  renderWithProviders(<Controlled />)
  return { trigger: screen.getByRole('button', { name: /CPU/ }) }
}

/** 面板是否真的渲染出来（而不是 body 兜底）。 */
function panel(): HTMLElement | null {
  return screen.queryByTestId('overview-resource-attribution')
}

describe('GaugeAttributionTooltip 悬停触发', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => { vi.runOnlyPendingTimers(); vi.useRealTimers() })

  it('鼠标进入球即开（不必点击）', () => {
    // 报障主线：改动前面板永远不会出现。
    const { trigger } = setup()
    expect(panel()).toBeNull()

    act(() => { fireEvent.mouseEnter(trigger) })

    expect(panel()).not.toBeNull()
  })

  it('鼠标离开后不立即关，而是延时关', () => {
    // 立即关会在鼠标斜穿其他元素时造成面板乱闪。
    const { trigger } = setup()

    act(() => { fireEvent.mouseEnter(trigger) })
    expect(panel()).not.toBeNull()

    act(() => { fireEvent.mouseLeave(trigger) })
    // 计时未到：仍应开着
    expect(panel()).not.toBeNull()

    act(() => { vi.advanceTimersByTime(200) })
    expect(panel()).toBeNull()
  })

  it('从球移到面板不会被关掉（两触发器共用计时器，不闪）', () => {
    // 球与面板之间有 4px 空隙，鼠标必然穿过。若两者各自计时，
    // 就会在穿越瞬间先关后开——面板一闪，根本读不了数。
    const { trigger } = setup()

    act(() => { fireEvent.mouseEnter(trigger) })
    act(() => { fireEvent.mouseLeave(trigger) })

    // 关闭计时器还在跑，此时鼠标进入面板（真实操作中正在穿空隙）
    const p = panel()
    expect(p).not.toBeNull()
    act(() => { fireEvent.mouseEnter(p as HTMLElement) })

    act(() => { vi.advanceTimersByTime(200) })
    // 计时器已被取消：面板仍在
    expect(panel()).not.toBeNull()
  })

  it('点击触发仍然可用（触屏与键盘回退路径）', () => {
    // 纯悬停会让触屏、键盘用户完全无法打开。
    const { trigger } = setup()

    act(() => { fireEvent.click(trigger) })

    expect(panel()).not.toBeNull()
  })

  it('鼠标反复进入不会把面板刷成关闭态', () => {
    // 同一条路径多次 enter（嵌套元素/快速移动）：已开就不该再 toggle，
    // 否则两次 toggle 等于没开。
    const { trigger } = setup()

    act(() => { fireEvent.mouseEnter(trigger) })
    act(() => { fireEvent.mouseEnter(trigger) })
    act(() => { fireEvent.mouseEnter(trigger) })

    expect(panel()).not.toBeNull()
  })

  it('悬停已开时点一下是「钉住」而不是关闭', () => {
    // 真实鼠标点击与触屏 tap 都必然先派发 mouseenter 再派发 click。
    // 若 click 照 Radix 的 next=false 直接关，用户「移上去+点一下」看到的是
    // 面板一闪就没，触屏更是完全打不开。故此时应保持打开并取消悬停归属。
    const { trigger } = setup()

    act(() => { fireEvent.mouseEnter(trigger) })
    expect(panel()).not.toBeNull()

    act(() => { fireEvent.click(trigger) })
    expect(panel()).not.toBeNull()   // 钉住，没关

    // 钉住后鼠标移开也不再自动收（已不归悬停管）
    act(() => { fireEvent.mouseLeave(trigger) })
    act(() => { vi.advanceTimersByTime(200) })
    expect(panel()).not.toBeNull()
  })
})
