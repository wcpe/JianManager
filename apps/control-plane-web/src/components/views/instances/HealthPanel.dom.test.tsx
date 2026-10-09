import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithI18n } from '@/test/i18n'
import { HealthPanel } from '@/components/views/instances/HealthPanel'

/**
 * FR-450 §2.3.2 端口 + 健康检查面板 · 受控视图测（ADR-097 a 范式）。
 *
 * 断言锚在 `data-health-state` 属性上而非文案：可达性四态是本组件的**核心逻辑**，
 * 而文案属消费方语言包，两者耦合会让测试在改文案时假失败。
 */
function reachabilityOf(container: HTMLElement): string | null {
  return container.querySelector('[data-health-state]')?.getAttribute('data-health-state') ?? null
}

describe('HealthPanel（FR-450 · ADR-097 a 范式）', () => {
  it.each([
    ['实例尚未加载 → 未知', {}, 'unknown'],
    ['未运行 → 不可达', { status: 'STOPPED' }, 'unreachable'],
    ['运行中且探针本次取回成功 → 可达', { status: 'RUNNING', serverState: { available: true } }, 'reachable'],
    ['运行中探针在位但取不回 → 超时', { status: 'RUNNING', serverState: { connected: true, available: false } }, 'timeout'],
    ['运行中但无探针 → 未知', { status: 'RUNNING', serverState: {} }, 'unknown'],
  ])('%s', (_name, props, want) => {
    const { container } = renderWithI18n(<HealthPanel {...(props as object)} />)
    expect(reachabilityOf(container)).toBe(want)
  })

  it('只渲染已声明的端口，未声明的不占位', () => {
    const { container } = renderWithI18n(
      <HealthPanel status="RUNNING" serverPort={25565} queryPort={25577} />,
    )
    expect(screen.getByText(':25565')).toBeInTheDocument()
    expect(screen.getByText(':25577')).toBeInTheDocument()
    // 端口卡数量 = 已声明数（probePort 未给 → 不渲染第 3 张）。
    expect(container.querySelectorAll('[data-testid="health-panel"] .rounded-md.border.bg-card').length).toBe(2)
  })

  it('无任何端口时给出空态而非空网格', () => {
    renderWithI18n(<HealthPanel status="RUNNING" />)
    expect(screen.queryByText(/^:/)).not.toBeInTheDocument()
  })
})
