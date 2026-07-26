import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** V1 兼容写白名单（仅用于旧 Token 展示，不再作为新建默认值）。 */
export const DEFAULT_WRITE_ALLOWLIST = ['instance.life', 'node.maintenance'] as const

/** V1 写白名单项：仅用于旧 Token 的兼容展示映射（FR-395）。 */
export const WRITE_ALLOWLIST_OPTIONS = [
  { value: 'instance.life', labelKey: 'agentTokens.write.instanceLife' },
  { value: 'node.maintenance', labelKey: 'agentTokens.write.nodeMaintenance' },
] as const

/** V2 能力分组选项（与后端 service.AgentCapability* 对齐，FR-395 / ADR-080）。 */
export const CAPABILITY_OPTIONS = [
  { value: 'node.read', labelKey: 'agentTokens.capability.nodeRead' },
  { value: 'node.operate', labelKey: 'agentTokens.capability.nodeOperate' },
  { value: 'node.destructive', labelKey: 'agentTokens.capability.nodeDestructive' },
  { value: 'instance.read', labelKey: 'agentTokens.capability.instanceRead' },
  { value: 'instance.life', labelKey: 'agentTokens.capability.instanceLife' },
  { value: 'instance.command', labelKey: 'agentTokens.capability.instanceCommand' },
  { value: 'instance.provision', labelKey: 'agentTokens.capability.instanceProvision' },
  { value: 'instance.configure', labelKey: 'agentTokens.capability.instanceConfigure' },
  { value: 'instance.content', labelKey: 'agentTokens.capability.instanceContent' },
  { value: 'instance.destructive', labelKey: 'agentTokens.capability.instanceDestructive' },
  { value: 'bot.read', labelKey: 'agentTokens.capability.botRead' },
  { value: 'bot.manage', labelKey: 'agentTokens.capability.botManage' },
  { value: 'bot.load', labelKey: 'agentTokens.capability.botLoad' },
  { value: 'observability.read', labelKey: 'agentTokens.capability.observabilityRead' },
] as const

/** 新建 V2 Token 的默认能力：仅只读，服务端不隐式补能力。 */
export const DEFAULT_CAPABILITIES = ['node.read', 'instance.read', 'observability.read'] as const

/**
 * 列表项：后端 model.AgentToken 序列化后，scope/白名单字段为 JSON 文本字符串
 *（如 `"[1,2]"` / `"[]"`），前端统一经 parse* 规范化为数组后再展示。
 */
export interface AgentTokenRaw {
  id: number
  name: string
  tokenPrefix: string
  scopedInstanceIds: string | number[] | null
  scopedNodeIds: string | number[] | null
  writeAllowlist: string | string[] | null
  expiresAt: string
  revoked: boolean
  lastUsedAt?: string | null
  createdAt: string
  createdBy: number
  /** 近 24h 调用次数（FR-390）。 */
  callCount24h?: number
  /** 策略版本；缺失或非 2 一律按 V1 展示（FR-395）。 */
  policyVersion?: number | null
  /** V2 能力数组；V1 为空（FR-395）。 */
  capabilities?: string | string[] | null
}

/** 前端规范化后的 Token 元数据（无明文）。 */
export interface AgentTokenInfo {
  id: number
  name: string
  tokenPrefix: string
  scopedInstanceIds: number[]
  scopedNodeIds: number[]
  writeAllowlist: string[]
  expiresAt: string
  revoked: boolean
  lastUsedAt?: string | null
  createdAt: string
  createdBy: number
  callCount24h: number
  /** 1=V1 兼容策略，2=V2 能力策略。 */
  policyVersion: 1 | 2
  /** V2 能力；V1 恒为空数组。 */
  capabilities: string[]
}

/** POST 签发请求体。V2 提交 capabilities，禁止与 writeAllowlist 混用。 */
export interface IssueAgentTokenRequest {
  name: string
  scopedInstanceIds: number[]
  scopedNodeIds: number[]
  writeAllowlist?: string[]
  policyVersion?: number
  capabilities?: string[]
  /** 有效天数；<=0 后端默认 90，上限 365。 */
  ttlDays?: number
}

