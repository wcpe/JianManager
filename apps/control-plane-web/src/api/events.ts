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

  useEffect(() => {
    if (!useAuthStore.getState().accessToken) return

    const base = api.defaults.baseURL || ''
    const url = `${base}/instances/events`

    // EventSource 不支持自定义 header，改用 fetch + ReadableStream
    const controller = new AbortController()
    // 用于区分「首次连接」与「重连」：只有重连才需要补拉断连期间漏掉的变化。
    let hasConnectedOnce = false

    async function connect() {
      try {
        // 该原生 fetch 绕过 axios 拦截器，连接前自行确保 token 未过期，避免加载期无谓 401（BUG-008）。
        const token = await ensureFreshToken()
        if (!token || controller.signal.aborted) return
        const resp = await fetch(url, {
          headers: { Authorization: `Bearer ${token}` },
          signal: controller.signal,
        })

        if (!resp.ok || !resp.body) {
          // 认证失败时不重试（401 由全局拦截器处理）
          return
        }

        // 连接建立成功。若是重连，说明中间有一段推送空窗，期间发生的状态变化已经漏掉，
        // 必须主动失效一次缓存来补齐——SSE 只推「变化」而不推「当前全量」，漏一个事件
        // 就是永久偏差。（首连不补：那批数据正是首屏刚拉过的。）
        if (hasConnectedOnce) {
          void qc.invalidateQueries({ queryKey: ['instances'] })
        }
        hasConnectedOnce = true

        const reader = resp.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''

        while (true) {
          const { done, value } = await reader.read()
          if (done) {
            // 【必须重连】服务端正常关闭流（网关超时、滚动重启、连接回收）时 done 为 true。
            // 原先这里直接 break，函数随即返回——既不重连也不报错，SSE 就此**静默死亡**，
            // 界面在很长一段时间里看起来「没坏」，实则再也不更新。
            // 删除过渡态轮询后（见 api/instances.ts），这里就是唯一的状态更新通道，不能断。
            if (!controller.signal.aborted) setTimeout(connect, 2000)
            return
          }

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
      } catch {
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

// 需要从 client 导入 api 以获取 baseURL，并复用 ensureFreshToken 做连接前 token 刷新
import api, { ensureFreshToken } from '@/api/client'
