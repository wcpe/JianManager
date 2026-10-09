import { describe, expect, it } from 'vitest'
import {
  DEFAULT_SEG_BY_TAB,
  isOpsSeg,
  isOpsTab,
  normalizeOpsTab,
  resolveOpsSeg,
} from './client-dist-ops-tab'

describe('normalizeOpsTab（旧路由 → 新 Tab 归一化）', () => {
  describe('source=monitor（旧 /client-dist-monitor）', () => {
    it('statistics|monitor|clients 落到同名 Tab', () => {
      expect(normalizeOpsTab('statistics', 'monitor')).toEqual({ tab: 'statistics' })
      expect(normalizeOpsTab('monitor', 'monitor')).toEqual({ tab: 'monitor' })
      expect(normalizeOpsTab('clients', 'monitor')).toEqual({ tab: 'clients' })
    })

    it('logs 补 type=request', () => {
      expect(normalizeOpsTab('logs', 'monitor')).toEqual({ tab: 'logs', type: 'request' })
    })

    it('缺失/非法 → statistics（监控页 landing）', () => {
      expect(normalizeOpsTab(null, 'monitor')).toEqual({ tab: 'statistics' })
      expect(normalizeOpsTab('', 'monitor')).toEqual({ tab: 'statistics' })
      expect(normalizeOpsTab('   ', 'monitor')).toEqual({ tab: 'statistics' })
      expect(normalizeOpsTab('bogus', 'monitor')).toEqual({ tab: 'statistics' })
      // 安全页旧别名不属监控来源 → 回落监控 landing。
      expect(normalizeOpsTab('events', 'monitor')).toEqual({ tab: 'statistics' })
    })

    it('页面 B 直达 canonical Tab 亦透传（statistics/monitor/clients/logs）', () => {
      expect(normalizeOpsTab('overview', 'monitor')).toEqual({ tab: 'overview' })
      expect(normalizeOpsTab('profiles', 'monitor')).toEqual({ tab: 'profiles' })
      expect(normalizeOpsTab('actions', 'monitor')).toEqual({ tab: 'actions' })
    })
  })

  describe('source=security（旧 /client-dist-security）', () => {
    it('overview/logs 原地保留', () => {
      expect(normalizeOpsTab('overview', 'security')).toEqual({ tab: 'overview' })
      expect(normalizeOpsTab('logs', 'security')).toEqual({ tab: 'logs' })
    })

    it('events → monitor + seg=events', () => {
      expect(normalizeOpsTab('events', 'security')).toEqual({ tab: 'monitor', seg: 'events' })
    })

    it('profiles/ip/players → profiles + 对应 seg', () => {
      expect(normalizeOpsTab('profiles', 'security')).toEqual({ tab: 'profiles', seg: 'client' })
      expect(normalizeOpsTab('ip', 'security')).toEqual({ tab: 'profiles', seg: 'ip' })
      expect(normalizeOpsTab('players', 'security')).toEqual({ tab: 'profiles', seg: 'player' })
    })

    it('actions/groups → actions + 对应 seg', () => {
      expect(normalizeOpsTab('actions', 'security')).toEqual({ tab: 'actions', seg: 'actions' })
      expect(normalizeOpsTab('groups', 'security')).toEqual({ tab: 'actions', seg: 'groups' })
    })

    it('缺失/非法 → overview（安全页 landing）', () => {
      expect(normalizeOpsTab(undefined, 'security')).toEqual({ tab: 'overview' })
      expect(normalizeOpsTab('', 'security')).toEqual({ tab: 'overview' })
      expect(normalizeOpsTab('nope', 'security')).toEqual({ tab: 'overview' })
      // 监控页旧 key 不属安全来源 → 回落安全 landing。
      expect(normalizeOpsTab('clients', 'security')).toEqual({ tab: 'clients' })
      expect(normalizeOpsTab('statistics', 'security')).toEqual({ tab: 'statistics' })
    })
  })
})

describe('isOpsTab / isOpsSeg', () => {
  it('识别 canonical Tab', () => {
    for (const t of ['overview', 'statistics', 'monitor', 'logs', 'clients', 'profiles', 'actions']) {
      expect(isOpsTab(t)).toBe(true)
    }
    expect(isOpsTab('events')).toBe(false)
    expect(isOpsTab('ip')).toBe(false)
  })

  it('识别合法 seg', () => {
    for (const s of ['live', 'events', 'client', 'ip', 'player', 'actions', 'groups']) {
      expect(isOpsSeg(s)).toBe(true)
    }
    expect(isOpsSeg('bogus')).toBe(false)
  })
})

describe('resolveOpsSeg（页面 B 分档解析）', () => {
  it('显式合法 seg 优先', () => {
    expect(resolveOpsSeg('ip', 'profiles')).toBe('ip')
    expect(resolveOpsSeg('groups', 'actions')).toBe('groups')
    expect(resolveOpsSeg('events', 'monitor')).toBe('events')
  })

  it('非法/缺失 seg 时回落别名派生或 Tab 缺省', () => {
    expect(resolveOpsSeg(null, 'monitor', 'events')).toBe('events')
    expect(resolveOpsSeg('bogus', 'monitor', 'events')).toBe('events')
    expect(resolveOpsSeg(null, 'monitor')).toBe('live')
    expect(resolveOpsSeg(null, 'profiles')).toBe('client')
    expect(resolveOpsSeg(null, 'actions')).toBe('actions')
    expect(resolveOpsSeg(null, 'logs')).toBeUndefined()
    expect(resolveOpsSeg(null, 'overview')).toBeUndefined()
  })

  it('DEFAULT_SEG_BY_TAB 仅覆盖有分档的 Tab', () => {
    expect(DEFAULT_SEG_BY_TAB).toEqual({ monitor: 'live', profiles: 'client', actions: 'actions' })
  })
})
