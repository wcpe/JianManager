import { useQuery, useMutation } from '@tanstack/react-query'
import api from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { useNavigate } from 'react-router'

interface SetupStatusResponse {
  setupRequired: boolean
}

interface SetupRequest {
  username: string
  password: string
}

interface SetupResponse {
  accessToken: string
  refreshToken: string
  expiresIn: number
}

/** 查询系统是否需要初始化。 */
export function useSetupStatus() {
  return useQuery({
    queryKey: ['setup-status'],
    queryFn: async () => {
      const { data } = await api.get<SetupStatusResponse>('/setup/status')
      return data
    },
    retry: false,
    staleTime: 0,
  })
}

/** 创建初始管理员并自动登录。 */
export function useSetup() {
  const loginStore = useAuthStore((s) => s.login)
  const navigate = useNavigate()

  return useMutation({
    mutationFn: async (req: SetupRequest) => {
      const { data } = await api.post<SetupResponse>('/setup', req)
      return data
    },
    onSuccess: (data) => {
      loginStore(data.accessToken, data.refreshToken)
      navigate('/')
    },
  })
}
