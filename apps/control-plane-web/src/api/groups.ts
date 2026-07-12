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

/** 编辑用户组名称/描述（FR-156，兑现 FR-003）。 */
export function useUpdateGroup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...body }: { id: number; name?: string; description?: string }) =>
      api.put(`/groups/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['groups'] }),
  })
}

/** 修改用户组配额（实例/Bot/存储上限，FR-156）。 */
export function useUpdateGroupQuota() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...quota }: { id: number; maxInstances?: number; maxBots?: number; maxStorageMb?: number }) =>
      api.put(`/groups/${id}/quota`, quota),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['groups'] }),
  })
}

/** 向用户组添加成员（FR-156）。role 省略为普通成员。 */
export function useAddGroupMember() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, userId, role }: { id: number; userId: number; role?: number }) =>
      api.post(`/groups/${id}/members`, { userId, role }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['groups'] }),
  })
}

/** 从用户组移除成员（按用户 ID，FR-156）。 */
export function useRemoveGroupMember() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, userId }: { id: number; userId: number }) =>
      api.delete(`/groups/${id}/members/${userId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['groups'] }),
  })
}
