import { describe, it, expect } from 'vitest'
import type { BusinessEvent } from '@/api/economy'
import type { EconomyMirrorRow } from '@/api/economy'
import {
  toLedgerRow,
  toLedgerRows,
  isValidAmount,
  canQueryLeaderboard,
  fmtEpochMillis,
  sumDecimalStrings,
  aggregateByCurrency,
} from './economy-view'

/** 构造一条经济业务事件 envelope（模拟后端 GET /business/events 的 BusinessEvent 行）。 */
function evt(id: number, data: Record<string, string> | null, extra: Partial<BusinessEvent> = {}): BusinessEvent {
  const frame =
    data === null
      ? 'not-json'
      : JSON.stringify({ type: 'event', event: 'economy_change', domain: 'economy', dedupKey: data.ledgerId, data })
  return {
    id,
    domain: 'economy',
    dedupKey: data?.ledgerId ?? '',
    action: 'economy_change',
    nodeUuid: 'node-1',
    instanceUuid: 'inst-1',
    payloadJson: frame,
    occurredAt: 1700000000000,
    createdAt: '2026-06-25T00:00:00Z',
    ...extra,
  }
}

const goodData = {
  playerName: 'Steve',
  currency: 'coin',
  zoneId: 'zone-a',
  entryType: 'DEPOSIT',
  signedAmount: '100.00',
  balanceAfter: '100.00',
  ledgerId: '42',
  occurredAt: '1700000000123',
}

describe('toLedgerRow 经济 envelope → 流水行', () => {
  it('解析完整 data 为流水行', () => {
    const row = toLedgerRow(evt(1, goodData))
    expect(row).not.toBeNull()
    expect(row).toMatchObject({
      id: 1,
      playerName: 'Steve',
      currency: 'coin',
      zoneId: 'zone-a',
      entryType: 'DEPOSIT',
      signedAmount: '100.00',
      balanceAfter: '100.00',
      ledgerId: '42',
      occurredAt: 1700000000123,
    })
  })

  it('金额字符串原样保留（不丢精度、不转浮点）', () => {
    const row = toLedgerRow(evt(2, { ...goodData, signedAmount: '0.000000000000000001', balanceAfter: '123456789012345.67' }))
    expect(row?.signedAmount).toBe('0.000000000000000001')
    expect(row?.balanceAfter).toBe('123456789012345.67')
  })

  it('payload 非法 JSON → null（坏事件降级，不渲染坏行）', () => {
    expect(toLedgerRow(evt(3, null))).toBeNull()
  })

  it('缺关键字段（无 playerName/currency）→ null', () => {
    expect(toLedgerRow(evt(4, { ledgerId: '7', zoneId: 'zone-a' }))).toBeNull()
  })

  it('occurredAt 缺失时回退 envelope.occurredAt', () => {
    const { occurredAt: _omit, ...noTime } = goodData
    void _omit
    const row = toLedgerRow(evt(5, noTime, { occurredAt: 1699999999000 }))
    expect(row?.occurredAt).toBe(1699999999000)
  })
})

describe('toLedgerRows 批量解析丢弃坏行', () => {
  it('坏事件被丢弃，好事件保序', () => {
    const rows = toLedgerRows([evt(1, goodData), evt(2, null), evt(3, { ...goodData, ledgerId: '43' })])
    expect(rows.map((r) => r.id)).toEqual([1, 3])
  })
})

describe('isValidAmount 金额合法性（禁浮点、恒正）', () => {
  it.each(['1', '10', '0.5', '100.00', '999999999999999.999999'])('合法: %s', (s) => {
    expect(isValidAmount(s)).toBe(true)
  })
  it.each(['', '  ', '0', '-1', 'abc', '1.2.3', '1e3', '1,000', 'NaN'])('非法: %s', (s) => {
    expect(isValidAmount(s)).toBe(false)
  })
})

describe('canQueryLeaderboard 排行须有货币', () => {
  it('空货币不可查', () => {
    expect(canQueryLeaderboard('')).toBe(false)
    expect(canQueryLeaderboard('   ')).toBe(false)
  })
  it('有货币可查', () => {
    expect(canQueryLeaderboard('coin')).toBe(true)
  })
})

describe('fmtEpochMillis', () => {
  it('0/非法显示破折号', () => {
    expect(fmtEpochMillis(0)).toBe('—')
    expect(fmtEpochMillis(-1)).toBe('—')
    expect(fmtEpochMillis(Number.NaN)).toBe('—')
  })
  it('正数格式化为本地时间串（非破折号）', () => {
    expect(fmtEpochMillis(1700000000000)).not.toBe('—')
  })
})

describe('sumDecimalStrings 大数精确十进制求和（禁浮点）', () => {
  it('整数相加', () => {
    expect(sumDecimalStrings(['100', '23', '7'])).toBe('130')
  })
  it('小数对齐相加', () => {
    expect(sumDecimalStrings(['1.5', '2.25', '0.25'])).toBe('4')
  })
  it('超 Number 精度也不失真', () => {
    expect(sumDecimalStrings(['123456789012345.67', '0.33'])).toBe('123456789012346')
  })
  it('极小金额累加不进浮点误差', () => {
    expect(sumDecimalStrings(['0.1', '0.2'])).toBe('0.3')
  })
  it('忽略空串 / 非法项', () => {
    expect(sumDecimalStrings(['10', '', 'abc', '5'])).toBe('15')
  })
  it('空列表为 0', () => {
    expect(sumDecimalStrings([])).toBe('0')
  })
})

describe('aggregateByCurrency 多区聚合余额', () => {
  function row(p: Partial<EconomyMirrorRow>): EconomyMirrorRow {
    return {
      id: 0,
      nodeUuid: 'n',
      zoneId: 'z',
      playerName: 'Steve',
      currency: 'coin',
      currencyId: 1,
      balance: '0',
      lastSeq: 0,
      lastLedgerId: 0,
      lastEntryType: '',
      occurredAt: 0,
      updatedAt: '',
      ...p,
    }
  }
  it('同币种跨节点/区聚合为一行（总额 + 来源区数）', () => {
    const agg = aggregateByCurrency([
      row({ id: 1, currency: 'coin', balance: '100', nodeUuid: 'n1', zoneId: 'a' }),
      row({ id: 2, currency: 'coin', balance: '50', nodeUuid: 'n2', zoneId: 'b' }),
      row({ id: 3, currency: 'gem', balance: '7', nodeUuid: 'n1', zoneId: 'a' }),
    ])
    expect(agg).toHaveLength(2)
    const coin = agg.find((a) => a.currency === 'coin')!
    expect(coin.total).toBe('150')
    expect(coin.sources).toBe(2)
    const gem = agg.find((a) => a.currency === 'gem')!
    expect(gem.total).toBe('7')
    expect(gem.sources).toBe(1)
  })
  it('按币种字典序稳定排序', () => {
    const agg = aggregateByCurrency([row({ currency: 'zeny' }), row({ currency: 'coin' }), row({ currency: 'gem' })])
    expect(agg.map((a) => a.currency)).toEqual(['coin', 'gem', 'zeny'])
  })
  it('空输入 → 空数组', () => {
    expect(aggregateByCurrency([])).toEqual([])
  })
})
