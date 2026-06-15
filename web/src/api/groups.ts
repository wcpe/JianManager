import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface GroupMember {
  id: number
  userId: number
  role: number
  user?: { id: number; username: string }
}

export interface GroupQuota {
  maxInstances: number
  maxBots: number
  maxStorageMb: number
}

export interface GroupInfo {
  id: number
  uuid: string
  name: string
  description: string
  members?: GroupMember[]
  quota?: GroupQuota
  createdAt: string
}

export function useGroups() {
  return useQuery({
    queryKey: ['groups'],
    queryFn: async () => {
      const { data } = await api.get<GroupInfo[]>('/groups')
      return data
    },
  })
}

export function useCreateGroup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { name: string; description?: string }) =>
      api.post('/groups', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['groups'] }),
  })
}

export function useDeleteGroup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/groups/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['groups'] }),
  })
}
