import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from './client'

/**
 * AI 助手对话运行时的前端契约（FR-519，对接 FR-515 的 /assistant/conversations|runs）。
 *
 * 三条与后端一致、但容易被前端写错的语义：
 *  1. 越权一律 **404**（后端有意不用 403）：前端不得据 404 推断「不存在」以外的任何事，
 *     也不得在 404 时提示「无权限」——那等于把「存在性」还给用户。
 *  2. run 的文本是**增量事件**，不是一次性响应。观察走 fetch + Bearer 的 SSE
 *     （不能用 EventSource：它无法带 Authorization 头，只能把 JWT 塞 URL，见 shared-contracts §8）。
 *  3. 事件 seq **每 run 从 1 连续**，因此断线后用 `after=<最后收到的 seq>` 补读即可，
 *     不重放、不重复渲染。
 */

/** 会话。 */
export interface AssistantConversation {
  id: number
  title: string
  selectedModelId: number | null
  createdAt: string
  updatedAt: string
}

/** 消息（历史回看用）。 */
export interface AssistantMessage {
  id: number
  conversationId: number
  role: 'user' | 'assistant' | 'tool' | string
  content: string
  createdAt: string
}

/** 事件帧（与后端 assistantEventFrame 逐字段对应）。events 补读接口回的是**裸数组**。 */
export interface AssistantEventFrame {
  seq: number
  type:
    | 'text_delta'
    | 'tool_call'
    | 'tool_status'
    | 'tool_result'
    | 'usage'
    | 'context_warning'
    | 'error'
    | 'run_end'
    | string
  messageId?: number
  callId?: string
  /** 载荷形状随 type 而变，按需解窄（见事件处理处）。 */
  data: unknown
  createdAt: string
}

/** run 状态（后端 run 状态机取值）。 */
export type AssistantRunStatus =
  | 'queued'
  | 'running'
  | 'waiting_confirmation'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'timed_out'
  | 'interrupted'

/** 提交一轮后的受理结果（后端回 202）。 */
export interface AssistantRunAccepted {
  runId: number
  conversationId: number
  status: AssistantRunStatus
}

const CHAT_KEY = ['assistant', 'chat'] as const

/** 会话列表（按更新时间倒序，后端已排序）。 */
export function useAssistantConversations(enabled = true) {
  return useQuery({
    queryKey: [...CHAT_KEY, 'conversations'],
    queryFn: async () => {
      // 后端形状：{ items, nextCursor }（cursor 分页；本期前端只取首页，
      // 历史抽屉的分页留待需要时再引 nextCursor，不先造一套用不上的游标状态）。
      const { data } = await api.get<{ items: AssistantConversation[]; nextCursor?: string }>(
        '/assistant/conversations',
      )
      return data.items ?? []
    },
    enabled,
  })
}

/** 单个会话的消息历史。 */
export function useAssistantMessages(conversationId: number | null) {
  return useQuery({
    queryKey: [...CHAT_KEY, 'messages', conversationId],
    queryFn: async () => {
      // 后端形状：{ items }。
      const { data } = await api.get<{ items: AssistantMessage[] }>(
        `/assistant/conversations/${conversationId}/messages`,
      )
      return data.items ?? []
    },
    enabled: conversationId !== null,
  })
}

/** 新建会话。 */
export function useCreateConversation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: { title?: string; selectedModelId?: number | null }) => {
      const { data } = await api.post<AssistantConversation>('/assistant/conversations', payload)
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: [...CHAT_KEY, 'conversations'] })
    },
  })
}

/** 重命名会话。 */
export function useRenameConversation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, title }: { id: number; title: string }) => {
      const { data } = await api.patch<AssistantConversation>(`/assistant/conversations/${id}`, { title })
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: [...CHAT_KEY, 'conversations'] })
    },
  })
}

/** 删除会话。 */
export function useDeleteConversation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      await api.delete(`/assistant/conversations/${id}`)
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: [...CHAT_KEY] })
    },
  })
}

/**
 * 提交一轮。
 *
 * `idempotencyKey` 由调用方生成并在重试时**复用**：同键同内容回原 run，
 * 同键异内容 409。前端不要每次点击都换新键——那会让「网络抖动后重试」变成两轮回复。
 */
