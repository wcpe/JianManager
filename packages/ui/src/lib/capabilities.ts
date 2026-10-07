import { useMemo } from 'react'

/**
 * 实例能力画像（FR-445，ADR-091）——前端门控的唯一真源。
 *
 * 后端**详情路径**（HTTP `GET /instances/:id` 与 MCP `agent_get_instance`）在序列化前
 * 现算并以 `capabilities` 字段下发权威画像；列表/搜索响应同样逐行下发。前端**优先**用它。
 * 下面的 `CAPABILITY_REGISTRY` 只是**显式标注的降级兜底**：仅当响应缺 `capabilities`
 * （离线 / 旧响应 / 手工构造的实例对象）时按 `(type, role)` 查表，避免详情页白屏。
 * 兜底表与后端 `service/capability_profile.go` 注册表、devmock `capability-profile.ts`
 * 三份须逐画像一致，由 `capabilities.contract.test.ts` 枚举比对看守。
 */

/** 能力：多数能力对应详情页的一个 Tab，也有少数是**动作级**能力（如 `clone`，不落 Tab）。 */
export type Capability =
  | 'overview'
  | 'terminal'
  | 'files'
  | 'plugins'
  | 'metrics'
  | 'players'
  | 'business'
  | 'bot'
  | 'backup'
  | 'bcTopology'
  | 'process'
  | 'health'
  | 'config'
  /** 动作级能力：可被克隆（复制后端子服工作目录/配置），仅后端子服类实例声明，不映射为 Tab。 */
  | 'clone'

/** 能力数据来源偏好（按序降级）。 */
export type DataSource = 'probe' | 'direct' | 'node' | 'none'

/** 实例能力画像（与后端 model.InstanceCapabilityProfile 同构）。 */
export interface InstanceCapabilityProfile {
  type: string
  role: string
  /** 是否具备 MC 服务端「世界语义」（world/chunk/TPS）：只影响 Tab 内字段取舍，不作 Tab 级门控。 */
  mcSemantics: boolean
  /** 有序能力集合，决定 Tab 顺序。 */
  capabilities: Capability[]
  /** 各能力的数据来源偏好；缺省视为无偏好。 */
  sources?: Partial<Record<Capability, DataSource[]>>
}

const BASE: Capability[] = ['overview', 'terminal', 'files']

/** 未知 (type, role) 的安全回退：overview/terminal/files + backup，任何进程都成立。 */
export const UNIVERSAL_FALLBACK: InstanceCapabilityProfile = {
  type: '',
  role: '',
  mcSemantics: false,
  capabilities: [...BASE, 'backup'],
}

/** 降级兜底表（键 `${type}:${role}`），与后端注册表、devmock 真源一一对应。 */
export const CAPABILITY_REGISTRY: Record<string, InstanceCapabilityProfile> = {
  'minecraft_java:backend': {
    type: 'minecraft_java',
    role: 'backend',
    mcSemantics: true,
    capabilities: ['overview', 'terminal', 'files', 'plugins', 'metrics', 'players', 'business', 'bot', 'backup', 'clone'],
    sources: { metrics: ['probe', 'direct'], players: ['probe', 'direct'], process: ['node'] },
  },
  'minecraft_java:proxy': {
    type: 'minecraft_java',
    role: 'proxy',
    mcSemantics: false,
    capabilities: ['overview', 'terminal', 'files', 'bcTopology', 'players', 'plugins', 'process', 'health', 'config', 'backup'],
    sources: { bcTopology: ['node'], players: ['probe', 'direct'], process: ['node'], health: ['direct'], config: ['node'] },
  },
  'generic:beacon': {
    type: 'generic',
    role: 'beacon',
    mcSemantics: false,
    capabilities: [...BASE, 'process', 'health', 'config', 'backup'],
    sources: { process: ['node'], health: ['direct'], config: ['node'] },
  },
  'generic:universal': {
    type: 'generic',
    role: 'universal',
    mcSemantics: false,
    capabilities: [...BASE, 'process', 'health', 'config', 'backup'],
    sources: { process: ['node'], health: ['direct'], config: ['node'] },
  },
}

/** 参与画像解析的实例最小结构（避免对 api 层反向依赖）。 */
export interface CapabilitySubject {
  type?: string
  role?: string
  capabilities?: InstanceCapabilityProfile | null
}

/** 按 (type, role) 取本地降级兜底画像（仅在响应未下发 `capabilities` 时使用）。 */
export function capabilityProfileFor(type?: string, role?: string): InstanceCapabilityProfile {
  if (type && role && CAPABILITY_REGISTRY[`${type}:${role}`]) return CAPABILITY_REGISTRY[`${type}:${role}`]
  return UNIVERSAL_FALLBACK
}

/**
 * 解析实例能力画像：优先后端下发的 `capabilities`，缺失时按 (type, role) 本地兜底。
 * 纯函数（非 hook），供列表逐行渲染等无法调 hook 的场景复用。
 */
export function resolveCapabilities(inst?: CapabilitySubject | null): InstanceCapabilityProfile {
  if (inst?.capabilities && Array.isArray(inst.capabilities.capabilities)) return inst.capabilities
  return capabilityProfileFor(inst?.type, inst?.role)
}

/** 画像是否声明某能力。 */
export function hasCapability(profile: InstanceCapabilityProfile, cap: Capability): boolean {
  return profile.capabilities.includes(cap)
}

/**
 * 实例能力画像 hook（FR-445 §2.5）：稳定的画像对象，供详情页 Tab 门控与分段渲染消费。
 * 传 undefined（实例尚未加载）时返回 universal 回退画像。
 */
export function useInstanceCapabilities(inst?: CapabilitySubject | null): InstanceCapabilityProfile {
  return useMemo(() => resolveCapabilities(inst), [inst])
}
