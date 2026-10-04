import { render, renderHook, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { SizeContext, SizeProvider, useDensity, type Density } from './SizeProvider'

/**
 * 尺寸上下文测试（FR-496 阶段 6）。
 *
 * 契约只有两条，但都容易被后续批次静默破坏：
 * ① **解析优先级**：显式入参 > Provider > 兜底。任一层写反，最直接的症状是
 *    「页面切了紧凑模式但某块区域纹丝不动」，且不会报错；
 * ② **不渲染 DOM**：Provider 是纯上下文，包一层 div 会让 flex/grid 的直接子元素
 *    关系变化，属于页面级布局事故（故用 children 直连断言，而不是只看功能）。
 */

function providerWrapper(density?: Density) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <SizeProvider density={density}>{children}</SizeProvider>
  }
}

describe('useDensity 解析优先级', () => {
  it('无 Provider、无入参时兜底为 default', () => {
    const { result } = renderHook(() => useDensity())
    expect(result.current).toBe('default')
  })

  it('无 Provider 时可指定兜底值', () => {
    const { result } = renderHook(() => useDensity(undefined, 'compact'))
    expect(result.current).toBe('compact')
  })

  it('从上层 Provider 继承密度', () => {
    const { result } = renderHook(() => useDensity(), {
      wrapper: providerWrapper('compact'),
    })
    expect(result.current).toBe('compact')
  })

  it('显式入参压过 Provider（调用点永远说了算）', () => {
    const { result } = renderHook(() => useDensity('default'), {
      wrapper: providerWrapper('compact'),
    })
    expect(result.current).toBe('default')
  })

  it('Provider 压过兜底值', () => {
    const { result } = renderHook(() => useDensity(undefined, 'compact'), {
      wrapper: providerWrapper('default'),
    })
    expect(result.current).toBe('default')
  })

  it('内层 Provider 覆盖外层（嵌套时取最近的一层）', () => {
    const { result } = renderHook(() => useDensity(), {
      wrapper: ({ children }) => (
        <SizeProvider density="compact">
          <SizeProvider density="default">{children}</SizeProvider>
        </SizeProvider>
      ),
    })
    expect(result.current).toBe('default')
  })

  it('Provider 未传 density 等价于 default（不改变既有表现）', () => {
    const { result } = renderHook(() => useDensity(), {
      wrapper: ({ children }) => <SizeProvider>{children}</SizeProvider>,
    })
    expect(result.current).toBe('default')
  })
})

describe('SizeProvider 的 DOM 契约', () => {
  it('不渲染任何额外元素，children 直连父容器', () => {
    render(
      <div data-testid="host">
        <SizeProvider density="compact">
          <span>紧凑内容</span>
        </SizeProvider>
      </div>,
    )

    const host = screen.getByTestId('host')
    expect(host.children).toHaveLength(1)
    expect(host.firstElementChild?.tagName).toBe('SPAN')
    expect(host).toHaveTextContent('紧凑内容')
  })

  it('上下文标识 SizeContext 可供消费方直接读取（兼容非 hook 场景）', () => {
    expect(SizeContext.displayName).toBe('SizeContext')
  })
})