export function useSubmitRun() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({
      conversationId,
      text,
      contextSnapshot,
      idempotencyKey,
    }: {
      conversationId: number
      text: string
      contextSnapshot?: string | null
      idempotencyKey: string
    }) => {
      const { data } = await api.post<AssistantRunAccepted>(
        `/assistant/conversations/${conversationId}/runs`,
        { text, contextSnapshot: contextSnapshot ?? null },
        { headers: { 'Idempotency-Key': idempotencyKey } },
      )
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: [...CHAT_KEY, 'conversations'] })
    },
  })
}

/** 取消 run（显式停止，与「关闭浮窗」语义无关）。 */
export function useCancelRun() {
  return useMutation({
    mutationFn: async ({ runId }: { runId: number }) => {
      await api.post(`/assistant/runs/${runId}/cancel`, {})
    },
  })
}

/** 生成幂等键（RFC4122 v4 的简化实现，够用且不引依赖）。 */
export function newIdempotencyKey(): string {
  const c = globalThis.crypto
  if (c && 'randomUUID' in c) return c.randomUUID()
  return `k-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
}

/**
 * 观察一个 run 的事件流（fetch + Bearer 的 SSE）。
 *
 * 为什么不用 EventSource：它无法携带 Authorization 头，只能把 JWT 放进 URL
 * （shared-contracts §8 明确禁止），且不支持自定义重连游标。
 *
 * 断线语义：调用方应在 onError 后用**最后收到的 seq** 重新调用本函数补读，
 * 而不是重新提交 run（重提会产生第二条回复）。
 *
 * 返回 abort 函数：卸载/切换会话时必须调用，否则连接会一直挂着。
 */
export function streamRunEvents(params: {
  runId: number
  after?: number
  onEvent: (frame: AssistantEventFrame) => void
  onEnd?: () => void
  onError?: (err: unknown) => void
}): () => void {
  const { runId, after = 0, onEvent, onEnd, onError } = params
  const controller = new AbortController()

  void (async () => {
    try {
      const token = getAccessToken()
      const res = await fetch(
        `/api/v1/assistant/runs/${runId}/stream?after=${encodeURIComponent(String(after))}`,
        {
          method: 'GET',
          headers: {
            Accept: 'text/event-stream',
            ...(token ? { Authorization: `Bearer ${token}` } : {}),
          },
          signal: controller.signal,
        },
      )
      if (!res.ok) {
        throw new Error(`事件流建立失败（HTTP ${res.status}）`)
      }
      if (!res.body) throw new Error('事件流无响应体')

      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buf = ''
      /** 当前帧的 data 行（SSE 允许一帧多行 data；event 名不参与判定——帧内自带 type）。 */
      let dataLines: string[] = []

      const dispatch = () => {
        if (dataLines.length === 0) return
        const raw = dataLines.join('\n')
        dataLines = []
        try {
          const frame = JSON.parse(raw) as AssistantEventFrame
          onEvent(frame)
          // run_end 是每 run 恰好一条的终态事件：收到即停止读取，不必等服务端关连接。
          if (frame.type === 'run_end') {
            controller.abort()
            onEnd?.()
          }
        } catch {
          // 单帧解析失败不该中断整条流（与后端「不静默跳过」的取舍相反：
          // 后端负责不产出坏帧，前端在这里只保证「一帧坏不拖垮整轮」）。
        }
      }

      for (;;) {
        const { value, done } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        // 按行消费；SSE 帧以空行结束。
        for (;;) {
          const idx = buf.indexOf('\n')
          if (idx < 0) break
          const line = buf.slice(0, idx).replace(/\r$/, '')
          buf = buf.slice(idx + 1)
          if (line === '') {
            dispatch()
            continue
          }
          if (line.startsWith(':')) continue // 注释帧（心跳）
          if (line.startsWith('data:')) {
            dataLines.push(line.slice(5).replace(/^ /, ''))
          }
        }
      }
      onEnd?.()
    } catch (err) {
      // AbortError 是我们自己关闭（收到 run_end 或调用方卸载），不算错误。
      if ((err as { name?: string })?.name === 'AbortError') return
      onError?.(err)
    }
  })()

  return () => controller.abort()
}

/**
 * 取 access token。
 *
 * 直接读 localStorage 的两个裸键（用户登出/刷新后由 auth store 维护），
 * 与 `api` 客户端的拦截器取法保持一致——这里不能用 axios 实例，
 * 因为 SSE 需要**逐步读取响应体**，而 axios 在浏览器里拿不到流。
 */
function getAccessToken(): string | null {
  try {
    return localStorage.getItem('accessToken')
  } catch {
    return null
  }
}
