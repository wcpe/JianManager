import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, it, expect } from 'vitest'

import { CAPABILITY_REGISTRY, UNIVERSAL_FALLBACK, type InstanceCapabilityProfile } from './capabilities'
import {
  CAPABILITY_PROFILES as DEVMOCK_PROFILES,
  UNIVERSAL_CAPABILITIES as DEVMOCK_FALLBACK,
} from '@jianmanager/devmock/capability-profile'

/**
 * FR-445 / ADR-091：能力画像有三份登记（后端 `service/capability_profile.go`、前端
 * `lib/capabilities.ts`、devmock `capability-profile.ts`）。本契约测试逐画像枚举比对
 * devmock ↔ 前端，防止任一真源漂移导致「假后端」与前端对同一 (type, role) 给出不同 Tab 集。
 *
 * 后端一份由 `service/capability_profile_test.go` 看守；三份的能力枚举/世界语义须逐一相等。
 */

function normProfile(p: {
  mcSemantics: boolean
  capabilities: readonly string[]
  sources?: Record<string, readonly string[]>
}): Required<Pick<InstanceCapabilityProfile, 'mcSemantics' | 'capabilities'>> & {
  sources: Record<string, string[]>
} {
  const sources: Record<string, string[]> = {}
  for (const [k, v] of Object.entries(p.sources ?? {}).sort(([a], [b]) => a.localeCompare(b))) {
    sources[k] = [...v]
  }
  return { mcSemantics: p.mcSemantics, capabilities: [...p.capabilities], sources }
}

describe('能力画像三真源一致性（devmock ↔ 前端，FR-445）', () => {
  it('画像键集合完全一致', () => {
    expect(Object.keys(DEVMOCK_PROFILES).sort()).toEqual(Object.keys(CAPABILITY_REGISTRY).sort())
  })

  it('每个 (type, role) 的能力集合/顺序与 MC 世界语义一致', () => {
    for (const key of Object.keys(CAPABILITY_REGISTRY)) {
      const fe = normProfile(CAPABILITY_REGISTRY[key])
      const mock = normProfile(DEVMOCK_PROFILES[key])
      expect(mock.capabilities, `${key} capabilities`).toEqual(fe.capabilities)
      expect(mock.mcSemantics, `${key} mcSemantics`).toBe(fe.mcSemantics)
      expect(mock.sources, `${key} sources`).toEqual(fe.sources)
    }
  })

  it('未知组合的安全回退子集一致', () => {
    expect([...DEVMOCK_FALLBACK]).toEqual(UNIVERSAL_FALLBACK.capabilities)
    expect(UNIVERSAL_FALLBACK.mcSemantics).toBe(false)
  })
})

// ── 后端 ↔ 前端 契约看守（N4）────────────────────────────────────────────────
// 上述用例只锁「前端 ↔ devmock」。后端 `service/capability_profile.go` 注册表此前无机器看守，
// 改动后前端兜底表漂移不会失败。仓库根的共享契约样本 `contracts/capability-profiles.json`
// 由后端测试 `service/capability_profile_contract_test.go` 断言（后端 == 样本），
// 本用例断言（前端 == 样本），二者传递性给出「后端 == 前端」看守。
// 改动画像后：UPDATE_CAPABILITY_CONTRACT=1 go test ./internal/controlplane/service/ -run SharedContractFixture
interface ContractProfile {
  mcSemantics: boolean
  capabilities: string[]
  sources?: Record<string, string[]>
}
interface ContractDoc {
  fallback: ContractProfile
  profiles: Record<string, ContractProfile>
}

function normContract(p: ContractProfile): ReturnType<typeof normProfile> {
  return normProfile(p)
}

describe('能力画像共享契约样本（后端 ↔ 前端，FR-445）', () => {
  const repoRoot = path.resolve(__dirname, '../../../../')
  const fixture = JSON.parse(
    readFileSync(path.join(repoRoot, 'contracts/capability-profiles.json'), 'utf8'),
  ) as ContractDoc

  it('画像键集合与后端契约样本一致', () => {
    expect(Object.keys(CAPABILITY_REGISTRY).sort()).toEqual(Object.keys(fixture.profiles).sort())
  })

  it('每个 (type, role) 的能力/MC 世界语义/sources 与后端契约样本一致', () => {
    for (const key of Object.keys(fixture.profiles)) {
      const fe = normProfile(CAPABILITY_REGISTRY[key])
      const backend = normContract(fixture.profiles[key])
      expect(backend, `${key} 契约样本`).toEqual(fe)
    }
  })

  it('安全回退子集与后端契约样本一致', () => {
    expect(normContract(fixture.fallback)).toEqual(normProfile(UNIVERSAL_FALLBACK))
  })
})
