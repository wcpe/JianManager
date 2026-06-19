import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 群组（Network 软标签）列表项（对应后端 service.NetworkSummary，FR-032）。 */
export interface NetworkSummary {
  id: number
  uuid: string
  name: string
  description: string
  memberCount: number
  createdAt: string
}

/** 群组成员实例概要。 */
export interface NetworkMember {
  instanceId: number
  name: string
  role: string
  nodeId: number
  status: string
}

/** 群组详情（含成员）。 */
export interface NetworkDetail {
  id: number
  uuid: string
  name: string
  description: string
  members: NetworkMember[]
}

/** 群组批量操作结果。 */
export interface BatchActionResult {
  action: string
  total: number
  succeeded: number
  failed: number
  results: { instanceId: number; ok: boolean; error?: string }[]
}

/** 群组列表。 */
export function useNetworks() {
  return useQuery({
    queryKey: ['networks'],
    queryFn: async () => {
      const { data } = await api.get<NetworkSummary[]>('/networks')
      return data
    },
  })
}

/** 群组详情（含成员）。 */
export function useNetwork(id: number) {
  return useQuery({
    queryKey: ['networks', id],
    queryFn: async () => {
      const { data } = await api.get<NetworkDetail>(`/networks/${id}`)
      return data
    },
    enabled: !!id,
  })
}

/** 创建群组。 */
export function useCreateNetwork() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { name: string; description?: string }) =>
      api.post<NetworkSummary>('/networks', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['networks'] }),
  })
}

/** 重命名/改描述。 */
export function useUpdateNetwork(id: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { name?: string; description?: string }) =>
      api.patch<NetworkDetail>(`/networks/${id}`, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['networks'] })
      qc.invalidateQueries({ queryKey: ['networks', id] })
    },
  })
}

/** 删除群组（不影响成员实例与注册关系）。 */
export function useDeleteNetwork() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/networks/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['networks'] }),
  })
}

/** 批量加入成员。 */
export function useAddNetworkMembers(id: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (instanceIds: number[]) =>
      api.post(`/networks/${id}/members`, { instanceIds }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['networks'] })
      qc.invalidateQueries({ queryKey: ['networks', id] })
    },
  })
}

/** 移除成员。 */
export function useRemoveNetworkMember(id: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (instanceId: number) => api.delete(`/networks/${id}/members/${instanceId}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['networks'] })
      qc.invalidateQueries({ queryKey: ['networks', id] })
    },
  })
}

/** 群组成员批量启停（按标签批量运维）。 */
export function useNetworkAction(id: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (action: 'start' | 'stop' | 'restart') =>
      api.post<BatchActionResult>(`/networks/${id}/actions`, { action }).then((r) => r.data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['networks', id] })
    },
  })
}
