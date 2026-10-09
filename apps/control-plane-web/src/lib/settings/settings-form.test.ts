/*
 * 用 node:fs 直接读仓库内后端源码来钉跨层契约。`@ts-expect-error` 是必需的：packages/ui 的
 * tsconfig `types` 只声明 vite/client（不含 node），而本文件由 vitest 的 node project 执行、
 * 确实能加载 node 内置模块。与 `color-contrast.test.ts` 同一处置，不为此给组件包补 node 类型依赖。
 * 读源码而非断言硬编码清单，是为了让后端新增可编辑键时这里立刻变红，而不是等界面缺项被发现。
 */
// @ts-expect-error 见上方说明：node 内置模块类型不在本包 tsconfig 的 types 列表内
import { readFileSync } from 'node:fs'
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

// 后端 settings.go 的 editable 数组是「界面上应当可编辑」的真源，这里把契约钉住。
// 曾出现：运行时策略类新键未同步 keyCategory 的前缀族，兜底落进 security，
// 而 security 分区只渲染只读行 —— 23 个可编辑键在界面上完全不可见（含 runtime.mirror.nodejs、
// health.*、snapshot.*、quota.*、instance_reverse_reconcile.*、bot_reclaim.*、crash.*）。
describe('keyCategory 与后端可编辑键的契约', () => {
  // 敏感项走专用构造器（proxyURLItem / inviteSMTPPasswordItem / githubTokenItem），
  // 不是 editableItem，正则抓不到，故显式列出。
  const SPECIAL_EDITABLE = ['proxy.url', 'invite.smtp.password', 'github.token']

  const loadEditableKeys = (): string[] => {
    // 五级 ../ 到仓库根（本文件在 apps/control-plane-web/src/lib/settings/）。
    // 曾写四级：解析到 apps/internal/...（不存在）→ 被 catch 吞掉 → `if (!keys) return` 跳过，
    // 于是本用例永久绿，它要防的事故（23 个可编辑键落进只读 security 分区而界面不可见）原样复现也无人报警。
    // 故此处不再静默跳过：读不到就让用例变红，路径被移动时必须立刻暴露。
    const backendSource = new URL(
      '../../../../../internal/controlplane/service/settings.go',
      import.meta.url,
    )
    let src: string
    try {
      src = readFileSync(backendSource, 'utf8')
    } catch (err) {
      throw new Error(`读不到后端 settings.go（路径失效？）：${backendSource.pathname}`, {
        cause: err,
      })
    }
    // 常量名 → 键字符串
    const constToKey = new Map<string, string>()
    for (const m of src.matchAll(/^\s*(SettingKey\w+)\s*=\s*"([^"]+)"/gm)) {
      constToKey.set(m[1], m[2])
    }
    expect(constToKey.size, '未能解析出 SettingKey 常量').toBeGreaterThan(0)

    const block = src.match(/editable\s*:?=\s*\[\]SettingItem\{([\s\S]*?)\n\t\}/)
    expect(block, '未能在 settings.go 中定位 editable 数组').toBeTruthy()
    const keys = [...block![1].matchAll(/s\.editableItem\((SettingKey\w+)/g)].map((m) =>
      constToKey.get(m[1]),
    )
    expect(keys.every(Boolean), '有 editableItem 引用了未解析到的常量').toBe(true)
    return [...(keys as string[]), ...SPECIAL_EDITABLE]
  }

  it('后端标记可编辑的键都不落进只读的 security 分区', () => {
    const keys = loadEditableKeys()
    // 后端当前 38 项可编辑；低于 30 说明解析失效，别让用例假绿
    expect(keys.length).toBeGreaterThanOrEqual(30)
    const invisible = keys.filter((k) => keyCategory(k) === 'security')
    expect(
      invisible,
      `这些键后端允许编辑，但会落进只读的 security 分区而不可见：${invisible.join(', ')}`,
    ).toEqual([])
  })

  it('新增键族各自归入预期分类', () => {
    expect(keyCategory('health.scan_interval')).toBe('policy')
    expect(keyCategory('quota.enforce_mode')).toBe('policy')
    expect(keyCategory('crash.stat_retention_days')).toBe('policy')
    expect(keyCategory('instance_reverse_reconcile.grace_period')).toBe('policy')
    expect(keyCategory('bot_reclaim.auto_reclaim')).toBe('policy')
    expect(keyCategory('snapshot.retention_days')).toBe('backup')
    expect(keyCategory('runtime.mirror.nodejs')).toBe('runtime')
    // 只读键仍须归 security，否则会把不可编辑项渲染成输入框
    expect(keyCategory('server.host')).toBe('security')
  })
})
