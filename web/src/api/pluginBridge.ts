import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'
import { useAuthStore } from '@/stores/auth'

/** 某实例的插件连接状态（FR-103 / ADR-012）。 */
export interface PluginConnection {
  instanceUuid: string
  instanceId: number
  instanceName: string
  nodeUuid: string
  connected: boolean
  lastEventAt: number
}

/** 插件桥连接 token（签发后写入插件配置）。 */
export interface PluginToken {
  token: string
  wsUrl: string
  instanceUuid: string
  expiresIn: number
}

/** 插件桥实时事件（SSE 推送）。 */
export interface PluginEvent {
  instanceUuid: string
  type: string
  data: string
  timestamp: number
}

/** 已连插件列表（连接状态）。轮询刷新作为 SSE 之外的兜底。 */
export function usePluginConnections(options?: { refetchInterval?: number }) {
  return useQuery({
    queryKey: ['pluginConnections'],
    queryFn: async () => {
      const { data } = await api.get<PluginConnection[]>('/plugins')
      return data
    },
    refetchInterval: options?.refetchInterval,
  })
}

/** 为实例签发插件桥 token。 */
export function useIssuePluginToken() {
  return useMutation({
    mutationFn: async (instanceId: number) => {
      const { data } = await api.post<PluginToken>(`/instances/${instanceId}/plugin-token`)
      return data
    },
  })
}

/** 向实例当前连入的插件下发指令（踢/封/whitelist 等）。 */
export function useSendPluginCommand() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ instanceId, action, argsJson }: { instanceId: number; action: string; argsJson?: string }) =>
      api.post(`/instances/${instanceId}/plugin-command`, { action, argsJson: argsJson ?? '' }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['pluginConnections'] }),
  })
}

/**
 * 订阅插件桥 SSE 事件流，把最近事件保存在本地状态供 UI 展示。
 * 收到 connected/disconnected 事件时同时失效连接列表缓存，触发重新拉取。
 * 复用 events.ts 的 fetch + ReadableStream 方案（EventSource 不支持自定义 header）。
 */
export function usePluginEvents(maxEvents = 50) {
  const qc = useQueryClient()
  const [events, setEvents] = useState<PluginEvent[]>([])

  useEffect(() => {
    const token = useAuthStore.getState().accessToken
    if (!token) return

    const base = api.defaults.baseURL || ''
    const url = `${base}/plugins/events`
    const controller = new AbortController()

    async function connect() {
      try {
        const resp = await fetch(url, {
          headers: { Authorization: `Bearer ${token}` },
          signal: controller.signal,
        })
        if (!resp.ok || !resp.body) return

        const reader = resp.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''

        while (true) {
          const { done, value } = await reader.read()
          if (done) break
          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() || ''

          for (const line of lines) {
            if (line.startsWith('data: ') && line.includes('instanceUuid')) {
              try {
                const json = JSON.parse(line.slice(6)) as PluginEvent
                if (!json.instanceUuid) continue
                setEvents((prev) => [json, ...prev].slice(0, maxEvents))
                if (json.type === 'connected' || json.type === 'disconnected') {
                  qc.invalidateQueries({ queryKey: ['pluginConnections'] })
                }
              } catch {
                // 忽略解析错误（如初始 connected 确认帧）
              }
            }
          }
        }
      } catch (err) {
        if (controller.signal.aborted) return
        setTimeout(connect, 5000)
      }
    }

    connect()
    return () => controller.abort()
  }, [qc, maxEvents])

  return events
}
