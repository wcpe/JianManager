import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface NodeInfo {
  id: number
  uuid: string
  name: string
  host: string
  grpcPort: number
  wsPort: number
  status: number // 0=offline, 1=online, 2=starting
  /** 维护模式（cordon）：true 时禁止新实例调度到该节点（FR-048）。 */
  maintenance: boolean
  os: string
  arch: string
  cpuCores: number
  memoryMb: number
  diskTotalMb: number
  cpuUsage: number
  memoryUsage: number
  diskUsage: number
  networkBytesSent: number
  networkBytesRecv: number
  /** 1 分钟 load average（FR-062）；取不到为 0。 */
  loadAvg1: number
  lastHeartbeat: string | null
  createdAt: string
}

/** 节点排空结果（FR-048）。 */
export interface DrainResult {
  stoppedCount: number
  stopped: number[]
  failed: number[]
  errors?: string[]
}

/** enrollment token 签发请求（FR-080）。 */
export interface IssueEnrollTokenRequest {
  /** 预设节点名；留空则注册时以 Worker 上报名生效。 */
  nodeName?: string
  /** token 有效期（分钟），默认 30，范围 1~1440。 */
  ttlMinutes?: number
}

/** enrollment token 签发响应：含一次性明文 + 两端一键安装命令（FR-080）。 */
export interface IssuedEnrollToken {
  /** 明文 token，仅签发时一次性返回、不可二次读取。 */
  token: string
  tokenId: number
  tokenPrefix: string
  expiresAt: string
  nodeName: string
  /** CP 对外公布的 gRPC 地址（host:port），写入一键命令。 */
  controlPlaneGrpc: string
  installCommandLinux: string
  installCommandWindows: string
}

/** enrollment token 元数据（列表项，无明文，FR-080）。 */
export interface EnrollTokenInfo {
  id: number
  tokenPrefix: string
  nodeName: string
  expiresAt: string
  used: boolean
  usedAt: string | null
  usedByNode: string
  revoked: boolean
  createdAt: string
}

/** 签发一次性 enrollment token（仅平台管理员，FR-080）。 */
export function useIssueEnrollToken() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (req: IssueEnrollTokenRequest) => {
      const { data } = await api.post<IssuedEnrollToken>('/nodes/enroll-token', req)
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['enrollTokens'] }),
  })
}

/** 列出 enrollment token 元数据（仅平台管理员，FR-080）。 */
export function useEnrollTokens(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['enrollTokens'],
    queryFn: async () => {
      const { data } = await api.get<EnrollTokenInfo[]>('/nodes/enroll-tokens')
      return data
    },
    enabled: options?.enabled ?? true,
  })
}

/** 吊销未消费的 enrollment token（仅平台管理员，FR-080）。 */
export function useRevokeEnrollToken() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/nodes/enroll-tokens/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['enrollTokens'] }),
  })
}

/** 获取节点列表，支持自动轮询刷新。 */
export function useNodes(options?: { refetchInterval?: number }) {
  return useQuery({
    queryKey: ['nodes'],
    queryFn: async () => {
      const { data } = await api.get<NodeInfo[]>('/nodes')
      return data
    },
    refetchInterval: options?.refetchInterval,
  })
}

/** 获取节点详情。 */
export function useNode(id: number) {
  return useQuery({
    queryKey: ['nodes', id],
    queryFn: async () => {
      const { data } = await api.get<NodeInfo>(`/nodes/${id}`)
      return data
    },
    enabled: !!id,
  })
}

/** 置/解节点维护模式（cordon，FR-048）。 */
export function useSetNodeMaintenance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) =>
      api.post<NodeInfo>(`/nodes/${id}/maintenance`, { enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['nodes'] }),
  })
}

/** 排空节点：停止其上运行实例（FR-048）。 */
export function useDrainNode() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post<DrainResult>(`/nodes/${id}/drain`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['nodes'] })
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
  })
}

/** 主动下线节点：解除注册并保留记录（FR-048）。 */
export function useDeleteNode() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/nodes/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['nodes'] }),
  })
}
