import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import MetricsTabSegment from './MetricsTabSegment'
import MetricsFallbackSegment from './MetricsFallbackSegment'

/**
 * FR-448 §2.1 监控页签分流与降级视图 · 受控视图测（ADR-097 a 范式）。
 *
 * 本组件在应用侧原本没有测试，故这里是**新补**而非迁移。
 * 两个要点：分流器只按注入结果渲染、自己不做判定；直探摘要缺省即显式「不可用」，
 * 绝不落回探针的伪值（0 玩家 / 0.0 TPS）。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        metrics: {
          fallbackTitle: '轻量监控',
          fallbackHint: '探针未连入，以下为节点侧进程指标与直探摘要',
          directProbeTitle: '直探摘要',
          directProbeUnavailable: '直探不可用（探针未连入且直探编排未落地）',
          directProbeMotd: 'MOTD',
          directProbeOnline: '在线',
          directProbeVersion: '版本',
          directProbeLatency: '延迟',
        },
        process: { title: '进程指标', cpu: 'CPU', memory: '内存', threads: '线程', uptime: '运行时长' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

function Wrapper({ children }: { children: ReactNode }) {
  return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
}

describe('MetricsTabSegment 分流（FR-448 §2.1 · ADR-097）', () => {
  it('style=probe 时只渲染探针分支', () => {
    render(
      <MetricsTabSegment style="probe" probeSlot={<div data-testid="probe-slot" />} fallbackSlot={<div data-testid="fallback-slot" />} />,
      { wrapper: Wrapper },
    )
    expect(screen.getByTestId('probe-slot')).toBeInTheDocument()
    expect(screen.queryByTestId('fallback-slot')).not.toBeInTheDocument()
  })

  it('style=lightweight 时只渲染轻量分支', () => {
    render(
      <MetricsTabSegment style="lightweight" probeSlot={<div data-testid="probe-slot" />} fallbackSlot={<div data-testid="fallback-slot" />} />,
      { wrapper: Wrapper },
    )
    expect(screen.getByTestId('fallback-slot')).toBeInTheDocument()
    expect(screen.queryByTestId('probe-slot')).not.toBeInTheDocument()
  })
})

describe('MetricsFallbackSegment 降级视图（ADR-097 a 范式）', () => {
  it('进程指标缺失时各项显「—」，且不渲染任何探针指标', () => {
    render(<MetricsFallbackSegment metrics={undefined} summary={undefined} />, { wrapper: Wrapper })
    expect(screen.getByTestId('metrics-fallback')).toBeInTheDocument()
    expect(screen.getByTestId('process-panel')).toBeInTheDocument()
    // 探针不在时 TPS/MSPT 之类根本不该出现。
    expect(screen.queryByText(/TPS/)).not.toBeInTheDocument()
  })

  it('直探摘要缺省即显式「不可用」，不落 0 占位', () => {
    render(<MetricsFallbackSegment summary={null} />, { wrapper: Wrapper })
    expect(screen.getByTestId('direct-probe-summary')).toHaveTextContent('直探不可用')
    expect(screen.queryByText('0')).not.toBeInTheDocument()
  })

  it('有直探数据时呈现 MOTD / 在线 / 版本 / 延迟', () => {
    render(
      <MetricsFallbackSegment summary={{ motd: 'A Minecraft Server', onlinePlayers: 3, maxPlayers: 20, version: '1.20.4', latencyMs: 42 }} />,
      { wrapper: Wrapper },
    )
    const summary = screen.getByTestId('direct-probe-summary')
    expect(summary).toHaveTextContent('A Minecraft Server')
    expect(summary).toHaveTextContent('3 / 20')
    expect(summary).toHaveTextContent('1.20.4')
    expect(summary).toHaveTextContent('42ms')
  })
})
