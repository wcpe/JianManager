import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { NodeStartupProgressPanel } from '@/components/views/nodes/NodeStartupProgressPanel'
import type { NodeStartupProgressInfo } from '@/api/nodes'

/** 
 *范围②：节点页「正在启动的 Worker」提示条。
 *
 * 三条必须守住的行为：
 *  1. 空态**不占位**——绝大多数时间没有正在启动的节点，留一块空白只是噪声；
 *  2. 正常进度显示「阶段 · n/m」与已耗时；
 *  3. stale 显示「启动中断」而不是继续显示进度——这两者的运维动作完全不同
 *     （前者等人去查进程，后者只需等待），视觉与文案都必须可分。
 */

function progress(over: Partial<NodeStartupProgressInfo> = {}): NodeStartupProgressInfo {
  return {
    key: 'name:acc-node',
    nodeName: 'acc-node',
    phase: 'ingest_state_load',
    phaseElapsedMs: 3200,
    totalElapsedMs: 4100,
    phasesDone: 5,
    phasesTotal: 7,
    updatedAt: '2030-01-01T00:00:00Z',
    stale: false,
    ...over,
  }
}

describe('NodeStartupProgressPanel', () => {
  it('空态不渲染任何内容（不占位）', () => {
    const { container } = renderWithProviders(<NodeStartupProgressPanel items={[]} />)
    // 不留空容器：否则页面上会多出一块无意义的留白
    expect(container).toBeEmptyDOMElement()
  })

  it('加载中不渲染（避免启动瞬间闪一条空提示）', () => {
    const { container } = renderWithProviders(
      <NodeStartupProgressPanel items={[progress()]} loading />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it('显示阶段中文名、进度 n/m 与已耗时', () => {
    renderWithProviders(<NodeStartupProgressPanel items={[progress()]} />)

    expect(screen.getByText('acc-node')).toBeInTheDocument()
    // 阶段名走中文对照：ingest_state_load → 加载采集状态
    expect(screen.getByText(/加载采集状态 · 5\/7/)).toBeInTheDocument()
    expect(screen.getByText(/4\.1s/)).toBeInTheDocument()
  })

  it('未收录的阶段名原样显示，不静默吞掉', () => {
    // 新阶段上线但前端还没补中文时，界面上应看到原始标识而不是空白——
    // 空白会让「后端上报了新阶段」这件事完全不可见。
    renderWithProviders(
      <NodeStartupProgressPanel items={[progress({ phase: 'some_future_phase' })]} />,
    )
    expect(screen.getByText(/some_future_phase/)).toBeInTheDocument()
  })

  it('stale 时显示「启动中断」且不再显示进度条', () => {
    // 关键取舍：中断与「正在启动」的运维动作完全不同（前者要查进程，后者只需等）。
    // 因此 stale 时不显示进度条——一个停在 5/7 的进度条会让人以为它在慢慢推进。
    renderWithProviders(<NodeStartupProgressPanel items={[progress({ stale: true })]} />)

    expect(screen.getByText('启动中断')).toBeInTheDocument()
    expect(screen.getByText(/进程可能已退出/)).toBeInTheDocument()
    expect(screen.queryByRole('progressbar')).toBeNull()
  })

  it('多个节点各自一行', () => {
    renderWithProviders(
      <NodeStartupProgressPanel
        items={[
          progress({ key: 'a', nodeName: 'node-a' }),
          progress({ key: 'b', nodeName: 'node-b', phasesDone: 2 }),
        ]}
      />,
    )
    expect(screen.getByText('node-a')).toBeInTheDocument()
    expect(screen.getByText('node-b')).toBeInTheDocument()
    expect(screen.getByText(/加载采集状态 · 2\/7/)).toBeInTheDocument()
  })

  it('进度条带可达名称（落在外层 role=progressbar 元素上）', () => {
    renderWithProviders(<NodeStartupProgressPanel items={[progress()]} />)
    const bar = screen.getByRole('progressbar')
    expect(bar).toHaveAttribute('aria-valuenow', '71') // round(5/7*100)
    expect(bar).toHaveAttribute('aria-label', 'acc-node 的启动进度')
  })
})
