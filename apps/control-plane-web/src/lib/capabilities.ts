import { useMemo } from 'react'

/**
 * 实例能力画像（FR-445，ADR-091）——前端门控的唯一真源。
 *
 * 后端 `GET /instances/:id`（及 MCP `agent_get_instance`）在响应里下发 `capabilities`；
 * 前端**优先**用它，缺失时（离线/mock 兼容/旧响应）按 `(type, role)` 走本地兜底表。
 * 本地兜底表与后端 `service/capability_profile.go` 的注册表一一对应，修改须同步。
 */

/** 能力：一个能力对应详情页的一个 Tab。 */
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

/** 本地兜底表（键 `${type}:${role}`），与后端注册表一一对应。 */
export const CAPABILITY_REGISTRY: Record<string, InstanceCapabilityProfile> = {
  'minecraft_java:backend': {
    type: 'minecraft_java',
    role: 'backend',
    mcSemantics: true,
    capabilities: ['overview', 'terminal', 'files', 'plugins', 'metrics', 'players', 'business', 'bot', 'backup'],
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

/** 按 (type, role) 取本地兜底画像。 */
export function capabilityProfileFor(type?: string, role?: string): InstanceCapabilityProfile {
  // 归一：部分数据源（devmock 等）以 `minecraft_proxy` 表达代理实现形态，
  // 语义上与 `minecraft_java` 同属 Java 代理（role=proxy），故归一后查表。
  const normType = type === 'minecraft_proxy' ? 'minecraft_java' : type
  if (normType && role && CAPABILITY_REGISTRY[`${normType}:${role}`]) return CAPABILITY_REGISTRY[`${normType}:${role}`]
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
