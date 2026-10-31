import { act, render, screen } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import { TimeSeriesChart, type ChartSeries } from './TimeSeriesChart'
import { MonitorChart } from './MonitorChart'

/**
 * 图表替代文本（可访问性）用例。
 *
 * 缺陷背景：两张图都只画裸 SVG，容器连 `role="img"` 都没有，读屏用户得不到任何可读的替代信息
 * （统计页 / 分发运维 / 会话指标共 10+ 处趋势图都受影响）。本文件锁死：
 * ① 绘图容器是有名字的 `role="img"`；
 * ② 名字可由序列名推导（组件库不写死文案）；
 * ③ 图例不只是色块——序列名以文字呈现（仅靠颜色传达信息对色觉障碍用户不可用）。
 *
 * 组件自测容器宽度（不用 ResponsiveContainer，见 BUG-007），故这里把 ResizeObserver 打桩成
 * 立即回报宽度，再用假定时器跨过 2000ms 的重绘防抖。
 */

beforeAll(async () => {
  await i18n.use(initReactI18next).init({
    lng: 'zh-CN',
    fallbackLng: 'zh-CN',
    resources: { 'zh-CN': { translation: { common: { noData: '暂无数据' } } } },
    interpolation: { escapeValue: false },
  })
})

beforeEach(() => {
  vi.useFakeTimers()
  vi.stubGlobal(
    'ResizeObserver',
    // 不用构造参数属性（`constructor(private readonly cb)`）：本仓库开启了
    // `erasableSyntaxOnly`，那只存在于类型层面的语法会被拒绝。
    class MockResizeObserver {
      private readonly cb: ResizeObserverCallback
      constructor(cb: ResizeObserverCallback) {
        this.cb = cb
      }
      observe() {
        this.cb([{ contentRect: { width: 600 } }] as unknown as ResizeObserverEntry[], this as unknown as ResizeObserver)
      }
      unobserve() {}
      disconnect() {}
    },
  )
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const series: ChartSeries[] = [
  { key: 'tps', name: 'TPS', points: [{ ts: '2025-01-01T00:00:00Z', value: 20 }, { ts: '2025-01-01T01:00:00Z', value: 30 }] },
  { key: 'mspt', name: 'MSPT', points: [{ ts: '2025-01-01T00:00:00Z', value: 8 }, { ts: '2025-01-01T01:00:00Z', value: 9 }] },
]

const wrap = (ui: React.ReactElement) => render(<I18nextProvider i18n={i18n}>{ui}</I18nextProvider>)

describe('TimeSeriesChart 可访问性', () => {
  it('绘图容器是有名字的 role="img"，名字由序列名推导', () => {
    wrap(<TimeSeriesChart series={series} />)
    act(() => {
      vi.advanceTimersByTime(2100)
    })

    expect(screen.getByRole('img', { name: 'TPS, MSPT' })).toBeInTheDocument()
  })

  it('显式 ariaLabel 覆盖推导值', () => {
    wrap(<TimeSeriesChart series={series} ariaLabel="近 24 小时 TPS 趋势" />)
    act(() => {
      vi.advanceTimersByTime(2100)
    })

    expect(screen.getByRole('img', { name: '近 24 小时 TPS 趋势' })).toBeInTheDocument()
  })

  it('图例带文字标签，不是只靠色块区分序列', () => {
    const { container } = wrap(<TimeSeriesChart series={series} />)
    act(() => {
      vi.advanceTimersByTime(2100)
    })

    const legend = container.querySelector('.recharts-legend-wrapper')
    expect(legend).not.toBeNull()
    expect(legend?.textContent).toContain('TPS')
    expect(legend?.textContent).toContain('MSPT')
  })
})

describe('MonitorChart 可访问性', () => {
  it('绘图容器是有名字的 role="img"', () => {
    wrap(
      <MonitorChart
        series={[
          { key: 'online', name: '在线', points: [{ ts: '2025-01-01T00:00:00Z', value: 1 }] },
          { key: 'lag', name: '延迟', points: [{ ts: '2025-01-01T00:00:00Z', value: 2 }] },
        ]}
      />,
    )
    act(() => {
      vi.advanceTimersByTime(2100)
    })

    expect(screen.getByRole('img', { name: '在线, 延迟' })).toBeInTheDocument()
  })

  it('显式 ariaLabel 覆盖推导值', () => {
    wrap(
      <MonitorChart
        series={[{ key: 'online', name: '在线', points: [{ ts: '2025-01-01T00:00:00Z', value: 1 }] }]}
        ariaLabel="节点在线人数"
      />,
    )
    act(() => {
      vi.advanceTimersByTime(2100)
    })

    expect(screen.getByRole('img', { name: '节点在线人数' })).toBeInTheDocument()
  })
})
