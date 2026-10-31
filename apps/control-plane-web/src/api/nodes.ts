import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

// 节点实体类型已回迁应用侧（迁包已撤销，见 ADR-103）；此处转出，调用点零改动。
import type { NodeInfo } from '@/lib/nodes/node-types'
export type { NodeInfo } from '@/lib/nodes/node-types'
import type { ArchivedNode } from '@/lib/nodes/node-types'
export type { ArchivedNode } from '@/lib/nodes/node-types'

/** 节点排空结果。 */
export interface DrainResult {
  stoppedCount: number
  stopped: number[]
  failed: number[]
  errors?: string[]
}

/** enrollment token 签发请求。 */
export interface IssueEnrollTokenRequest {
  /** 预设节点名；留空则注册时以 Worker 上报名生效。 */
  nodeName?: string
  /** token 有效期（分钟），默认 30，范围 1~1440。 */
  ttlMinutes?: number
}

/** enrollment token 签发响应：含一次性明文 + 两端一键安装命令。 */
export interface IssuedEnrollToken {
  /** 明文 token，仅签发时一次性返回、不可二次读取。 */
  token: string
  tokenId: number
  tokenPrefix: string
  expiresAt: string
  nodeName: string
  /** CP 对外公布的 gRPC 地址（host:port），写入一键命令。 */
  controlPlaneGrpc: string
  /** CP 托管安装脚本的基址（scheme://host），供前端拼「手动安装步骤」兜底命令。 */
  scriptBaseUrl: string
  installCommandLinux: string
  installCommandWindows: string
}

/** enrollment token 元数据（列表项，无明文）。 */
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

/** 签发一次性 enrollment token（仅平台管理员）。 */
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

/** 列出 enrollment token 元数据（仅平台管理员）。 */
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

/** 吊销未消费的 enrollment token（仅平台管理员）。 */
export function useRevokeEnrollToken() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/nodes/enroll-tokens/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['enrollTokens'] }),
  })
}

/** 获取节点列表，支持自动轮询刷新；enabled=false 可按需挂起（如侧栏常驻列双空时）。 */
export function useNodes(options?: { refetchInterval?: number; enabled?: boolean }) {
  return useQuery({
    queryKey: ['nodes'],
    queryFn: async () => {
      const { data } = await api.get<NodeInfo[]>('/nodes')
      return data
    },
    refetchInterval: options?.refetchInterval,
    enabled: options?.enabled,
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

/** 置/解节点维护模式（cordon）。 */
export function useSetNodeMaintenance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) =>
      api.post<NodeInfo>(`/nodes/${id}/maintenance`, { enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['nodes'] }),
  })
}

/** 排空节点：停止其上运行实例。 */
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

/** 节点删除被实例守卫拒绝时随 409 返回的实例摘要。 */
export interface NodeDeleteBlockedInstance {
  id: number
  name: string
  status: string
}

/** 节点删除结果：force 级联时报告删除的实例记录数。 */
export interface NodeDeleteResult {
  message: string
  instancesPurged: number
}

/** 
 * 主动下线节点：解除注册并保留记录。
 *：名下有实例回 409 NODE_HAS_INSTANCES（含实例清单）；离线节点可 force 级联
 * 删除实例平台记录（不清理远端文件），故成功后同时失效实例列表。
 *：下线后进归档，故一并失效 archived-nodes。
 */
export function useDeleteNode() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, force }: { id: number; force?: boolean }) =>
      api.delete<NodeDeleteResult>(`/nodes/${id}`, force ? { params: { force: true } } : undefined),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['nodes'] })
      qc.invalidateQueries({ queryKey: ['archived-nodes'] })
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
  })
}


/** 归档清理结果：硬删节点记录；force 时含级联实例数。 */
export interface NodePurgeResult {
  message: string
  instancesPurged: number
}

/** 列出已下线（软删）节点，按 deletedAt 倒序。 */
export function useArchivedNodes(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['archived-nodes'],
    queryFn: async () => {
      const { data } = await api.get<ArchivedNode[]>('/nodes/archived')
      return data
    },
    enabled: options?.enabled,
  })
}

