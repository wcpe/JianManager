/**
 * @file 侧栏指标显示格式单测。
 *
 * 回归来源：悬浮卡把时序库的浮点 `avg` 原样输出，显示成 `18.208000000000002`；
 * 而行尾同一份数据却取了整——两处各写一份、只有一处修对。这些用例锁住「两处一致」。
 */
import { describe, it, expect } from 'vitest'

import {
  formatCpuPct,
  formatMemGb,
  formatPlayers,
  formatPlayersRatio,
  formatTps,
} from './metric-display'

describe('formatPlayers', () => {
  it('把时序库的浮点均值收敛成整数（回归：18.208000000000002）', () => {
    expect(formatPlayers(18.208000000000002)).toBe('18')
    // 四舍五入而非截断：18.6 人应显示 19，否则长期少算。
    expect(formatPlayers(18.6)).toBe('19')
    expect(formatPlayers(0)).toBe('0')
  })

  it('缺测返回空串（不写占位符，由调用方决定呈现）', () => {
    expect(formatPlayers(null)).toBe('')
  })
})

describe('formatPlayersRatio', () => {
  it('当前/最大都用整数，上限同样收敛', () => {
    expect(formatPlayersRatio(18.208, 50)).toBe('18/50')
    expect(formatPlayersRatio(18.6, 50.4)).toBe('19/50')
  })

  it('上限缺测时只显示当前人数（不能写成「18/0」或「18/--」）', () => {
    expect(formatPlayersRatio(18.2, null)).toBe('18人')
  })

  it('当前人数缺测时整体为空（单独的上限没有展示意义）', () => {
    expect(formatPlayersRatio(null, 50)).toBe('')
  })
})

describe('formatTps', () => {
  it('保留一位小数，且不露出浮点尾巴', () => {
    expect(formatTps(19.85)).toBe('19.9')
    expect(formatTps(18.208000000000002)).toBe('18.2')
    // 量化后再 toFixed：不能出现 18.200000000000003 这类结果
    expect(formatTps(18.2)).toBe('18.2')
    expect(formatTps(20)).toBe('20.0')
  })

  it('缺测返回空串', () => {
    expect(formatTps(null)).toBe('')
  })
})

describe('formatCpuPct / formatMemGb', () => {
  it('CPU 取整百分数', () => {
    expect(formatCpuPct(61.4)).toBe('61%')
    expect(formatCpuPct(null)).toBe('')
  })

  it('内存换算成 GB 且固定两位', () => {
    expect(formatMemGb(2.5 * 1024 ** 3)).toBe('2.50G')
    expect(formatMemGb(null)).toBe('')
  })
})
