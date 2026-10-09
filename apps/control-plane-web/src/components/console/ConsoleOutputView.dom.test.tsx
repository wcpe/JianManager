import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { ConsoleLineBuffer } from '@/lib/console-line-buffer'
import type { LogLine } from '@/lib/console-log-line'
import { renderWithProviders } from '@/test/render'
import ConsoleOutputView from './ConsoleOutputView'

/**
 * 输出区（FR-415，spec §2.1；ADR-086）：虚拟列表 / 自动滚底 / 上滚暂停 / 回溯锚点。
 *
 * jsdom 不做布局，`clientHeight`/`scrollHeight` 恒为 0，所以滚动相关用例显式把这三个
 * 尺寸属性打桩成可控值——否则「是否在底部」永远为真，暂停分支根本走不到。
 */

const ROW_HEIGHT = 22 // fontSize 14 × 1.6 四舍五入
const FALLBACK_VIEWPORT = 640 // useVirtualRows 在无布局环境下的回退视口高
const OVERSCAN = 12

/** 造 n 条真实解析过的行（走行缓冲，避免手捏与生产不一致的假对象）。 */
function makeLines(n: number, prefix = 'line'): readonly LogLine[] {
  const buffer = new ConsoleLineBuffer()
  for (let i = 0; i < n; i++) buffer.appendChunk(`${prefix}-${i}\n`, 'stdout')
  return buffer.snapshot()
}

/** 把容器打桩成「可滚动」：内容 5000px、视口 640px。 */
function stubScrollMetrics(el: HTMLElement, scrollHeight = 5000, clientHeight = FALLBACK_VIEWPORT) {
  let top = 0
  Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => scrollHeight })
  Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => clientHeight })
  Object.defineProperty(el, 'scrollTop', {
    configurable: true,
    get: () => top,
    set: (value: number) => {
      top = value
    },
  })
}

const renderedSeqs = () =>
  Array.from(document.querySelectorAll('[data-console-line-seq]')).map((el) =>
    Number(el.getAttribute('data-console-line-seq')),
  )

/**
 * 追加新行的宿主。
 *
 * 不用 RTL 的 `rerender`：本文件的 `renderWithProviders` 把 Provider 写在元素树里，
 * `rerender` 会连 Provider 一起换掉 → 组件真卸载重挂，跟随态与容器节点全部重置，
 * 测不到「新行到来时的滚动行为」。
 */
function AppendHarness({
  initialLines = 400,
  droppedCount = 0,
  fontSize = 14,
}: {
  initialLines?: number
  droppedCount?: number
  fontSize?: number
}) {
  const [buffer] = useState(() => {
    const created = new ConsoleLineBuffer()
    for (let i = 0; i < initialLines; i++) created.appendChunk(`line-${i}\n`, 'stdout')
    return created
  })
  const [lines, setLines] = useState<readonly LogLine[]>(() => buffer.snapshot())

  return (
    <>
      <button
        type="button"
        onClick={() => {
          for (let i = 0; i < 3; i++) buffer.appendChunk(`incoming-${i}\n`, 'stdout')
          setLines(buffer.snapshot())
        }}
      >
        push-3
      </button>
      <ConsoleOutputView lines={lines} droppedCount={droppedCount} fontSize={fontSize} />
    </>
  )
}

describe('ConsoleOutputView 虚拟列表', () => {
  it('只渲染可视窗口：5000 行时 DOM 里只有几十行', () => {
    renderWithProviders(<ConsoleOutputView lines={makeLines(5000)} droppedCount={0} fontSize={14} />)

    const seqs = renderedSeqs()
    expect(seqs.length).toBeLessThan(60)
    expect(seqs.length).toBeGreaterThan(20)
    // 窗口是连续的一段，且从头开始（jsdom 无布局，滚动位置为 0）。
    expect(seqs[0]).toBe(0)
    expect(seqs).toEqual(seqs.map((_, i) => seqs[0] + i))
    // 远处的行不在 DOM 里——这正是 canvas 换 DOM 后仍能承载 5000 行的原因。
    expect(screen.queryByText('line-4999')).not.toBeInTheDocument()
  })

  it('overscan 为上下各 12 行（spec §2.1）', () => {
    const { container } = renderWithProviders(
      <ConsoleOutputView lines={makeLines(500)} droppedCount={0} fontSize={14} />,
    )
    const output = screen.getByTestId('console-output')
    stubScrollMetrics(output, 500 * ROW_HEIGHT)

    // 滚到第 100 行：窗口起点应为 100 - overscan = 88。
    output.scrollTop = 100 * ROW_HEIGHT
    fireEvent.scroll(output)

    const seqs = renderedSeqs()
    expect(seqs[0]).toBe(100 - OVERSCAN)
    // 窗口尾部也带 overscan：100 + ceil(640/22) + 12。
    expect(seqs.at(-1)).toBe(100 + Math.ceil(FALLBACK_VIEWPORT / ROW_HEIGHT) + OVERSCAN - 1)
    expect(container).toBeTruthy()
  })

  it('行高随字号固定缩放（固定行高是虚拟定位的前提）', () => {
    renderWithProviders(<ConsoleOutputView lines={makeLines(3)} droppedCount={0} fontSize={20} />)
    const row = document.querySelector('[data-console-line-seq="0"]') as HTMLElement
    expect(row.style.height).toBe('32px') // 20 × 1.6
    expect(screen.getByTestId('console-output')).toHaveStyle({ fontSize: '20px' })
  })
})

