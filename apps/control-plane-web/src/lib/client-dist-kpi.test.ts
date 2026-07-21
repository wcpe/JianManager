import { describe, expect, it } from 'vitest'
import {
  activeClientsHintKey,
  formatKpiRate,
  resolveActiveClients,
  resolveRequestRates,
  resolveUpdateRates,
} from './client-dist-kpi'

describe('client-dist-kpi（FR-356）', () => {
  it('formatKpiRate：分母为 0 的率与非法值', () => {
    expect(formatKpiRate(0)).toBe('0.0%')
    expect(formatKpiRate(0.917)).toBe('91.7%')
    expect(formatKpiRate(null)).toBe('—')
    expect(formatKpiRate(undefined)).toBe('—')
    expect(formatKpiRate(Number.NaN)).toBe('—')
  })

  it('resolveActiveClients：obs 优先且保留 exact/approx', () => {
    const exact = resolveActiveClients({ activeMachines: 12, activeMachinesExact: true }, { activeMachines: 3 })
    expect(exact).toEqual({ value: 12, exactness: 'exact', source: 'observability' })
    expect(activeClientsHintKey(exact.exactness, exact.source)).toBe('clientDistKpi.activeExact')

    const approx = resolveActiveClients({ activeMachines: 512, activeMachinesExact: false })
    expect(approx.exactness).toBe('approx')
    expect(activeClientsHintKey(approx.exactness, approx.source)).toBe('clientDistKpi.activeApprox')
  })

  it('resolveActiveClients：stats 回退不得宣称精确去重', () => {
    const fb = resolveActiveClients(null, { activeMachines: 3, successRate: 0.667 })
    expect(fb).toEqual({ value: 3, exactness: 'unknown', source: 'stats' })
    expect(activeClientsHintKey(fb.exactness, fb.source)).toBe('clientDistKpi.activeFromStats')
  })

  it('resolveUpdateRates：禁止用 stats 请求成功率冒充更新成功率', () => {
    const fromObs = resolveUpdateRates({ successRate: 0.917, failStaticRate: 0.028, rollbackRate: 0.01 })
    expect(fromObs.source).toBe('observability')
    expect(fromObs.successRate).toBe(0.917)

    const none = resolveUpdateRates(null)
    expect(none).toEqual({
      successRate: null,
      failStaticRate: null,
      rollbackRate: null,
      source: 'none',
    })
  })

  it('resolveRequestRates：仅 stats HTTP 率', () => {
    const r = resolveRequestRates({ successRate: 0.667, failureRate: 0.333, activeMachines: 3 })
    expect(r).toEqual({ successRate: 0.667, failureRate: 0.333, source: 'stats' })
  })
})
