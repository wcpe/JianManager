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

/** GET /users 分页信封（带 limit 时的响应形态，FR-336）。 */
export interface UserPage {
  items: UserInfo[]
  total: number
  limit: number
  offset: number
}

/** 服务端搜索/分页参数（FR-336）。limit 是信封形态开关，调用方必须携带。 */
export interface UserSearchParams {
  q?: string
  limit?: number
  offset?: number
}

/**
 * 服务端搜索用户（FR-336）：GET /users?q=&limit=&offset= 返回分页信封。
 * queryKey 前缀复用 ['users']，写操作的 invalidateQueries({queryKey:['users']}) 前缀命中可联动刷新；
 * placeholderData 保留上一页结果，避免键入/加载更多时列表闪烁。
 */
export function useUserSearch(params: UserSearchParams) {
  return useQuery({
    queryKey: ['users', 'search', params],
    queryFn: async () => {
      const { data } = await api.get<UserPage>('/users', { params })
      return data
    },
    placeholderData: (prev) => prev,
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
