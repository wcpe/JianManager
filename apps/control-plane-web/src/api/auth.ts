import { useMutation } from '@tanstack/react-query'
import api from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { useNavigate } from 'react-router'

interface LoginRequest {
  username: string
  password: string
}

interface LoginResponse {
  accessToken: string
  refreshToken: string
  expiresIn: number
}

/** 登录 mutation；成功后跳到调用方已校验的站内目标。 */
export function useLogin(returnTo = '/') {
  const loginStore = useAuthStore((s) => s.login)
  const navigate = useNavigate()

  return useMutation({
    mutationFn: async (req: LoginRequest) => {
      const { data } = await api.post<LoginResponse>('/auth/login', req)
      return data
    },
    onSuccess: (data) => {
      loginStore(data.accessToken, data.refreshToken)
      navigate(returnTo, { replace: true })
    },
  })
}
