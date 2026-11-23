import { render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MetricsOverviewStrip } from './MetricsOverviewStrip'
import type { RawSeries } from '../lib/monitor-metrics'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
const now = Date.parse('2026-01-01T12:00:00Z')
function rawAt(interval: number, age: number, value: number | null = 42): RawSeries[] {
  return [{ metricKey: 'node_cpu_pct', points: [
    { ts: new Date(now - age - interval).toISOString(), value: 20 },
    { ts: new Date(now - age).toISOString(), value },
  ] }]
}
function cpuCard() {
  const card = screen.getByText('monitor.metric.cpu').parentElement
  if (!card) throw new Error('CPU overview missing')
  return card
}

beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(now) })
afterEach(() => { vi.useRealTimers() })

describe('MetricsOverviewStrip sample freshness', () => {
  it('旧末值标出真实采样时间和陈旧，尾部null不刷新值的时间', () => {
    const raw = rawAt(30_000, 600_000)
    raw[0]?.points.push({ ts: new Date(now).toISOString(), value: null })
    render(<MetricsOverviewStrip kind="node" raw={raw} isLoading={false} />)
    const card = cpuCard()
    expect(within(card).getByText('42%')).toBeInTheDocument()
    expect(within(card).getByText('monitor.processSampledAt')).toBeInTheDocument()
    expect(card.querySelector('time')).toHaveAttribute('datetime', new Date(now - 600_000).toISOString())
    expect(within(card).getByText('overview.freshness.stale')).toBeInTheDocument()
  })
  it.each([300_000, 3_600_000])('%ims粒度的正常桶不按90秒误判陈旧', (interval) => {
    render(<MetricsOverviewStrip kind="node" raw={rawAt(interval, interval * 2)} isLoading={false} />)
    expect(within(cpuCard()).getByText('monitor.processSampledAt')).toBeInTheDocument()
    expect(within(cpuCard()).queryByText('overview.freshness.stale')).not.toBeInTheDocument()
  })
  it.each([30_000, 300_000, 3_600_000])('%ims周期缺失超过三个桶后明确标记陈旧', (interval) => {
    render(<MetricsOverviewStrip kind="node" raw={rawAt(interval, interval * 4)} isLoading={false} />)
    expect(within(cpuCard()).getByText('overview.freshness.stale')).toBeInTheDocument()
  })
  it('只有单个样本无法推断周期时仍明确显示采样时间与真实0', () => {
    const ts = new Date(now - 600_000).toISOString()
    render(<MetricsOverviewStrip kind="node" raw={[{ metricKey: 'node_cpu_pct', points: [{ ts, value: 0 }] }]} isLoading={false} />)
    expect(within(cpuCard()).getByText('0%')).toBeInTheDocument()
    expect(within(cpuCard()).getByText('monitor.processSampledAt')).toBeInTheDocument()
    expect(cpuCard().querySelector('time')).toHaveAttribute('datetime', ts)
    expect(within(cpuCard()).queryByText('overview.freshness.stale')).not.toBeInTheDocument()
  })
  it('全缺测保持破折号，不标采样时间', () => {
    render(<MetricsOverviewStrip kind="node" raw={[{ metricKey: 'node_cpu_pct', points: [{ ts: new Date(now).toISOString(), value: null }] }]} isLoading={false} />)
    expect(within(cpuCard()).getByText('—')).toBeInTheDocument()
    expect(cpuCard().querySelector('time')).toBeNull()
  })
})
