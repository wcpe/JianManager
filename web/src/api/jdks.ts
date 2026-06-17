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

export function useInstallJDK(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { vendor: string; majorVersion: number; arch: string }) =>
      api.post(`/nodes/${nodeId}/jdks/install`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['node-jdks', nodeId] }),
  })
}
