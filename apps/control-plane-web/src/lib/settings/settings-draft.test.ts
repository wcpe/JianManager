import { describe, expect, it } from 'vitest'
import {
  buildSubmitValues,
  clearSubmitted,
  emptyDraft,
  hasChanges,
  shownText,
  type KvPairRow,
  type SettingItem,
  type SettingsDraft,
} from './settings-draft'

const MOCK_KEY = 'debug.mode'
const LEVEL_KEY = 'log.level'
const PATH_KEY = 'log.acq.path_overrides'

const item = (over: Partial<SettingItem> & Pick<SettingItem, 'key'>): SettingItem => ({
  platform: '',
  override: '',
  overridden: false,
  ...over,
})

/** 模拟 kv-pairs 的序列化（真实实现见 lib/settings/path-overrides.ts）。 */
const serializeKv = (rows: readonly KvPairRow[]): string => {
  const filled = rows.filter((r) => r.key.trim() && r.value.trim())
  if (filled.length === 0) return ''
  return JSON.stringify(Object.fromEntries(filled.map((r) => [r.key, r.value])))
}

const items: SettingItem[] = [
  item({ key: MOCK_KEY, platform: 'false' }),
  item({ key: LEVEL_KEY, platform: 'info' }),
  item({ key: PATH_KEY, platform: '' }),
]

describe('shownText', () => {
  it('无草稿时回落平台值', () => {
    expect(shownText(items[0] as SettingItem, emptyDraft())).toBe('false')
  })

  it('有覆盖值时优先覆盖值', () => {
    const it = item({ key: MOCK_KEY, platform: 'false', override: 'true', overridden: true })
    expect(shownText(it, emptyDraft())).toBe('true')
  })

  it('草稿优先于一切', () => {
    const d: SettingsDraft = { text: { [MOCK_KEY]: 'draft' }, kv: {} }
    expect(shownText(items[0] as SettingItem, d)).toBe('draft')
  })
})

describe('buildSubmitValues', () => {
  it('只提交真正改动的键（未改的不提交，避免把"继承平台"固化成覆盖）', () => {
    const d: SettingsDraft = { text: { [LEVEL_KEY]: 'debug' }, kv: {} }
    expect(buildSubmitValues(items, d, serializeKv)).toEqual({ [LEVEL_KEY]: 'debug' })
  })

  it('改回原值等于没改', () => {
    const d: SettingsDraft = { text: { [LEVEL_KEY]: 'info' }, kv: {} }
    expect(buildSubmitValues(items, d, serializeKv)).toEqual({})
  })

  it('没有任何草稿时提交集为空', () => {
    expect(buildSubmitValues(items, emptyDraft(), serializeKv)).toEqual({})
  })

  /*
    ↓↓↓ 这组是之前集成失败的根因场景，必须在纯函数层就钉死 ↓↓↓
  */

  it('半填的 kv 行不算改动（不能因为"正在编辑"就提交一条空配置）', () => {
    const d: SettingsDraft = {
      text: {},
      kv: { [PATH_KEY]: [{ key: 'inst:1', value: '' }] }, // 只填了实例没填路径
    }
    expect(buildSubmitValues(items, d, serializeKv)).toEqual({})
  })

  it('刚点「添加」的完全空行也不算改动', () => {
    const d: SettingsDraft = { text: {}, kv: { [PATH_KEY]: [{ key: '', value: '' }] } }
    expect(buildSubmitValues(items, d, serializeKv)).toEqual({})
  })

  it('填完整的 kv 行会序列化成 JSON 提交', () => {
    const d: SettingsDraft = {
      text: {},
      kv: { [PATH_KEY]: [{ key: 'inst:1', value: 'logs/a.log' }] },
    }
    expect(buildSubmitValues(items, d, serializeKv)).toEqual({
      [PATH_KEY]: '{"inst:1":"logs/a.log"}',
    })
  })

  it('字符串字段与 kv 字段可以同时提交（互不干扰）', () => {
    const d: SettingsDraft = {
      text: { [LEVEL_KEY]: 'debug' },
      kv: { [PATH_KEY]: [{ key: 'inst:1', value: 'a.log' }] },
    }
    expect(buildSubmitValues(items, d, serializeKv)).toEqual({
      [LEVEL_KEY]: 'debug',
      [PATH_KEY]: '{"inst:1":"a.log"}',
    })
  })
})

describe('hasChanges', () => {
  it('有改动返回 true', () => {
    expect(hasChanges(items, { text: { [LEVEL_KEY]: 'debug' }, kv: {} }, serializeKv)).toBe(true)
  })

  it('只有半填 kv 行时返回 false（还没到能提交的状态）', () => {
    expect(
      hasChanges(items, { text: {}, kv: { [PATH_KEY]: [{ key: 'inst:1', value: '' }] } }, serializeKv),
    ).toBe(false)
  })
})

describe('clearSubmitted', () => {
  it('清除仍是提交值的键', () => {
    const d: SettingsDraft = { text: { [LEVEL_KEY]: 'debug' }, kv: {} }
    const next = clearSubmitted(d, { [LEVEL_KEY]: 'debug' }, items, serializeKv)
    expect(next.text[LEVEL_KEY]).toBeUndefined()
  })

  it('在途期间被再次改动的键**保留**（不能被整表清掉）', () => {
    const d: SettingsDraft = { text: { [LEVEL_KEY]: 'warn' }, kv: {} }
    const next = clearSubmitted(d, { [LEVEL_KEY]: 'debug' }, items, serializeKv)
    expect(next.text[LEVEL_KEY]).toBe('warn')
  })

  it('kv 字段：仅当当前序列化结果仍等于提交值时才清', () => {
    const rows: KvPairRow[] = [{ key: 'inst:1', value: 'a.log' }]
    const d: SettingsDraft = { text: {}, kv: { [PATH_KEY]: rows } }
    const next = clearSubmitted(d, { [PATH_KEY]: serializeKv(rows) }, items, serializeKv)
    expect(next.kv[PATH_KEY]).toBeUndefined()
  })

  it('kv 字段：在途期间又加了行则保留草稿', () => {
    const submitted: KvPairRow[] = [{ key: 'inst:1', value: 'a.log' }]
    const edited: KvPairRow[] = [...submitted, { key: 'inst:2', value: 'b.log' }]
    const d: SettingsDraft = { text: {}, kv: { [PATH_KEY]: edited } }
    const next = clearSubmitted(d, { [PATH_KEY]: serializeKv(submitted) }, items, serializeKv)
    expect(next.kv[PATH_KEY]).toHaveLength(2)
  })
})
