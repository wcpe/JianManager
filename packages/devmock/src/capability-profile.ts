/**
 * 实例能力画像（FR-445 / ADR-091）——devmock 侧真源。
 *
 * 画像有三份登记（后端 `internal/controlplane/service/capability_profile.go` 注册表、
 * 前端 `apps/control-plane-web/src/lib/capabilities.ts` 兜底表、devmock 本模块）。三份必须
 * 逐画像一致，否则「假后端」会与真后端/前端对同一实例给出不同的 Tab 集合。
 * 一致性由前端 `src/lib/capabilities.contract.test.ts`（devmock↔前端枚举比对）与
 * 后端 `service/capability_profile_test.go` 共同看守。
 *
 * 代理（role=proxy）在后端的真实类型是 `minecraft_java`（见 `internal/controlplane/model/instance.go`），
 * 本模块不再使用已不存在的 `minecraft_proxy` 伪类型（前端亦不再为其做归一）。
 */

/** 能力数据来源偏好（按序降级，FR-447 编排语义）。 */
export type DataSource = 'probe' | 'direct' | 'node' | 'none'

/** 实例能力画像（与后端 `model.InstanceCapabilityProfile` / 前端同构）。 */
export interface MockCapabilityProfile {
  type: string
  role: string
  /** 是否具备 MC 服务端「世界语义」（world/chunk/TPS）：只影响 Tab 内字段取舍，不作 Tab 级门控。 */
  mcSemantics: boolean
  capabilities: string[]
  sources?: Record<string, string[]>
}

/** 未知 (type, role) 的安全回退：overview/terminal/files + backup，任何进程都成立。 */
export const UNIVERSAL_CAPABILITIES: readonly string[] = ['overview', 'terminal', 'files', 'backup']

/**
 * 画像表（键 `${type}:${role}`），与后端 `capabilityRegistry` 一一对应（含 sources）。
 * proxy 画像保留 process/health/config（BC 自身运行指标/端口健康/config.yml 各有独立 Tab，
 * 详见 minor 5 的「子面板单一归属」——由组件层负责不重复渲染，画像不裁剪）。
 * 新增产品 = 三处同步加一条描述符。
 */
export const CAPABILITY_PROFILES: Record<string, Omit<MockCapabilityProfile, 'type' | 'role'>> = {
  'minecraft_java:backend': {
    mcSemantics: true,
    capabilities: ['overview', 'terminal', 'files', 'plugins', 'metrics', 'players', 'business', 'bot', 'backup', 'clone'],
    sources: { metrics: ['probe', 'direct'], players: ['probe', 'direct'], process: ['node'] },
  },
  'minecraft_java:proxy': {
    mcSemantics: false,
    capabilities: ['overview', 'terminal', 'files', 'bcTopology', 'players', 'plugins', 'process', 'health', 'config', 'backup'],
    sources: { bcTopology: ['node'], players: ['probe', 'direct'], process: ['node'], health: ['direct'], config: ['node'] },
  },
  'generic:beacon': {
    mcSemantics: false,
    capabilities: ['overview', 'terminal', 'files', 'process', 'health', 'config', 'backup'],
    sources: { process: ['node'], health: ['direct'], config: ['node'] },
  },
  'generic:universal': {
    mcSemantics: false,
    capabilities: ['overview', 'terminal', 'files', 'process', 'health', 'config', 'backup'],
    sources: { process: ['node'], health: ['direct'], config: ['node'] },
  },
}

/**
 * 按 (type, role) 取画像；未知组合回退安全子集（不白屏）。
 * 返回对象的 type/role 如实回填请求值。
 */
export function capabilityProfileFor(type: string, role: string): MockCapabilityProfile {
  const profile = CAPABILITY_PROFILES[`${type}:${role}`]
  if (profile) return { type, role, ...profile }
  return { type, role, mcSemantics: false, capabilities: [...UNIVERSAL_CAPABILITIES] }
}