/** POST 签发响应：明文仅此一次。 */
export interface IssuedAgentToken {
  token: AgentTokenRaw
  plaintext: string
}

/** 把后端 JSON 文本或数组字段解析为 number[]。 */
export function parseUintList(value: string | number[] | null | undefined): number[] {
  if (value == null || value === '') return []
  if (Array.isArray(value)) {
    return value.map((n) => Number(n)).filter((n) => Number.isFinite(n) && Number.isInteger(n) && n > 0)
  }
  try {
    const parsed = JSON.parse(value) as unknown
    if (!Array.isArray(parsed)) return []
    return parsed.map((n) => Number(n)).filter((n) => Number.isFinite(n) && Number.isInteger(n) && n > 0)
  } catch {
    return []
  }
}

/** 把后端 JSON 文本或数组字段解析为 string[]。 */
export function parseStringList(value: string | string[] | null | undefined): string[] {
  if (value == null || value === '') return []
  if (Array.isArray(value)) return value.map(String).filter(Boolean)
  try {
    const parsed = JSON.parse(value) as unknown
    if (!Array.isArray(parsed)) return []
    return parsed.map(String).filter(Boolean)
  } catch {
    return []
  }
}

/** 规范化列表项（V1/V2 兼容，FR-395）。 */
export function normalizeAgentToken(raw: AgentTokenRaw): AgentTokenInfo {
  const n = Number(raw.callCount24h)
  const pv = raw.policyVersion === 2 ? 2 : 1
  const capabilities = pv === 2 ? parseStringList(raw.capabilities) : []
  return {
    id: raw.id,
    name: raw.name,
    tokenPrefix: raw.tokenPrefix,
    scopedInstanceIds: parseUintList(raw.scopedInstanceIds),
    scopedNodeIds: parseUintList(raw.scopedNodeIds),
    writeAllowlist: parseStringList(raw.writeAllowlist),
    expiresAt: raw.expiresAt,
    revoked: raw.revoked,
    lastUsedAt: raw.lastUsedAt,
    createdAt: raw.createdAt,
    createdBy: raw.createdBy,
    callCount24h: Number.isFinite(n) && n >= 0 ? Math.floor(n) : 0,
    policyVersion: pv,
    capabilities,
  }
}

/**
 * 计算 Token 展示状态：
 * - revoked：已吊销
 * - expired：已过期（未吊销但 expiresAt <= now）
 * - active：有效
 */
export function agentTokenStatus(
  tok: Pick<AgentTokenInfo, 'revoked' | 'expiresAt'>,
  now = Date.now(),
): 'active' | 'expired' | 'revoked' {
  if (tok.revoked) return 'revoked'
  const exp = Date.parse(tok.expiresAt)
  if (Number.isFinite(exp) && exp <= now) return 'expired'
  return 'active'
}

/** 列出全部 Agent Token 元数据（仅平台管理员，FR-387 / FR-384）。 */
export function useAgentTokens(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['agentTokens'],
    queryFn: async () => {
      const { data } = await api.get<AgentTokenRaw[]>('/agent/tokens')
      return (data ?? []).map(normalizeAgentToken)
    },
    enabled: options?.enabled ?? true,
  })
}

/** 签发 Agent Token；成功后失效列表缓存。明文仅在 mutation 结果中出现一次。 */
export function useIssueAgentToken() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (req: IssueAgentTokenRequest) => {
      const { data } = await api.post<IssuedAgentToken>('/agent/tokens', req)
      return {
        token: normalizeAgentToken(data.token),
        plaintext: data.plaintext,
      }
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['agentTokens'] }),
  })
}

/** 吊销 Agent Token。 */
export function useRevokeAgentToken() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/agent/tokens/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['agentTokens'] }),
  })
}
