import { describe, expect, it } from 'vitest'
import { CAPABILITY_REGISTRY, UNIVERSAL_FALLBACK } from './capabilities'
import { resolveMetricsTabStyle } from './metrics-source'

const backend = CAPABILITY_REGISTRY['minecraft_java:backend']
const beacon = CAPABILITY_REGISTRY['generic:beacon']

/**
 * FR-448 §2.1：metrics Tab 的探针有无两套样式判定。
 * 探针全量样式要求「画像声明 probe 来源」且「运行时探针可用」二者同时成立。
 */
describe('resolveMetricsTabStyle（FR-448 探针有无两套样式判定）', () => {
  it('backend 画像 + 探针可用 → 探针全量样式', () => {
    expect(resolveMetricsTabStyle(backend, { probeAvailable: true })).toBe('probe')
  })

  it('backend 画像 + 探针不可用 → 轻量降级样式', () => {
    expect(resolveMetricsTabStyle(backend, { probeAvailable: false })).toBe('lightweight')
  })

  it('探针位缺省（undefined）视为不可用 → 轻量降级', () => {
    expect(resolveMetricsTabStyle(backend, {})).toBe('lightweight')
  })

  it('画像未声明 probe 为来源（beacon）→ 轻量降级，即便运行时探针被误置位', () => {
    expect(resolveMetricsTabStyle(beacon, { probeAvailable: true })).toBe('lightweight')
  })

  it('兜底画像（未知组合）无 metrics 来源 → 轻量降级', () => {
    expect(resolveMetricsTabStyle(UNIVERSAL_FALLBACK, { probeAvailable: true })).toBe('lightweight')
  })

  it('FR-447 接入点：sourceAvailable.probe 优先于 probeAvailable', () => {
    expect(resolveMetricsTabStyle(backend, { probeAvailable: false, sourceAvailable: { probe: true } })).toBe('probe')
    expect(resolveMetricsTabStyle(backend, { probeAvailable: true, sourceAvailable: { probe: false } })).toBe('lightweight')
  })
})
