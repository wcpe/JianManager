import { describe, expect, it } from 'vitest'
import { FIELD_SPECS, fieldSpecOf, isEffectiveImmediately, usesCustomEditor } from './field-types'

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
