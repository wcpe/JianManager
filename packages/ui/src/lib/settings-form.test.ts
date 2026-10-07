import { describe, it, expect } from 'vitest'
import {
  diffSettings,
  hasUnsavedChanges,
  keyCategory,
  validateSettingDraft,
  hasInvalidDraft,
  MAX_DIRECT_PROBE_TIMEOUT_MS,
  type DraftDiffItem,
} from './settings-form'

const items: DraftDiffItem[] = [
  { key: 'log.level', value: 'info' },
  { key: 'backup.retention_days', value: '7' },
]

describe('diffSettings', () => {
  it('returns no changes for an empty draft', () => {
    expect(diffSettings(items, {})).toEqual({})
  })

  it('ignores a draft value equal to the current value', () => {
    expect(diffSettings(items, { 'log.level': 'info' })).toEqual({})
  })

  it('keeps only draft values that differ from current', () => {
    expect(diffSettings(items, { 'log.level': 'warn', 'backup.retention_days': '7' })).toEqual({
      'log.level': 'warn',
    })
  })

  it('ignores draft keys not present in the editable item set', () => {
    expect(diffSettings(items, { 'unknown.key': 'x' })).toEqual({})
  })

  it('ignores undefined draft entries', () => {
    expect(diffSettings(items, { 'log.level': undefined as unknown as string })).toEqual({})
  })
})

describe('hasUnsavedChanges', () => {
  it('is false when nothing differs', () => {
    expect(hasUnsavedChanges(items, {})).toBe(false)
    expect(hasUnsavedChanges(items, { 'log.level': 'info' })).toBe(false)
  })

  it('is true when at least one value differs', () => {
    expect(hasUnsavedChanges(items, { 'log.level': 'debug' })).toBe(true)
  })
})

describe('validateSettingDraft（与后端 validateSettingValue 规则一致）', () => {
  const cases: Array<[key: string, value: string, ok: boolean]> = [
    // graceful_stop.timeout：正 Go duration（支持 1h30m 多段组合）
    ['graceful_stop.timeout', '30s', true],
    ['graceful_stop.timeout', '1h30m', true],
    ['graceful_stop.timeout', '0.5s', true],
    ['graceful_stop.timeout', 'abc', false],
    ['graceful_stop.timeout', '30', false],
    ['graceful_stop.timeout', '0s', false],
    ['graceful_stop.timeout', '-5s', false],
    // direct_probe.*（FR-446 MC 直探超时）：同 Go duration 规则，另限上界（= directprobe.MaxTimeout，10s）
    ['direct_probe.slp_timeout', '3s', true],
    ['direct_probe.slp_timeout', '1500ms', true],
    ['direct_probe.slp_timeout', '10s', true],
    ['direct_probe.query_timeout', '2s', true],
    ['direct_probe.query_timeout', '10s', true],
    ['direct_probe.query_timeout', 'abc', false],
    ['direct_probe.query_timeout', '0s', false],
    ['direct_probe.query_timeout', '11s', false],
    ['direct_probe.query_timeout', '10001ms', false],
    ['direct_probe.query_timeout', '1m', false],
    // backup.retention_days：非负整数
    ['backup.retention_days', '0', true],
    ['backup.retention_days', '30', true],
    ['backup.retention_days', 'abc', false],
    ['backup.retention_days', '-1', false],
    ['backup.retention_days', '1.5', false],
    // 镜像源：非空
    ['jdk.mirror.temurin', 'https://mirror.example.com', true],
    ['jdk.mirror.temurin', '', false],
    // proxy.url：空=清除覆盖合法；非空须为受支持 scheme 的合法 URL
    ['proxy.url', '', true],
    ['proxy.url', 'http://127.0.0.1:7890', true],
    ['proxy.url', 'socks5://127.0.0.1:1080', true],
    ['proxy.url', 'not-a-url', false],
    ['proxy.url', 'ftp://x', false],
    // 未纳管键不校验
    ['some.other.key', 'anything', true],
  ]
  for (const [key, value, ok] of cases) {
    it(`${key}=${JSON.stringify(value)} → ${ok ? '合法' : '非法'}`, () => {
      const err = validateSettingDraft(key, value)
      if (ok) expect(err).toBeUndefined()
      else expect(err).toBeTruthy()
    })
  }
})

describe('hasInvalidDraft', () => {
  it('草稿非法即 true', () => {
    expect(hasInvalidDraft(items, { 'backup.retention_days': 'abc' })).toBe(true)
  })
  it('草稿缺省回落当前值（合法）为 false', () => {
    expect(hasInvalidDraft(items, {})).toBe(false)
  })
})

describe('MAX_DIRECT_PROBE_TIMEOUT_MS（与后端 directprobe.MaxTimeout 同值，10s）', () => {
  it('上界为 10s，且边界恰好可接受、多 1ms 即拒绝', () => {
    // 由「单拍采集预算 < 30s 心跳节拍」反推：余量 1s + 探针 5s + slp + query ≤ 30s − 4s。
    expect(MAX_DIRECT_PROBE_TIMEOUT_MS).toBe(10_000)
    expect(validateSettingDraft('direct_probe.slp_timeout', '10s')).toBeUndefined()
    expect(validateSettingDraft('direct_probe.slp_timeout', '10000ms')).toBeUndefined()
    expect(validateSettingDraft('direct_probe.slp_timeout', '10001ms')).toBe(
      'settings.invalidDirectProbeTimeout',
    )
    expect(validateSettingDraft('direct_probe.query_timeout', '10.001s')).toBe(
      'settings.invalidDirectProbeTimeout',
    )
  })
})

describe('keyCategory（github.token 归网络类，与出站代理同面板）', () => {
  it('github.token 落 network，其余前缀规则不变', () => {
    expect(keyCategory('github.token')).toBe('network')
    expect(keyCategory('proxy.url')).toBe('network')
    expect(keyCategory('log.level')).toBe('logging')
    expect(keyCategory('invite.smtp.password')).toBe('email')
    expect(keyCategory('jwt.secret')).toBe('security')
  })
})
