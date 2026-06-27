import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface NodeJDK {
  id: number
  nodeId: number
  vendor: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  createdAt: string
}

export function useNodeJDKs(nodeId: number) {
  return useQuery({
    queryKey: ['node-jdks', nodeId],
    queryFn: async () => {
      const { data } = await api.get<NodeJDK[]>(`/nodes/${nodeId}/jdks`)
      return data
    },
    enabled: !!nodeId,
  })
}

export function useCreateJDK(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: Omit<NodeJDK, 'id' | 'nodeId' | 'createdAt'>) =>
      api.post(`/nodes/${nodeId}/jdks`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] }),
  })
}

export function useUpdateJDK(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ jdkId, body }: { jdkId: number; body: Partial<Omit<NodeJDK, 'id' | 'nodeId' | 'createdAt'>> }) =>
      api.put(`/nodes/${nodeId}/jdks/${jdkId}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] }),
  })
}

export function useDeleteJDK(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (jdkId: number) => api.delete(`/nodes/${nodeId}/jdks/${jdkId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] }),
  })
}

/** POST /nodes/:id/jdks/install 的 202 响应（FR-183，见 ADR-040）：异步任务受理，回执 taskId。 */
export interface InstallJDKAccepted {
  taskId: string
}

/**
 * 一键下载安装 JDK（FR-072 + FR-183 异步化）。
 * 由同步阻塞（最长 20min 返回 JDK 记录）改为**异步**：发起即返回 taskId（HTTP 202），
 * 进度/完成经全局任务中心（`/tasks`）与站内信查看；JDK 列表在任务完成后由心跳落库、随后失效刷新。
 * 这里仍失效 node-jdks 缓存（受理后立即刷一次；真正出现新条目要等任务完成，前端可由任务中心引导）。
 */
export function useInstallJDK(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { vendor: string; majorVersion: number; arch: string }) =>
      api.post<InstallJDKAccepted>(`/nodes/${nodeId}/jdks/install`, body).then((r) => r.data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] })
      qc.invalidateQueries({ queryKey: ['tasks'] })
    },
  })
}
