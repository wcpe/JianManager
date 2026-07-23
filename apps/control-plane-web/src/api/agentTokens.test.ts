import { describe, it, expect } from 'vitest'
import {
  parseUintList,
  parseStringList,
  normalizeAgentToken,
  agentTokenStatus,
  type AgentTokenRaw,
} from './agentTokens'

describe('agentTokens 解析与状态', () => {
  it('parseUintList：JSON 文本 / 数组 / 空', () => {
    expect(parseUintList('[1,2,3]')).toEqual([1, 2, 3])
    expect(parseUintList([4, 5])).toEqual([4, 5])
    expect(parseUintList('[]')).toEqual([])
    expect(parseUintList(null)).toEqual([])
    expect(parseUintList('not-json')).toEqual([])
    expect(parseUintList([0, -1, 2.5, 3])).toEqual([3])
  })

  it('parseStringList：JSON 文本 / 数组', () => {
    expect(parseStringList('["instance.life","node.maintenance"]')).toEqual([
      'instance.life',
      'node.maintenance',
    ])
    expect(parseStringList(['a', 'b'])).toEqual(['a', 'b'])
    expect(parseStringList('')).toEqual([])
  })

  it('normalizeAgentToken 规范化后端 JSON 字符串字段', () => {
    const raw: AgentTokenRaw = {
      id: 1,
      name: 'ci',
      tokenPrefix: 'jmat_ab12',
      scopedInstanceIds: '[1,2]',
      scopedNodeIds: '[]',
      writeAllowlist: '["instance.life"]',
      expiresAt: '2099-01-01T00:00:00Z',
      revoked: false,
      createdAt: '2026-01-01T00:00:00Z',
      createdBy: 1,
    }
    const tok = normalizeAgentToken(raw)
    expect(tok.scopedInstanceIds).toEqual([1, 2])
    expect(tok.scopedNodeIds).toEqual([])
    expect(tok.writeAllowlist).toEqual(['instance.life'])
  })

  it('agentTokenStatus：active / expired / revoked', () => {
    const now = Date.parse('2026-07-01T00:00:00Z')
    expect(
      agentTokenStatus({ revoked: false, expiresAt: '2099-01-01T00:00:00Z' }, now),
    ).toBe('active')
    expect(
      agentTokenStatus({ revoked: false, expiresAt: '2020-01-01T00:00:00Z' }, now),
    ).toBe('expired')
    expect(
      agentTokenStatus({ revoked: true, expiresAt: '2099-01-01T00:00:00Z' }, now),
    ).toBe('revoked')
  })
})
