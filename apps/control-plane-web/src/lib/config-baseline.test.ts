import { describe, expect, it } from 'vitest'
import {
  composeScopeKey,
  isValidScopeKey,
  scopeKindOf,
  scopeValueOf,
  shortHash,
} from './config-baseline'

describe('config-baseline scope 纯函数（FR-458）', () => {
  it('composeScopeKey：all 无取值，其余拼 kind:value', () => {
    expect(composeScopeKey('all', 'ignored')).toBe('all')
    expect(composeScopeKey('group', ' 2 ')).toBe('group:2')
    expect(composeScopeKey('tag', 'prod')).toBe('tag:prod')
  })

  it('scopeKindOf / scopeValueOf：解析与回退', () => {
    expect(scopeKindOf('group:2')).toBe('group')
    expect(scopeKindOf('network:5')).toBe('network')
    expect(scopeKindOf('tag:prod')).toBe('tag')
    expect(scopeKindOf('instance:9')).toBe('instance')
    expect(scopeKindOf('all')).toBe('all')
    expect(scopeKindOf('')).toBe('all')
    expect(scopeKindOf('bogus:1')).toBe('all')

    expect(scopeValueOf('group:2')).toBe('2')
    expect(scopeValueOf('tag:prod')).toBe('prod')
    expect(scopeValueOf('all')).toBe('')
  })

  it('isValidScopeKey：all 合法；group/network/instance 须正整数；tag 非空', () => {
    expect(isValidScopeKey('all')).toBe(true)
    expect(isValidScopeKey('group:2')).toBe(true)
    expect(isValidScopeKey('network:5')).toBe(true)
    expect(isValidScopeKey('instance:9')).toBe(true)
    expect(isValidScopeKey('tag:prod')).toBe(true)

    expect(isValidScopeKey('')).toBe(false)
    expect(isValidScopeKey('group:')).toBe(false)
    expect(isValidScopeKey('group:0')).toBe(false)
    expect(isValidScopeKey('group:-1')).toBe(false)
    expect(isValidScopeKey('group:abc')).toBe(false)
    expect(isValidScopeKey('tag:')).toBe(false)
    expect(isValidScopeKey('bogus:1')).toBe(false)
  })

  it('shortHash：短哈希原样、长哈希截断', () => {
    expect(shortHash('abc')).toBe('abc')
    expect(shortHash('a'.repeat(64))).toBe('a'.repeat(12))
    expect(shortHash('a'.repeat(64), 8)).toBe('a'.repeat(8))
  })
})
