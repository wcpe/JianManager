import { describe, it, expect } from 'vitest'
import {
  healthLevelRank,
  healthLevelLabel,
  healthLevelTone,
  summarizeHealthWall,
  healthWallSortLabel,
  formatPct,
} from './health-wall'
import type { HealthLevel, HealthWallNode } from '@/api/metrics'

/** 构造最小健康墙节点（只填分级相关字段）。 */
function cell(p: Partial<HealthWallNode>): HealthWallNode {
  return {
    nodeId: 1,
    nodeUuid: 'n',
    name: 'n',
    freshness: 'fresh',
    cpuPct: null,
    memPct: null,
    diskPct: null,
    running: 0,
    crashed: 0,
    stopped: 0,
    activeAlerts: 0,
    botActive: null,
    botConnecting: null,
    level: 'healthy',
    href: '/monitoring?node=n',
    ...p,
  }
}

describe('health-wall 分级排序与配色', () => {
  it('严重度：offline > stale > degraded > healthy', () => {
    expect(healthLevelRank('offline')).toBeGreaterThan(healthLevelRank('stale'))
    expect(healthLevelRank('stale')).toBeGreaterThan(healthLevelRank('degraded'))
    expect(healthLevelRank('degraded')).toBeGreaterThan(healthLevelRank('healthy'))
  })

  it('每级都有中文标签与语义色', () => {
    const levels: HealthLevel[] = ['offline', 'stale', 'degraded', 'healthy']
    for (const level of levels) {
      expect(healthLevelLabel(level)).not.toBe('')
      expect(healthLevelTone(level)).toBeTruthy()
    }
    expect(healthLevelTone('offline')).toBe('danger')
    expect(healthLevelTone('healthy')).toBe('success')
  })

  it('排序键标签覆盖全部枚举', () => {
    expect(healthWallSortLabel('cpu')).toBe('CPU')
    expect(healthWallSortLabel('instances')).toBe('实例数')
    expect(healthWallSortLabel('level')).toBe('严重度')
  })
})

describe('summarizeHealthWall', () => {
  it('空集合全为 0', () => {
    expect(summarizeHealthWall([])).toEqual({ total: 0, offline: 0, stale: 0, degraded: 0, healthy: 0 })
  })

  it('按分级计数', () => {
    const s = summarizeHealthWall([
      cell({ level: 'offline' }),
      cell({ level: 'offline' }),
      cell({ level: 'stale' }),
      cell({ level: 'degraded' }),
      cell({ level: 'healthy' }),
      cell({ level: 'healthy' }),
      cell({ level: 'healthy' }),
    ])
    expect(s).toEqual({ total: 7, offline: 2, stale: 1, degraded: 1, healthy: 3 })
  })
})

describe('formatPct', () => {
  it('缺测显示 --，其余取整百分比', () => {
    expect(formatPct(null)).toBe('--')
    expect(formatPct(93.4)).toBe('93%')
  })
})
