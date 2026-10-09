import { describe, it, expect } from 'vitest'
import { levelBadgeClass, levelStatusLevel, triggerUsesMetric, triggerUsesKeyword, triggerUsesEventMatch, channelUsesURL, channelIsTelegram, channelIsEmail, channelIsInApp, channelIsQQ, isQQTargetType, isEnvRef, formatSilenceWindow, isValidHHMM, parseChannelIds, summarizeRules, targetTypeForTrigger, triggerAllowsTargetSwitch } from '@/lib/alert-helpers'

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
  it('maps trigger target type', () => {
    expect(targetTypeForTrigger('metric')).toBe('node')
    expect(targetTypeForTrigger('node_offline')).toBe('node')
    expect(targetTypeForTrigger('instance_crash')).toBe('instance')
    expect(targetTypeForTrigger('log_keyword')).toBe('instance')
    expect(targetTypeForTrigger('player_event')).toBe('instance')
    expect(targetTypeForTrigger('backup_failed')).toBe('instance')
  })

  it('只有 metric 允许切换目标维度', () => {
    // metric 在 node/instance 两个维度都有评估器，故两种目标都合法、可切换。
    expect(triggerAllowsTargetSwitch('metric')).toBe(true)
    // node_offline 的判定只在节点维度，锁定 node 不提供切换。
    expect(triggerAllowsTargetSwitch('node_offline')).toBe(false)
    expect(triggerAllowsTargetSwitch('instance_crash')).toBe(false)
    expect(triggerAllowsTargetSwitch('baseline')).toBe(false)
  })
})

describe('channel field visibility', () => {
  it('url channels', () => {
    for (const t of ['webhook', 'dingtalk', 'wecom', 'feishu', 'discord']) {
      expect(channelUsesURL(t)).toBe(true)
    }
    expect(channelUsesURL('telegram')).toBe(false)
    expect(channelUsesURL('email')).toBe(false)
    // QQ 的 baseUrl 是可选 API 根地址，不是凭证，故不得走 ${ENV} 强约束的 URL 字段。
    expect(channelUsesURL('qq')).toBe(false)
  })
  it('telegram / email / inapp', () => {
    expect(channelIsTelegram('telegram')).toBe(true)
    expect(channelIsEmail('email')).toBe(true)
    expect(channelIsInApp('inapp')).toBe(true)
    expect(channelIsInApp('webhook')).toBe(false)
  })
  it('qq only for qq', () => {
    expect(channelIsQQ('qq')).toBe(true)
    expect(channelIsQQ('telegram')).toBe(false)
    expect(channelIsQQ('webhook')).toBe(false)
    expect(channelIsQQ('inapp')).toBe(false)
  })
  it('qq target type 只认 c2c（group 已随群聊路径下线）', () => {
    expect(isQQTargetType('c2c')).toBe(true)
    // 群主动消息被平台拒绝（40034105），存量 group 值不再被承认——它会在保存前被必填校验拦下。
    expect(isQQTargetType('group')).toBe(false)
    expect(isQQTargetType('')).toBe(false)
    expect(isQQTargetType('GROUP')).toBe(false)
    expect(isQQTargetType('channel')).toBe(false)
  })
})

describe('isEnvRef', () => {
  it('接受 ${VAR}', () => {
    expect(isEnvRef('${JM_WEBHOOK}')).toBe(true)
    expect(isEnvRef('  ${A_B_1}  ')).toBe(true)
  })
  it('接受连字符（扫码绑定返回的 ${QQ-102000001} 必须能通过）', () => {
    // 后端把 ${ENV} 的合法字符集放宽为允许连字符，引用名由 appId 直接拼出；
    // 旧正则不含 `-` 会把这个合法引用判为非法，导致扫码成功后表单永远存不了。
    expect(isEnvRef('${QQ-102000001}')).toBe(true)
    expect(isEnvRef('  ${QQ-102000001}  ')).toBe(true)
    expect(isEnvRef('${A-B_C-D1}')).toBe(true)
  })
  it('拒绝真正的非法值（放宽字符集不等于放开一切）', () => {
    expect(isEnvRef('invalid')).toBe(false)
    expect(isEnvRef('${}')).toBe(false)
    expect(isEnvRef('${1bad}')).toBe(false)
    expect(isEnvRef('${-lead}')).toBe(false)
    expect(isEnvRef('${QQ-102000001')).toBe(false)
    expect(isEnvRef('QQ-102000001}')).toBe(false)
    expect(isEnvRef('${QQ-102 000001}')).toBe(false)
    expect(isEnvRef('${QQ-102000001}extra')).toBe(false)
    expect(isEnvRef('https://x.com')).toBe(false)
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
