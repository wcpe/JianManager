import { describe, expect, it } from 'vitest'
import {
  METRIC_SOURCE_PROBE,
  METRIC_SOURCE_QUERY,
  METRIC_SOURCE_SLP,
  activeMetricSources,
  hasAnyMetricSource,
  metricSourceLabelKey,
} from './metrics-availability'

/**
 * FR-446/447 可用性位与来源解析：三态（探针+直探 / 仅直探 / 都无）与优先级。
 */
describe('metrics-availability', () => {
  it('sourceMask 按 探针 → SLP → Query 优先级解析来源', () => {
    expect(activeMetricSources({ sourceMask: METRIC_SOURCE_PROBE | METRIC_SOURCE_SLP | METRIC_SOURCE_QUERY })).toEqual([
      'probe',
      'slp',
      'query',
    ])
    expect(activeMetricSources({ sourceMask: METRIC_SOURCE_QUERY | METRIC_SOURCE_SLP })).toEqual(['slp', 'query'])
    expect(activeMetricSources({ sourceMask: METRIC_SOURCE_SLP })).toEqual(['slp'])
    expect(activeMetricSources({ sourceMask: 0 })).toEqual([])
  })

  it('sourceMask 缺省/为 0 时按可用性位回退（兼容旧 CP / devmock）', () => {
    expect(activeMetricSources({ probeAvailable: true, slpAvailable: true })).toEqual(['probe', 'slp'])
    expect(activeMetricSources({ queryAvailable: true })).toEqual(['query'])
    expect(hasAnyMetricSource({})).toBe(false)
    // 三源皆无 → 整体不可用
    expect(hasAnyMetricSource({ probeAvailable: false, slpAvailable: false, queryAvailable: false, sourceMask: 0 })).toBe(false)
  })

  it('来源位非 0 时以 sourceMask 为准（不回退可用性位）', () => {
    // 仅探针命中，但 slpAvailable 误为 true 时也不额外标注 SLP。
    expect(activeMetricSources({ sourceMask: METRIC_SOURCE_PROBE, slpAvailable: true })).toEqual(['probe'])
  })

  it('来源 → i18n 键映射稳定', () => {
    expect(metricSourceLabelKey('probe')).toBe('metrics.source.probe')
    expect(metricSourceLabelKey('slp')).toBe('metrics.source.slp')
    expect(metricSourceLabelKey('query')).toBe('metrics.source.query')
  })
})
