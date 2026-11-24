import { describe, expect, it } from 'vitest'
import { FIELD_SPECS, effectTimingOf, fieldSpecOf, isEffectiveImmediately, usesCustomEditor } from './field-types'

describe('fieldSpecOf', () => {
  it('已登记的字段返回声明的类型', () => {
    expect(fieldSpecOf('debug.mode').type).toBe('switch')
    expect(fieldSpecOf('log.level').type).toBe('select')
    expect(fieldSpecOf('github.token').type).toBe('secret')
    expect(fieldSpecOf('backup.retention_days').type).toBe('numeric')
    expect(fieldSpecOf('log.acq.path_overrides').type).toBe('kv-pairs')
  })

  it('未登记的字段回落 text（声明表可逐步补全，不必一次穷举）', () => {
    expect(fieldSpecOf('some.brand.new.key').type).toBe('text')
  })

  it('path_overrides 必须是 kv-pairs——这正是"不要让我输 JSON"的那个字段', () => {
    expect(fieldSpecOf('log.acq.path_overrides').type).toBe('kv-pairs')
  })

  it('select 类型必须带 options（否则渲染出空下拉）', () => {
    const level = fieldSpecOf('log.level')
    expect(level.options?.length ?? 0).toBeGreaterThan(0)
  })
})

describe('isEffectiveImmediately', () => {
  it('开关与日志级别是即时生效', () => {
    expect(isEffectiveImmediately('debug.mode')).toBe(true)
    expect(isEffectiveImmediately('log.level')).toBe(true)
  })

  it('备份保留天数与路径覆盖不是即时生效（需 Worker 侧应用）', () => {
    expect(isEffectiveImmediately('backup.retention_days')).toBe(false)
    expect(isEffectiveImmediately('log.acq.path_overrides')).toBe(false)
  })

  it('未登记字段默认不标"即时生效"（宁可少标，不可误导）', () => {
    expect(isEffectiveImmediately('mystery.key')).toBe(false)
  })
})

describe('effectTimingOf（三态）', () => {
  it('登记为互即时的字段返回 immediate', () => {
    expect(effectTimingOf('debug.mode')).toBe('immediate')
    expect(effectTimingOf('log.level')).toBe('immediate')
  })

  it('登记为非即时的字段返回 worker', () => {
    expect(effectTimingOf('backup.retention_days')).toBe('worker')
    expect(effectTimingOf('log.acq.path_overrides')).toBe('worker')
  })

  /*
    这是本组最重要的一条：未登记 spec 的字段必须是 unknown，**不能默认成 worker**。
    真实反例：traffic.rate_limit.* 是即时生效的，但它没有登记 spec；若默认 worker，
    界面就会给它打「Worker 侧生效」，用户会去重启 Worker 找一个不存在的开关。
  */
  it('未登记字段返回 unknown，而不是默认 worker（标错比不标更糟）', () => {
    expect(effectTimingOf('traffic.rate_limit.qps')).toBe('unknown')
    expect(effectTimingOf('mystery.key')).toBe('unknown')
  })

  it('登记了 spec 但没声明 effectiveImmediately 的字段也是 unknown', () => {
    // 表里没有任何这种字段，用兜底断言防止将来悄悄加一个"忘了声明"的条目。
    for (const [key, spec] of Object.entries(FIELD_SPECS)) {
      if (spec.effectiveImmediately === undefined) {
        expect(effectTimingOf(key), `${key} 未声明 effectiveImmediately`).toBe('unknown')
      }
    }
  })
})

describe('usesCustomEditor', () => {
  it('kv-pairs 由自定义编辑器接管（草稿是结构化数据，不能按字符串比较脏）', () => {
    expect(usesCustomEditor('log.acq.path_overrides')).toBe(true)
  })

  it('普通字段不使用自定义编辑器', () => {
    expect(usesCustomEditor('debug.mode')).toBe(false)
    expect(usesCustomEditor('log.level')).toBe(false)
    expect(usesCustomEditor('unknown.key')).toBe(false)
  })
})

describe('FIELD_SPECS 表本身', () => {
  it('每个 select 项都带非空 options（表的完整性）', () => {
    for (const [key, spec] of Object.entries(FIELD_SPECS)) {
      if (spec.type === 'select') {
        expect(spec.options?.length ?? 0, `${key} 是 select 却没有 options`).toBeGreaterThan(0)
      }
    }
  })

  it('每个 switch 项若给了文案就两边都给（只给一边会让界面出现"开启/关闭"混搭）', () => {
    for (const [key, spec] of Object.entries(FIELD_SPECS)) {
      if (spec.type === 'switch') {
        const hasOn = spec.onLabel !== undefined
        const hasOff = spec.offLabel !== undefined
        expect(hasOn, `${key} 的 onLabel/offLabel 须成对出现`).toBe(hasOff)
      }
    }
  })
})

/**
 * 声明表必须覆盖"界面上已经存在"的结构化字段。
 *
 * 【这条守的是一个真实退化】采集面板原用局部 `BOOLEAN_KEYS` 集合决定布尔用 Select 渲染；
 * 改用通用组件后集合被删，而声明表没补上这三个 key → 它们退化成普通文本框，
 * 用户对 "true"/"false" 可以随便敲，写错要等后端 422 才知道。
 * 静态表与"曾是布尔"的字段之间不该有这种情况。
 */
describe('日志采集字段的渲染声明', () => {
  it('三个采集开关都声明为 switch（不是退化成 text）', () => {
    for (const key of [
      'log.acq.rules_enabled',
      'log.acq.realtime_tail',
      'log.acq.gz_import_enabled',
    ]) {
      expect(fieldSpecOf(key).type, `${key} 应为 switch`).toBe('switch')
    }
  })

  it('path_overrides 是 kv-pairs（成对行编辑，不是 JSON 文本框）', () => {
    expect(fieldSpecOf('log.acq.path_overrides').type).toBe('kv-pairs')
  })
})
