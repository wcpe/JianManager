import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
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

/** 后端错误信封（axios）。 */
type ApiError = Error & { response?: { data?: { message?: string } } }

export function useGroups() {
  return useQuery({
    queryKey: ['groups'],
    queryFn: async () => {
      const { data } = await api.get<GroupInfo[]>('/groups')
      return data
    },
  })
}

// 写操作在 hook 层统一挂成功 toast（验收矩阵 #2）；错误反馈按调用方分工——
// 创建/编辑弹窗有内联 setError 的不在 hook 层重复 toast，其余（删除/成员/配额面板类）hook 层兜底。

export function useCreateGroup() {
  const qc = useQueryClient()
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (body: { name: string; description?: string }) =>
      api.post('/groups', body),
    onSuccess: () => {
      toast.success(t('groups.created'))
      void qc.invalidateQueries({ queryKey: ['groups'] })
    },
  })
}

export function useDeleteGroup() {
  const qc = useQueryClient()
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/groups/${id}`),
    onSuccess: () => {
      toast.success(t('groups.deleted'))
      void qc.invalidateQueries({ queryKey: ['groups'] })
    },
    onError: (err: ApiError) => {
      toast.error(err.response?.data?.message || t('common.error'))
    },
  })
}

/** 编辑用户组名称/描述（FR-156，兑现 FR-003）。 */
export function useUpdateGroup() {
  const qc = useQueryClient()
  const { t } = useTranslation()
  return useMutation({
    mutationFn: ({ id, ...body }: { id: number; name?: string; description?: string }) =>
      api.put(`/groups/${id}`, body),
    onSuccess: () => {
      toast.success(t('groups.updated'))
      void qc.invalidateQueries({ queryKey: ['groups'] })
    },
  })
}

/** 修改用户组配额（实例/Bot/存储上限，FR-156）。 */
export function useUpdateGroupQuota() {
  const qc = useQueryClient()
  const { t } = useTranslation()
  return useMutation({
    mutationFn: ({ id, ...quota }: { id: number; maxInstances?: number; maxBots?: number; maxStorageMb?: number }) =>
      api.put(`/groups/${id}/quota`, quota),
    onSuccess: () => {
      toast.success(t('groups.quotaUpdated'))
      void qc.invalidateQueries({ queryKey: ['groups'] })
    },
  })
}

/** 向用户组添加成员（FR-156）。role 省略为普通成员。 */
export function useAddGroupMember() {
  const qc = useQueryClient()
  const { t } = useTranslation()
  return useMutation({
    mutationFn: ({ id, userId, role }: { id: number; userId: number; role?: number }) =>
      api.post(`/groups/${id}/members`, { userId, role }),
    onSuccess: () => {
      toast.success(t('groups.memberAdded'))
      void qc.invalidateQueries({ queryKey: ['groups'] })
    },
    onError: (err: ApiError) => {
      toast.error(err.response?.data?.message || t('common.error'))
    },
  })
}

/** 从用户组移除成员（按用户 ID，FR-156）。 */
export function useRemoveGroupMember() {
  const qc = useQueryClient()
  const { t } = useTranslation()
  return useMutation({
    mutationFn: ({ id, userId }: { id: number; userId: number }) =>
      api.delete(`/groups/${id}/members/${userId}`),
    onSuccess: () => {
      toast.success(t('groups.memberRemoved'))
      void qc.invalidateQueries({ queryKey: ['groups'] })
    },
    onError: (err: ApiError) => {
      toast.error(err.response?.data?.message || t('common.error'))
    },
  })
}
