import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 统一 Runtime 视图行（FR-298）：node_jdks(type=jdk) + node_runtimes 读侧拼装。 */
export interface NodeRuntimeItem {
  id: number
  nodeId: number
  /** 运行时类型：jdk / nodejs / python（预留）。 */
  type: string
  /** 展示名：jdk=厂商（Temurin/...）；其它=登记名（如 "Node.js 22"）。 */
  name: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  createdAt: string
}

/** 一条扫描发现的运行时候选（FR-298）。 */
export interface RuntimeCandidate {
  type: string
  vendor: string
  version: string
  majorVersion: number
  arch: string
  path: string
  /** 已在库（托管根下 / DB 已登记同路径）：默认不勾选、不可重复入库。 */
  alreadyRegistered: boolean
}

/** 登记请求体（POST /nodes/:id/runtimes）：type=jdk 转发现有 JDK 链路，其它落 node_runtimes。 */
export interface RegisterRuntimeBody {
  type: string
  name?: string
  vendor?: string
  majorVersion: number
  version: string
  arch?: string
  path: string
  managed?: boolean
}

/** GET /nodes/:id/runtimes — 统一 Runtime 视图（含 syncFromWorker 容忍语义）。 */
export function useNodeRuntimes(nodeId: number, opts?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['node-runtimes', nodeId],
    queryFn: async () => {
      const { data } = await api.get<NodeRuntimeItem[]>(`/nodes/${nodeId}/runtimes`)
      return data
    },
    enabled: !!nodeId && (opts?.enabled ?? true),
  })
}

/** POST /nodes/:id/runtimes/scan — 扫描节点常见安装路径回候选列表（节点离线 503）。 */
export function useScanRuntimes(nodeId: number) {
  return useMutation({
    mutationFn: (types?: string[]) =>
      api
        .post<{ candidates: RuntimeCandidate[] }>(`/nodes/${nodeId}/runtimes/scan`, { types: types ?? [] })
        .then((r) => r.data.candidates),
  })
}

/** POST /nodes/:id/runtimes — 登记单条运行时（扫描候选入库 / 手动登记泛化）。 */
export function useRegisterRuntime(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: RegisterRuntimeBody) =>
      api.post<NodeRuntimeItem>(`/nodes/${nodeId}/runtimes`, body).then((r) => r.data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['node-runtimes', nodeId] })
      // type=jdk 落 node_jdks：JDK 面板列表一并失效。
      qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] })
    },
  })
}

/** DELETE /nodes/:id/runtimes/:rid?type= — 删除（type 定位承载表；jdk 托管连文件、其它只删记录）。 */
export function useDeleteRuntime(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, type }: { id: number; type: string }) =>
      api.delete(`/nodes/${nodeId}/runtimes/${id}?type=${encodeURIComponent(type)}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['node-runtimes', nodeId] })
      qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] })
    },
  })
}
