import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

export interface TerminalTokenData {
  token: string
  wsUrl: string
  expiresIn: number
}

/** 获取实例终端一次性连接 token（仅实例运行时可用）。 */
export function useTerminalToken(instanceId: number, permission: 'read' | 'write', enabled = true) {
  return useQuery({
    queryKey: ['terminalToken', instanceId, permission],
    queryFn: async () => {
      const { data } = await api.get<TerminalTokenData>(
        `/instances/${instanceId}/terminal-token`,
        { params: { permission } },
      )
      return data
    },
    enabled: !!instanceId && enabled,
    staleTime: 0,
    retry: 1,
  })
}
