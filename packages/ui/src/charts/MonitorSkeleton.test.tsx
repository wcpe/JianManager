import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { MonitorSkeleton, type MonitorSeriesHook } from './MonitorSkeleton'
import { INSTANCE_CHART_DEFS, type PlotSeries } from '../lib/monitor-metrics'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('./RangePicker', () => ({ RangePicker: () => <div>range picker</div> }))
vi.mock('./MonitorChart', () => ({
  MonitorChart: ({ series, emptyHint }: { series: PlotSeries[]; emptyHint: string }) =>
    series.some((s) => s.points.some((p) => p.value != null))
      ? <div role="img" aria-label="cached chart" /> : <p>{emptyHint}</p>,
}))

const defs = INSTANCE_CHART_DEFS.filter((def) => def.id === 'tps')
function view(result: ReturnType<MonitorSeriesHook> & { isError?: boolean }) {
  return <MonitorSkeleton defs={defs} source={{ kind: 'instance', uuid: 'i' }} useSeries={() => result} />
}

describe('MonitorSkeleton query states', () => {
  it('失败且无缓存时显示error，不显示noData', () => {
    render(view({ series: [], isLoading: false, isError: true }))
    expect(screen.getByRole('alert')).toHaveTextContent('common.error')
    expect(screen.queryByText('common.noData')).not.toBeInTheDocument()
  })
  it('失败且缓存只有null桶时只显示error，不误报noData', () => {
    render(view({ series: [{ metricKey: 'inst_tps', points: [{ ts: '2026-01-01T00:00:00Z', value: null }] }], isLoading: false, isError: true }))
    expect(screen.getByRole('alert')).toHaveTextContent('common.error')
    expect(screen.queryByText('common.noData')).not.toBeInTheDocument()
  })
  it('刷新失败仍保留缓存图，并提示error', () => {
    render(view({ series: [{ metricKey: 'inst_tps', points: [{ ts: '2026-01-01T00:00:00Z', value: 20 }] }], isLoading: false, isError: true }))
    expect(screen.getByRole('alert')).toHaveTextContent('common.error')
    expect(screen.getByRole('img', { name: 'cached chart' })).toBeInTheDocument()
  })
  it('兼容未提供isError的hook，成功空集显示noData', () => {
    render(view({ series: [], isLoading: false }))
    expect(screen.getByText('common.noData')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
  it('首载等待数据时仍显示loading', () => {
    render(view({ series: [], isLoading: true }))
    expect(screen.getByText('common.loading')).toBeInTheDocument()
    expect(screen.queryByText('common.noData')).not.toBeInTheDocument()
  })
})