describe('ConsoleOutputView 自动滚底与上滚暂停（spec §2.1）', () => {
  it('跟随态：新行到来即把视口推到底', () => {
    renderWithProviders(<AppendHarness initialLines={10} />)
    const output = screen.getByTestId('console-output')
    stubScrollMetrics(output)
    expect(output).toHaveAttribute('data-console-follow', 'true')

    fireEvent.click(screen.getByRole('button', { name: 'push-3' }))
    expect(output.scrollTop).toBe(5000)
  })

  it('手动上滚：暂停自动滚底并浮出「跳到底部」', () => {
    renderWithProviders(<ConsoleOutputView lines={makeLines(400)} droppedCount={0} fontSize={14} />)
    const output = screen.getByTestId('console-output')
    stubScrollMetrics(output)

    expect(screen.queryByRole('button', { name: /跳到底部/ })).not.toBeInTheDocument()

    output.scrollTop = 1000
    fireEvent.scroll(output)

    expect(output).toHaveAttribute('data-console-follow', 'false')
    expect(screen.getByRole('button', { name: '跳到底部' })).toBeInTheDocument()
  })

  it('暂停期间涌入的新行被计数，浮标带「N 条新」', () => {
    renderWithProviders(<AppendHarness initialLines={400} />)
    const output = screen.getByTestId('console-output')
    stubScrollMetrics(output)
    output.scrollTop = 1000
    fireEvent.scroll(output)

    fireEvent.click(screen.getByRole('button', { name: 'push-3' }))

    expect(screen.getByRole('button', { name: '跳到底部（3 条新）' })).toBeInTheDocument()
    // 暂停期间不得偷偷把视口拉走。
    expect(output.scrollTop).toBe(1000)
  })

  it('点浮标回到底部并恢复跟随，浮标随之消失', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ConsoleOutputView lines={makeLines(400)} droppedCount={0} fontSize={14} />)
    const output = screen.getByTestId('console-output')
    stubScrollMetrics(output)
    output.scrollTop = 1000
    fireEvent.scroll(output)

    await user.click(screen.getByRole('button', { name: '跳到底部' }))

    expect(output.scrollTop).toBe(5000)
    expect(output).toHaveAttribute('data-console-follow', 'true')
    expect(screen.queryByRole('button', { name: /跳到底部/ })).not.toBeInTheDocument()
  })

  it('滚回底部（在容差内）自动恢复跟随，无需点浮标', () => {
    renderWithProviders(<ConsoleOutputView lines={makeLines(400)} droppedCount={0} fontSize={14} />)
    const output = screen.getByTestId('console-output')
    stubScrollMetrics(output)
    output.scrollTop = 1000
    fireEvent.scroll(output)
    expect(output).toHaveAttribute('data-console-follow', 'false')

    output.scrollTop = 5000 - FALLBACK_VIEWPORT
    fireEvent.scroll(output)
    expect(output).toHaveAttribute('data-console-follow', 'true')
  })
})

describe('ConsoleOutputView 回溯锚点与空态', () => {
  it('环形缓冲丢过行时顶部挂「更早日志需回溯」锚点（FR-419 据此加载）', () => {
    renderWithProviders(<ConsoleOutputView lines={makeLines(5)} droppedCount={137} fontSize={14} />)
    expect(screen.getByTestId('console-earlier-anchor')).toHaveTextContent('更早日志需回溯（已丢弃 137 行）')
  })

  it('未丢弃时不挂锚点（不制造「日志不全」的错觉）', () => {
    renderWithProviders(<ConsoleOutputView lines={makeLines(5)} droppedCount={0} fontSize={14} />)
    expect(screen.queryByTestId('console-earlier-anchor')).not.toBeInTheDocument()
  })

  it('无输出时给空态文案而非空白', () => {
    renderWithProviders(<ConsoleOutputView lines={[]} droppedCount={0} fontSize={14} />)
    expect(screen.getByText('暂无输出')).toBeInTheDocument()
  })
})

