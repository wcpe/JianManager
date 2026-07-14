import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import api from '@/api/client'

export interface UserInfo {
  id: number
  uuid: string
  username: string
  role: number
  status: number
  createdAt: string
}

export function useUsers() {
  return useQuery({
    queryKey: ['users'],
    queryFn: async () => {
      const { data } = await api.get<UserInfo[]>('/users')
      return data
    },
  })
}

// 写操作在 hook 层统一挂成功 toast（验收矩阵 #2：账号类高影响操作要有明确成功反馈）；
// 错误反馈按调用方分工——弹窗内有内联 setError 的不在 hook 层重复 toast。

export function useUpdateUser() {
  const qc = useQueryClient()
  const { t } = useTranslation()
  return useMutation({
    mutationFn: ({ id, ...body }: { id: number; role?: number; status?: number; password?: string }) =>
      api.put(`/users/${id}`, body),
    onSuccess: () => {
      toast.success(t('users.updated'))
      void qc.invalidateQueries({ queryKey: ['users'] })
    },
  })
}

export function useDeleteUser() {
  const qc = useQueryClient()
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/users/${id}`),
    onSuccess: () => {
      toast.success(t('users.deleted'))
      void qc.invalidateQueries({ queryKey: ['users'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || t('common.error'))
    },
  })
}
