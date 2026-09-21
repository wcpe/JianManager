import { describe, it, expect } from 'vitest'

import {
  CAPABILITY_REGISTRY,
  UNIVERSAL_FALLBACK,
  capabilityProfileFor,
  hasCapability,
  resolveCapabilities,
  type InstanceCapabilityProfile,
} from './capabilities'

/**
 * FR-445 / ADR-091：前端能力画像兜底表与解析逻辑。
 *
 * 约定：本地兜底表与后端 `service/capability_profile.go` 的注册表一一对应；
 * 后端下发的 `capabilities` 优先，缺失时按 (type, role) 本地兜底。
 */
describe('capabilityProfileFor（本地兜底表）', () => {
  it('backend 画像 = 现有 9 Tab，具备 MC 世界语义', () => {
    const p = capabilityProfileFor('minecraft_java', 'backend')
    expect(p.mcSemantics).toBe(true)
    expect(p.capabilities).toEqual([
      'overview', 'terminal', 'files', 'plugins', 'metrics', 'players', 'business', 'bot', 'backup',
    ])
  })

  it('proxy 画像：有 bcTopology/process/health/config，无 metrics(TPS)/business/bot', () => {
    const p = capabilityProfileFor('minecraft_java', 'proxy')
    expect(p.mcSemantics).toBe(false)
    for (const cap of ['bcTopology', 'players', 'plugins', 'process', 'health', 'config'] as const) {
      expect(p.capabilities).toContain(cap)
    }
    for (const cap of ['metrics', 'business', 'bot'] as const) {
      expect(p.capabilities).not.toContain(cap)
    }
  })

  it('generic/beacon 与 generic/universal 无任何 MC 专有能力', () => {
    for (const role of ['beacon', 'universal'] as const) {
      const p = capabilityProfileFor('generic', role)
      expect(p.mcSemantics).toBe(false)
      expect(p.capabilities).toEqual(['overview', 'terminal', 'files', 'process', 'health', 'config', 'backup'])
      for (const cap of ['metrics', 'plugins', 'players', 'business', 'bot'] as const) {
        expect(p.capabilities).not.toContain(cap)
      }
    }
  })

  it('未知 (type, role) 回退安全子集，不白屏', () => {
    expect(capabilityProfileFor('mythical_binary', 'wizard')).toBe(UNIVERSAL_FALLBACK)
    expect(UNIVERSAL_FALLBACK.capabilities).toEqual(['overview', 'terminal', 'files', 'backup'])
  })

  it('minecraft_proxy 归一为 minecraft_java（devmock 兼容）', () => {
    expect(capabilityProfileFor('minecraft_proxy', 'proxy')).toBe(capabilityProfileFor('minecraft_java', 'proxy'))
  })

  it('注册表四种内置画像齐全', () => {
    expect(Object.keys(CAPABILITY_REGISTRY).sort()).toEqual([
      'generic:beacon', 'generic:universal', 'minecraft_java:backend', 'minecraft_java:proxy',
    ])
  })
})

describe('resolveCapabilities', () => {
  it('优先后端下发的 capabilities', () => {
    const server: InstanceCapabilityProfile = {
      type: 'generic', role: 'beacon', mcSemantics: false,
      capabilities: ['overview', 'process'],
    }
    const p = resolveCapabilities({ type: 'generic', role: 'beacon', capabilities: server })
    expect(p).toBe(server)
  })

  it('缺失 capabilities 时按 (type, role) 本地兜底', () => {
    const p = resolveCapabilities({ type: 'minecraft_java', role: 'backend' })
    expect(p.capabilities).toContain('metrics')
  })

  it('capabilities 结构非法（非数组）时也回落本地表，不抛错', () => {
    const bad = { type: 'minecraft_java', role: 'proxy', capabilities: { capabilities: 'nope' } as unknown as InstanceCapabilityProfile }
    const p = resolveCapabilities(bad)
    expect(p.capabilities).toContain('bcTopology')
  })

  it('undefined / null 回退安全子集', () => {
    expect(resolveCapabilities(undefined)).toBe(UNIVERSAL_FALLBACK)
    expect(resolveCapabilities(null)).toBe(UNIVERSAL_FALLBACK)
  })
})

describe('hasCapability', () => {
  it('按能力集合判定', () => {
    const backend = capabilityProfileFor('minecraft_java', 'backend')
    expect(hasCapability(backend, 'metrics')).toBe(true)
    expect(hasCapability(backend, 'bcTopology')).toBe(false)
  })
})
