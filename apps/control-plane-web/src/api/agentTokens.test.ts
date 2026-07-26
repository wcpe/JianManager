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

  it('normalizeAgentToken 规范化后端 JSON 字符串字段（V1 缺 policyVersion）', () => {
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
    expect(tok.policyVersion).toBe(1)
    expect(tok.capabilities).toEqual([])
  })

  it('normalizeAgentToken：V2 能力数组 / JSON 文本 / 空数组 / null', () => {
    const base = {
      id: 2,
      name: 'v2',
      tokenPrefix: 'jmat_v2',
      scopedInstanceIds: [] as number[],
      scopedNodeIds: [1],
      writeAllowlist: [] as string[],
      expiresAt: '2099-01-01T00:00:00Z',
      revoked: false,
      createdAt: '2026-07-26T00:00:00Z',
      createdBy: 1,
      policyVersion: 2,
    }
    expect(
      normalizeAgentToken({ ...base, capabilities: ['instance.read', 'node.read'] }).capabilities,
    ).toEqual(['instance.read', 'node.read'])
    expect(
      normalizeAgentToken({ ...base, capabilities: '["instance.life"]' }).capabilities,
    ).toEqual(['instance.life'])
    expect(normalizeAgentToken({ ...base, capabilities: [] }).capabilities).toEqual([])
    expect(normalizeAgentToken({ ...base, capabilities: null }).capabilities).toEqual([])
    expect(normalizeAgentToken({ ...base, policyVersion: 2 }).policyVersion).toBe(2)
  })

  it('normalizeAgentToken：非法 policyVersion 不按 V2 展示', () => {
    const raw: AgentTokenRaw = {
      id: 3,
      name: 'bad',
      tokenPrefix: 'jmat_x',
      scopedInstanceIds: [],
      scopedNodeIds: [],
      writeAllowlist: [],
      expiresAt: '2099-01-01T00:00:00Z',
      revoked: false,
      createdAt: '2026-07-26T00:00:00Z',
      createdBy: 1,
      policyVersion: 99,
      capabilities: ['instance.read'],
    }
    const tok = normalizeAgentToken(raw)
    expect(tok.policyVersion).toBe(1)
    expect(tok.capabilities).toEqual([])
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
