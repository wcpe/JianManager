import { describe, it, expect } from 'vitest'
import {
  statusCounts,
  healthSegments,
  toListParams,
  groupFilter,
  distribution,
} from './bots-overview'
import type { BotSummary, BotSummaryGroup } from '@/api/bots'

function group(key: string, total: number, online: number, label = key): BotSummaryGroup {
  return { key, label, total, online }
}

describe('statusCounts', () => {
  it('maps byStatus into overview-card counts', () => {
    const summary: BotSummary = {
      total: 10,
      byStatus: { connected: 6, connecting: 2, error: 1, stopped: 1 },
    }
    expect(statusCounts(summary)).toEqual({ total: 10, online: 6, connecting: 2, error: 1 })
  })

  it('defaults missing dimensions to zero', () => {
    expect(statusCounts(undefined)).toEqual({ total: 0, online: 0, connecting: 0, error: 0 })
    expect(statusCounts({ total: 3, byStatus: {} })).toEqual({
      total: 3,
      online: 0,
      connecting: 0,
      error: 0,
    })
  })
})

describe('healthSegments', () => {
  it('splits total into online + other', () => {
    const segs = healthSegments(10, 6)
    expect(segs).toEqual([
      { kind: 'online', count: 6, ratio: 0.6 },
      { kind: 'other', count: 4, ratio: 0.4 },
    ])
  })

  it('returns a single online segment when all online', () => {
    expect(healthSegments(5, 5)).toEqual([{ kind: 'online', count: 5, ratio: 1 }])
  })

  it('returns a single other segment when none online', () => {
    expect(healthSegments(4, 0)).toEqual([{ kind: 'other', count: 4, ratio: 1 }])
  })

  it('returns empty for empty group', () => {
    expect(healthSegments(0, 0)).toEqual([])
  })

  it('clamps online above total to avoid negative other', () => {
    expect(healthSegments(3, 9)).toEqual([{ kind: 'online', count: 3, ratio: 1 }])
  })
})

describe('toListParams', () => {
  it('omits empty filter fields', () => {
    expect(toListParams({})).toEqual({})
    expect(toListParams({ q: '', status: '' })).toEqual({})
  })

  it('keeps set fields including nodeId 0', () => {
    expect(toListParams({ q: 'bot', nodeId: 0, status: 'connected' })).toEqual({
      q: 'bot',
      nodeId: 0,
      status: 'connected',
    })
  })
})

describe('groupFilter', () => {
  const base = { q: 'b', status: 'connected' }

  it('overlays instance group key onto base filter', () => {
    expect(groupFilter('instance', group('7', 3, 1), base)).toEqual({
      q: 'b',
      status: 'connected',
      instanceId: 7,
    })
  })

  it('overlays node group key', () => {
    expect(groupFilter('node', group('2', 5, 5), {})).toEqual({ nodeId: 2 })
  })

  it('uses string key for status / behavior dims', () => {
    expect(groupFilter('status', group('error', 1, 0), {})).toEqual({ status: 'error' })
    expect(groupFilter('behavior', group('guard', 4, 2), {})).toEqual({ behavior: 'guard' })
  })
})

describe('distribution', () => {
  it('counts groups from instance / node summaries', () => {
    const byInstance: BotSummary = {
      total: 8,
      byStatus: {},
      groupBy: 'instance',
      groups: [group('1', 4, 2), group('2', 4, 4)],
    }
    const byNode: BotSummary = {
      total: 8,
      byStatus: {},
      groupBy: 'node',
      groups: [group('1', 8, 6)],
    }
    expect(distribution(byInstance, byNode)).toEqual({ instances: 2, nodes: 1 })
  })

  it('defaults to zero when summaries missing', () => {
    expect(distribution(undefined, undefined)).toEqual({ instances: 0, nodes: 0 })
  })
})
