import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 用户自助令牌（FR-513/434 / ADR-104）。
 *
 * 与 `agentTokens.ts` 的区别：这是**人类凭证**（`jmut_`），登录用户即可管理自己的；
 * Agent Token（`jmat_`）是机器凭证，仅平台管理员可签发。
 */

/** 列表项：后端 userTokenView 序列化结果（无明文、无 hash）。 */
export interface UserTokenInfo {
  id: number
  name: string
  deviceLabel: string
  tokenPrefix: string
  scopedNodeIds: number[]
  scopedInstanceIds: number[]
  capabilities: string[]
  expiresAt: string
  revoked: boolean
  lastUsedAt?: string | null
  createdAt: string
}

/** 创建令牌请求体。 */
export interface CreateUserTokenPayload {
  name: string
  deviceLabel?: string
  scopedNodeIds?: number[]
  scopedInstanceIds?: number[]
  capabilities?: string[]
  /** TTL 秒；0 或省略表示用后端默认（90 天）。 */
  ttlSeconds?: number
}

/** 创建响应：`plaintext` 仅此一次可见。 */
export interface CreateUserTokenResult {
  token: UserTokenInfo
  plaintext: string
  notice: string
}

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

const KEY = ['user-tokens'] as const

/** 列出本人全部令牌。 */
export function useUserTokens() {
  return useQuery({
    queryKey: KEY,
    queryFn: async (): Promise<UserTokenInfo[]> => {
      const { data } = await api.get<{ tokens: UserTokenInfo[]; count: number }>('/user/tokens')
      // 后端对空数组返回 []，但字段可能为 null（历史数据），统一兜底。
      return (data.tokens ?? []).map(normalize)
    },
  })
}

/** 规范化后端返回值，保证数组字段永不为 null。 */
export function normalize(t: UserTokenInfo): UserTokenInfo {
  return {
    ...t,
    scopedNodeIds: t.scopedNodeIds ?? [],
    scopedInstanceIds: t.scopedInstanceIds ?? [],
    capabilities: t.capabilities ?? [],
  }
}

/** 创建令牌；成功后刷新列表。 */
export function useCreateUserToken() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: CreateUserTokenPayload): Promise<CreateUserTokenResult> => {
      const { data } = await api.post<CreateUserTokenResult>('/user/tokens', payload)
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: KEY })
    },
  })
}

/** 吊销令牌；成功后刷新列表。 */
export function useRevokeUserToken() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number): Promise<void> => {
      await api.delete(`/user/tokens/${id}`)
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: KEY })
    },
  })
}

/**
 * 令牌状态：active | revoked | expired。
 * 过期判定以服务端 expiresAt 与本地时钟比较，仅用于展示，鉴权仍由服务端决定。
 */
export function userTokenStatus(t: UserTokenInfo, now: number = Date.now()): 'active' | 'revoked' | 'expired' {
  if (t.revoked) return 'revoked'
  if (new Date(t.expiresAt).getTime() <= now) return 'expired'
  return 'active'
}

/** 设备授权确认（FR-515）：用户在浏览器确认 jm 发起的登录。 */
export interface DeviceAuthorizePayload {
  userCode: string
  approve: boolean
}

/**
 * 查询设备码信息用于展示（复用 authorize 前的只读校验）。
 *
 * 后端没有单独的「查询设备码」端点：授权页在用户点确认时才校验，
 * 因此这里不做预取，直接由提交结果给出错误提示（不存在/已过期/已使用）。
 */
export function useAuthorizeDevice() {
  return useMutation({
    mutationFn: async (payload: DeviceAuthorizePayload): Promise<{ ok: boolean; approved: boolean }> => {
      const { data } = await api.post<{ ok: boolean; approved: boolean }>(
        '/oauth/device/authorize',
        payload,
      )
      return data
    },
  })
}

export { errMsg }
