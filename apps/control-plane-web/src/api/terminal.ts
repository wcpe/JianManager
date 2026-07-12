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
    // token TTL 30s。staleTime 0 + 窗口聚焦刷新会反复换 token，导致 Terminal 反复重连
    // （[连接已断开]/已连接 刷屏）。设较长 staleTime 并禁用聚焦刷新，token 在会话内稳定。
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
    retry: 1,
  })
}
