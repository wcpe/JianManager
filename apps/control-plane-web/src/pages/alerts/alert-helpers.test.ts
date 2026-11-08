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
  channelIsQQ,
  isQQTargetType,
  qqGatewayLevel,
  qqGatewayStatusKey,
  qqGatewayFatalHintKey,
  shortGroupOpenid,
  isEnvRef,
  formatSilenceWindow,
  isValidHHMM,
  parseChannelIds,
  summarizeRules,
  targetTypeForTrigger,
  triggerAllowsTargetSwitch,
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

/**
 * QQ 接入状态的展示映射（FR-495 只读观测区）。
 *
 * 这层纯函数的意义就是「后端串 → 能展示的东西」：状态串来自 DB/内存，任何历史值或未来
 * 新态都不能变成缺失 i18n key 或假颜色（`disconnected` 染红会把正常待机报成故障）。
 */
describe('qqGatewayLevel', () => {
  it('四态各自对应状态等级', () => {
    expect(qqGatewayLevel('connected')).toBe('success')
    expect(qqGatewayLevel('connecting')).toBe('warning')
    expect(qqGatewayLevel('error')).toBe('danger')
    // disconnected = 未配置/正常待机，不是故障；中性色才不会制造假告警。
    expect(qqGatewayLevel('disconnected')).toBe('neutral')
  })

  it('未知/空值一律中性（不因脏串报错）', () => {
    expect(qqGatewayLevel('weird')).toBe('neutral')
    expect(qqGatewayLevel(undefined)).toBe('neutral')
    expect(qqGatewayLevel('')).toBe('neutral')
  })
})

describe('qqGatewayStatusKey', () => {
  it('已知四态原样返回', () => {
    expect(qqGatewayStatusKey('connected')).toBe('connected')
    expect(qqGatewayStatusKey('connecting')).toBe('connecting')
    expect(qqGatewayStatusKey('disconnected')).toBe('disconnected')
    expect(qqGatewayStatusKey('error')).toBe('error')
  })

  it('未知值回落 unknown（不拼出缺失的 i18n key）', () => {
    expect(qqGatewayStatusKey('banned')).toBe('unknown')
    expect(qqGatewayStatusKey(undefined)).toBe('unknown')
  })
})

describe('qqGatewayFatalHintKey', () => {
  it('4014 三种写法都能识别（关闭码 / intent 字面量 / 官方常量名）', () => {
    expect(qqGatewayFatalHintKey('QQ 机器人缺少 1<<25（GROUP_AND_C2C_EVENT）intent 权限')).toBe(
      'alerts.qqGatewayHint4014',
    )
    expect(qqGatewayFatalHintKey('close 4014')).toBe('alerts.qqGatewayHint4014')
    expect(qqGatewayFatalHintKey('intent GROUP_AND_C2C_EVENT 未授权')).toBe('alerts.qqGatewayHint4014')
  })

  it('其他错误 / 空值不给提示（没有自己能动手修好的动作）', () => {
    expect(qqGatewayFatalHintKey('QQ 机器人已下架（4914），需人工处理')).toBeNull()
    expect(qqGatewayFatalHintKey('未配置启用的 QQ 通知通道，请先创建 qq 通道')).toBeNull()
    expect(qqGatewayFatalHintKey('')).toBeNull()
    expect(qqGatewayFatalHintKey(undefined)).toBeNull()
    expect(qqGatewayFatalHintKey('   ')).toBeNull()
  })
})

describe('shortGroupOpenid', () => {
  it('超过 8 位截断并加省略号', () => {
    expect(shortGroupOpenid('GROUP_OPENID_DEMO_1')).toBe('GROUP_OP…')
  })

  it('不足/等于 8 位原样返回（不补位、不裁成空串）', () => {
    expect(shortGroupOpenid('ABCDEFGH')).toBe('ABCDEFGH')
    expect(shortGroupOpenid('ABC')).toBe('ABC')
    expect(shortGroupOpenid('')).toBe('')
  })

  it('两端空白先剔除再截断', () => {
    expect(shortGroupOpenid('  ABC  ')).toBe('ABC')
  })
})
