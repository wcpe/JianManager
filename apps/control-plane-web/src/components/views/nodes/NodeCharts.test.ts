import { describe, expect, it } from 'vitest'
import { integrateRateBytes, resolveBucketSec, resolveCustomWindow } from './NodeCharts'

const BASE = Date.parse('2026-01-01T00:00:00Z')

/** 构造一串等间隔序列（秒），值来自入参（null 表示该拍缺测）。 */
function series(stepSec: number, values: (number | null)[]) {
  return values.map((value, i) => ({
    ts: new Date(BASE + i * stepSec * 1000).toISOString(),
    value,
  }))
}

describe('resolveBucketSec：一个点代表多长时间', () => {
  it('5m / 1h 档用后端写死的桶宽，不看序列间隔', () => {
    expect(resolveBucketSec(series(300, [1, 1, 1]), '5m')).toBe(300)
    expect(resolveBucketSec(series(3600, [1, 1]), '1h')).toBe(3600)
  })

  it('raw 档用心跳间隔的中位数（心跳周期可变，名义 30s 只是兜底）', () => {
    expect(resolveBucketSec(series(15, [1, 1, 1, 1]), 'raw')).toBe(15)
  })

  it('raw 档中间缺一个桶时中位数仍是真实间隔，不被缺口带偏', () => {
    // 正常 10s，第 3 个桶缺失 → 间隔为 10/10/20/10，中位数仍是 10
    const pts = [
      ...series(10, [1, 1, 1]),
      { ts: new Date(BASE + 30000).toISOString(), value: 1 },
      { ts: new Date(BASE + 40000).toISOString(), value: 1 },
    ]
    expect(resolveBucketSec(pts, 'raw')).toBe(10)
  })

  it('点数不足推不出间隔时退回名义值（raw 30s）', () => {
    expect(resolveBucketSec(series(10, [1]), 'raw')).toBe(30)
    expect(resolveBucketSec([], 'raw')).toBe(30)
  })

  it('档位未知时同样退回名义值', () => {
    expect(resolveBucketSec(series(10, [1]), undefined)).toBe(30)
  })
})

describe('integrateRateBytes：速率序列积成区间总消耗', () => {
  it('每点按桶宽积分（左矩形）', () => {
    // 300B/s、200B/s、100B/s 各持续 300s
    expect(integrateRateBytes(series(300, [300, 200, 100]), 300)).toBe((300 + 200 + 100) * 300)
  })

  it('缺测点（null）不计入', () => {
    expect(integrateRateBytes(series(60, [10, null, 30]), 60)).toBe((10 + 30) * 60)
  })

  it('桶宽非法时不编造数字', () => {
    expect(integrateRateBytes(series(60, [10, 20]), 0)).toBe(0)
    expect(integrateRateBytes(series(60, [10, 20]), Number.NaN)).toBe(0)
  })

  it('空序列与全缺测都给 0', () => {
    expect(integrateRateBytes([], 300)).toBe(0)
    expect(integrateRateBytes(series(60, [null, null]), 60)).toBe(0)
  })

  it('与 resolveBucketSec 串起来即是区间消耗（raw 场景）', () => {
    const pts = series(10, [1000, 2000, 3000, 4000])
    expect(integrateRateBytes(pts, resolveBucketSec(pts, 'raw'))).toBe(10_000 * 10)
  })
})

describe('resolveCustomWindow：自定义时间窗的成立条件', () => {
  it('两头都填且结束晚于开始：给出 RFC3339 的 from/to', () => {
    const w = resolveCustomWindow('2026-01-01T08:00', '2026-01-01T12:00')
    expect(w.customValid).toBe(true)
    expect(w.from).toBe(new Date('2026-01-01T08:00').toISOString())
    expect(w.to).toBe(new Date('2026-01-01T12:00').toISOString())
  })

  it('结束不晚于开始：不成立，且不给出任何窗口', () => {
    expect(resolveCustomWindow('2026-01-01T12:00', '2026-01-01T12:00')).toEqual({ customValid: false })
    expect(resolveCustomWindow('2026-01-01T12:00', '2026-01-01T08:00')).toEqual({ customValid: false })
  })

  it('只填了一头：不成立（不能拿半截条件去打后端）', () => {
    expect(resolveCustomWindow('2026-01-01T08:00', '').customValid).toBe(false)
    expect(resolveCustomWindow('', '2026-01-01T12:00').customValid).toBe(false)
    expect(resolveCustomWindow('', '').customValid).toBe(false)
  })

  it('乱填：不成立', () => {
    expect(resolveCustomWindow('not-a-date', '2026-01-01T12:00').customValid).toBe(false)
  })
})
