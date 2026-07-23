import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 默认写白名单（与后端 service.AgentWrite* 对齐）。 */
export const DEFAULT_WRITE_ALLOWLIST = ['instance.life', 'node.maintenance'] as const

/** 可选写白名单项（MVP 仅此两项）。 */
export const WRITE_ALLOWLIST_OPTIONS = [
  { value: 'instance.life', labelKey: 'agentTokens.write.instanceLife' },
  { value: 'node.maintenance', labelKey: 'agentTokens.write.nodeMaintenance' },
] as const

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
}

/** POST 签发请求体。 */
export interface IssueAgentTokenRequest {
  name: string
  scopedInstanceIds: number[]
  scopedNodeIds: number[]
  writeAllowlist?: string[]
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

/** 规范化列表项。 */
export function normalizeAgentToken(raw: AgentTokenRaw): AgentTokenInfo {
  const n = Number(raw.callCount24h)
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
