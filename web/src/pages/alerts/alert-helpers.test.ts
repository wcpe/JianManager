import { describe, it, expect } from 'vitest'
import {
  levelBadgeClass,
  levelStatusLevel,
  triggerUsesMetric,
  triggerUsesKeyword,
  triggerUsesEventMatch,
  channelUsesURL,
  channelIsTelegram,
  channelIsEmail,
  channelIsInApp,
  isEnvRef,
  formatSilenceWindow,
  isValidHHMM,
  parseChannelIds,
  summarizeRules,
} from './alert-helpers'

describe('levelBadgeClass', () => {
  it('distinguishes levels', () => {
    expect(levelBadgeClass('critical')).toContain('destructive')
    expect(levelBadgeClass('warn')).toContain('amber')
    expect(levelBadgeClass('info')).toContain('sky')
    expect(levelBadgeClass('unknown')).toContain('sky') // default
  })
})

describe('levelStatusLevel', () => {
  it('maps alert levels to status levels', () => {
    expect(levelStatusLevel('critical')).toBe('danger')
    expect(levelStatusLevel('warn')).toBe('warning')
    expect(levelStatusLevel('info')).toBe('info')
    expect(levelStatusLevel('unknown')).toBe('info') // default
  })
})

describe('summarizeRules', () => {
  it('counts total and enabled', () => {
    expect(summarizeRules([{ enabled: true }, { enabled: false }, { enabled: true }])).toEqual({
      total: 3,
      enabled: 2,
    })
  })
  it('empty list', () => {
    expect(summarizeRules([])).toEqual({ total: 0, enabled: 0 })
  })
})

describe('trigger field visibility', () => {
  it('metric fields only for metric', () => {
    expect(triggerUsesMetric('metric')).toBe(true)
    expect(triggerUsesMetric('log_keyword')).toBe(false)
  })
  it('keyword field only for log_keyword', () => {
    expect(triggerUsesKeyword('log_keyword')).toBe(true)
    expect(triggerUsesKeyword('metric')).toBe(false)
  })
  it('event match only for player_event', () => {
    expect(triggerUsesEventMatch('player_event')).toBe(true)
    expect(triggerUsesEventMatch('node_offline')).toBe(false)
  })
})

describe('channel field visibility', () => {
  it('url channels', () => {
    for (const t of ['webhook', 'dingtalk', 'wecom', 'feishu', 'discord']) {
      expect(channelUsesURL(t)).toBe(true)
    }
    expect(channelUsesURL('telegram')).toBe(false)
    expect(channelUsesURL('email')).toBe(false)
  })
  it('telegram / email / inapp', () => {
    expect(channelIsTelegram('telegram')).toBe(true)
    expect(channelIsEmail('email')).toBe(true)
    expect(channelIsInApp('inapp')).toBe(true)
    expect(channelIsInApp('webhook')).toBe(false)
  })
})

describe('isEnvRef', () => {
  it('accepts ${VAR}', () => {
    expect(isEnvRef('${JM_WEBHOOK}')).toBe(true)
    expect(isEnvRef('  ${A_B_1}  ')).toBe(true)
  })
  it('rejects plain / malformed', () => {
    expect(isEnvRef('https://x.com')).toBe(false)
    expect(isEnvRef('${1bad}')).toBe(false)
    expect(isEnvRef('$VAR')).toBe(false)
    expect(isEnvRef('')).toBe(false)
  })
})

describe('formatSilenceWindow', () => {
  it('same-day range', () => {
    expect(formatSilenceWindow('09:00', '18:00')).toBe('09:00 → 18:00')
  })
  it('cross-midnight marked', () => {
    expect(formatSilenceWindow('23:00', '07:00')).toBe('23:00 → 07:00(次日)')
  })
  it('empty when unset', () => {
    expect(formatSilenceWindow('', '07:00')).toBe('')
    expect(formatSilenceWindow('23:00', '')).toBe('')
  })
})

describe('isValidHHMM', () => {
  it('valid', () => {
    expect(isValidHHMM('00:00')).toBe(true)
    expect(isValidHHMM('23:59')).toBe(true)
    expect(isValidHHMM('')).toBe(true) // unset allowed
  })
  it('invalid', () => {
    expect(isValidHHMM('24:00')).toBe(false)
    expect(isValidHHMM('9:00')).toBe(false)
    expect(isValidHHMM('12:60')).toBe(false)
  })
})

describe('parseChannelIds', () => {
  it('parses array', () => {
    expect(parseChannelIds('[1,2,3]')).toEqual([1, 2, 3])
  })
  it('tolerates empty / null / garbage', () => {
    expect(parseChannelIds('')).toEqual([])
    expect(parseChannelIds(null)).toEqual([])
    expect(parseChannelIds('not json')).toEqual([])
    expect(parseChannelIds('{"a":1}')).toEqual([])
  })
})
