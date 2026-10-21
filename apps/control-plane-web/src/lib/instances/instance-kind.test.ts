/**
 * @file 实例类别归一单测（FR-511）。
 *
 * 关键点：前端回退推断必须与后端 `model.Instance.KindOrInfer` **同口径**。
 * 两条链各写一份推断时，最容易出现「后端说插件服、前端说二进制」的错位——
 * 那会让同一条数据在侧栏与详情页显示不同类型。
 */
import { describe, it, expect } from 'vitest'

import { instanceKindOf, isValidKind } from './instance-kind'

describe('isValidKind', () => {
  it('只认三类', () => {
    expect(isValidKind('plugin')).toBe(true)
    expect(isValidKind('proxy')).toBe(true)
    expect(isValidKind('binary')).toBe(true)
  })

  it('空值/脏值/大小写不符一律不认', () => {
    expect(isValidKind(undefined)).toBe(false)
    expect(isValidKind(null)).toBe(false)
    expect(isValidKind('')).toBe(false)
    expect(isValidKind('PAPER')).toBe(false)
    expect(isValidKind('unknown')).toBe(false)
  })
})

describe('instanceKindOf', () => {
  it('显式 kind 优先，不做推断', () => {
    expect(instanceKindOf({ kind: 'binary', role: 'backend', type: 'minecraft_java' })).toBe('binary')
  })

  it('旧数据（无 kind）按 role 回退：proxy → 代理', () => {
    expect(instanceKindOf({ role: 'proxy', type: 'minecraft_java' })).toBe('proxy')
  })

  it('旧数据按 type 回退：generic → 二进制', () => {
    expect(instanceKindOf({ role: 'universal', type: 'generic' })).toBe('binary')
    // beacon 角色配 generic 类型同样是二进制（不是 MC Java 进程）
    expect(instanceKindOf({ role: 'beacon', type: 'generic' })).toBe('binary')
  })

  it('其余 MC 服务端 → 插件服', () => {
    expect(instanceKindOf({ role: 'backend', type: 'minecraft_java' })).toBe('plugin')
  })

  it('非法 kind 值走回退，而不是原样透出', () => {
    expect(instanceKindOf({ kind: 'garbage', role: 'proxy', type: 'minecraft_java' })).toBe('proxy')
  })
})
