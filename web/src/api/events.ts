import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth'

/**
 * 订阅实例状态变更的 SSE 事件流。
 * 收到事件后自动失效实例相关 Query，驱使 TanStack Query 重新拉取最新数据。
 * 替代轮询方案，实现接近实时的状态推送。
 */
export function useInstanceEvents() {
  const qc = useQueryClient()
  const sourceRef = useRef<EventSource | null>(null)

  useEffect(() => {
    const token = useAuthStore.getState().accessToken
    if (!token) return

    const base = api.defaults.baseURL || ''
    const url = `${base}/instances/events`

    // EventSource 不支持自定义 header，改用 fetch + ReadableStream
    const controller = new AbortController()

    async function connect() {
      try {
        const resp = await fetch(url, {
          headers: { Authorization: `Bearer ${token}` },
          signal: controller.signal,
        })

        if (!resp.ok || !resp.body) {
          // 认证失败时不重试（401 由全局拦截器处理）
          return
        }

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
            if (line.startsWith('event: instance')) {
              // 下一行 data: 含 JSON 事件
              continue
            }
            if (line.startsWith('data: ') && line.includes('instanceUuid')) {
              try {
                const json = JSON.parse(line.slice(6))
                if (json.type === 'state_change') {
                  // 失效实例列表和详情缓存，触发重新拉取
                  qc.invalidateQueries({ queryKey: ['instances'] })
                }
              } catch {
                // 忽略解析错误
              }
            }
          }
        }
      } catch (err) {
        if (controller.signal.aborted) return
        // 连接失败时延迟重试
        setTimeout(connect, 5000)
      }
    }

    connect()

    return () => {
      controller.abort()
    }
  }, [qc])
}

// 需要从 client 导入 api 以获取 baseURL
import api from '@/api/client'