/** 
 * 彻底清理归档节点：仅已软删行生效；force 级联硬删名下实例平台记录，不碰远端文件。
 * 成功后失效 nodes / archived-nodes / instances。
 */
export function usePurgeArchivedNode() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, force }: { id: number; force?: boolean }) =>
      api.delete<NodePurgeResult>(
        `/nodes/archived/${id}`,
        force ? { params: { force: true } } : undefined,
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['nodes'] })
      qc.invalidateQueries({ queryKey: ['archived-nodes'] })
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
  })
}

/** 节点出站代理视图（见 ADR-043）。含凭据的 URL 已由后端脱敏。 */
export interface NodeProxyView {
  /** inherit=继承全局默认 / custom=自定义。 */
  mode: 'inherit' | 'custom'
  /** 节点自定义代理地址（脱敏；仅 custom 有意义）。 */
  url: string
  /** 节点自定义免代理列表（逗号分隔；仅 custom 有意义）。 */
  noProxy: string
  /** 当前生效代理地址（脱敏）：custom→节点值，inherit→全局默认。 */
  effectiveUrl: string
  /** 当前生效免代理列表。 */
  effectiveNoProxy: string
  /** 平台全局默认代理地址（脱敏，供展示「继承自全局」）。 */
  globalDefaultUrl: string
  /** 节点是否在线（离线时前端标注「待下发」，下次心跳生效）。 */
  online: boolean
}

/** 设置节点代理请求：mode=inherit 时 url/noProxy 忽略；custom 时 url 必填。 */
export interface UpdateNodeProxyBody {
  mode: 'inherit' | 'custom'
  url?: string
  noProxy?: string
}

/** 查询节点出站代理配置（仅平台管理员）。 */
export function useNodeProxy(nodeId: number, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['node-proxy', nodeId],
    queryFn: async () => {
      const { data } = await api.get<NodeProxyView>(`/nodes/${nodeId}/proxy`)
      return data
    },
    enabled: (options?.enabled ?? true) && !!nodeId,
  })
}

/** 设置节点出站代理（继承全局/自定义，仅平台管理员）。 */
export function useUpdateNodeProxy(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: UpdateNodeProxyBody) => {
      const { data } = await api.patch<NodeProxyView>(`/nodes/${nodeId}/proxy`, body)
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['node-proxy', nodeId] }),
  })
}

/** 
 * 正在启动的 Worker 进度。
 *
 * 与 `useNodes` 分开取数的原因：启动中的 Worker **尚未注册**，在节点的正式列表里
 * 根本没有记录（首装连 UUID 都还没有）。混进同一个列表会凭空出现「存在但从未上线」
 * 的节点，所以这里单独一个端点、单独一份数据。
 *
 * 轮询 5s：启动进度本身以秒级变化（一次启动通常几十毫秒到数秒），
 * 过密没有收益；而 Worker 每完成一个阶段就上报一次，5s 足以看到阶段推进。
 */
export interface NodeStartupProgressInfo {
  key: string
  nodeUuid?: string
  nodeName: string
  phase: string
  phaseElapsedMs: number
  totalElapsedMs: number
  phasesDone: number
  phasesTotal: number
  updatedAt: string
  /** 后端算好的陈旧标记：true 表示超时未更新，Worker 可能已崩溃。 */
  stale: boolean
}

export function useNodeStartupProgress(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['nodes', 'startup-progress'],
    queryFn: async () => {
      const { data } = await api.get<{ items: NodeStartupProgressInfo[] }>('/nodes/startup-progress')
      return data.items ?? []
    },
    refetchInterval: 5_000,
    enabled: options?.enabled,
  })
}

/** 启动阶段的中文名。未收录的阶段原样显示——不静默吞掉，否则新阶段上线后界面会空白。 */
export const STARTUP_PHASE_LABELS: Record<string, string> = {
  config: '加载配置',
  dataroot: '初始化数据根',
  identity: '读取节点身份',
  process_recover: '恢复实例',
  ingest_index_open: '打开采集索引',
  ingest_state_load: '加载采集状态',
  register: '注册到主控台',
  heartbeat: '启动心跳',
}