describe('ConsoleOutputView 行渲染', () => {
  it('拆出的时间 / 级别 / 来源 / 正文分列渲染', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('[09:12:13] [Server thread/WARN]: [MyPlugin] Can not keep up\n', 'stdout')
    renderWithProviders(<ConsoleOutputView lines={buffer.snapshot()} droppedCount={0} fontSize={14} />)

    expect(screen.getByText('09:12:13')).toBeInTheDocument()
    expect(screen.getByText('WARN')).toBeInTheDocument()
    // 来源列渲染成 `[MyPlugin]`（方括号是展示装饰，模型里 source 不含括号）。
    expect(screen.getByText('[MyPlugin]')).toBeInTheDocument()
    expect(screen.getByText('Can not keep up')).toBeInTheDocument()
  })

  it('ANSI SGR 着色渲染为行内颜色，未知转义不留可见垃圾', () => {
    const buffer = new ConsoleLineBuffer()
    // 绿色正文 + 一段清屏转义（必须被吞掉）。
    buffer.appendChunk('\u001b[32mgreen-part\u001b[0m\u001b[2Jtail\n', 'stdout')
    renderWithProviders(<ConsoleOutputView lines={buffer.snapshot()} droppedCount={0} fontSize={14} />)

    expect(screen.getByText('green-part')).toHaveStyle({ color: '#4e9a06' })
    // reset 之后的文本回到默认色（无 inline color）。
    expect(screen.getByText('tail')).not.toHaveStyle({ color: '#4e9a06' })
    // 行的可见文本只有「级别列 + 正文」（首行无继承源，ts 为 undefined 不渲染时间列），
    // 清屏转义整段消失、不留任何残字。
    const row = document.querySelector('[data-console-line-seq="0"]')!
    expect(row.textContent).toBe('INFOgreen-parttail')
    expect(row.textContent).not.toContain('2J')
    expect(row.textContent).not.toContain('\u001b')
  })

  it('命令回显与系统提示带独立 kind 标记（供样式与后续过滤区分）', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendCommand('say hi')
    buffer.appendSystem('[连接已断开]')
    renderWithProviders(<ConsoleOutputView lines={buffer.snapshot()} droppedCount={0} fontSize={14} />)

    expect(document.querySelector('[data-console-line-seq="0"]')).toHaveAttribute('data-console-line-kind', 'command')
    expect(document.querySelector('[data-console-line-seq="1"]')).toHaveAttribute('data-console-line-kind', 'system')
  })

  it('堆栈帧标记为 stack-frame，且默认被折叠进块（FR-418）', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('java.lang.NullPointerException: boom\n\tat a.B.c(B.java:1)\n', 'stdout')
    renderWithProviders(<ConsoleOutputView lines={buffer.snapshot()} droppedCount={0} fontSize={14} />)

    // FR-418 起堆栈默认折叠，故帧行先不在 DOM 里；展开后仍必须带 stack-frame 标记
    // ——即「解析器标类别」与「折叠层用它收起来」两件事都要成立。
    expect(document.querySelector('[data-console-line-seq="1"]')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '展开堆栈' }))
    expect(document.querySelector('[data-console-line-seq="1"]')).toHaveAttribute(
      'data-console-line-kind',
      'stack-frame',
    )
  })
})

describe('ConsoleOutputView 自动换行（可用性增强）', () => {
  it('换行开：超长行按估算占多行高，布局类切到折行', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk(`${'x'.repeat(300)}\n`, 'stdout')
    renderWithProviders(<ConsoleOutputView lines={buffer.snapshot()} droppedCount={0} fontSize={14} wrap />)

    const row = document.querySelector('[data-console-line-seq="0"]') as HTMLElement
    // jsdom 无 canvas → charWidth 回退 14×0.62=8.68；正文 300 字 ≈ 2604px，
    // 行宽 = 回退 cross 1024 − 16(padding) = 992 → ceil(2606/992)=3 行 → 高 66px。
    expect(row.style.height).toBe('66px')
    expect(row.className).toContain('whitespace-pre-wrap')
  })

  it('换行开：短行仍占一行高', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('short line\n', 'stdout')
    renderWithProviders(<ConsoleOutputView lines={buffer.snapshot()} droppedCount={0} fontSize={14} wrap />)

    const row = document.querySelector('[data-console-line-seq="0"]') as HTMLElement
    expect(row.style.height).toBe('22px')
  })

  it('换行关（默认）：保持等高与不折行布局，行为零变化', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk(`${'x'.repeat(300)}\n`, 'stdout')
    renderWithProviders(<ConsoleOutputView lines={buffer.snapshot()} droppedCount={0} fontSize={14} />)

    const row = document.querySelector('[data-console-line-seq="0"]') as HTMLElement
    expect(row.style.height).toBe('22px')
    expect(row.className).toContain('whitespace-pre')
    expect(row.className).not.toContain('whitespace-pre-wrap')
  })
})
