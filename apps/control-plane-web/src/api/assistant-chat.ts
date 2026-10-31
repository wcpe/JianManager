import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from './client'

/** 
 * AI 助手对话运行时的前端契约（对接的 /assistant/conversations|runs）。
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
  /** 
   * 该消息由哪一轮 run 产出。
   *
   * 判定「本轮是否已落库」要用它：失败轮（如上游不可达）**不写消息表**，
   * 只看条数或内容无法区分「已落库」与「历史里恰好有同样文字」。
   */
  runId?: number | null
  createdAt: string
  /** 首次编辑前的原文；未编辑过为 null。 */
  originalContent?: string | null
  editedAt?: string | null
  editCount?: number
  /** 被回退取代的时刻；非空表示已从会话上下文中移除但未物理删除。 */
  supersededAt?: string | null
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

/** 
 * 逐调用确认票据（§7）。
 *
 * 与对话事件分开取数：票据是**用户决策对象**，不是消息流的一部分。
 * 混进事件流会让「刷新后卡片消失」——而用户恰恰可能在刷新后才回来确认。
 */
export interface AssistantConfirmation {
  id: number
  toolName: string
  operation: string
  /** 冻结参数（票据创建时的原样快照，核对与执行都以它为准）。 */
  args: unknown
  targetType: string
  targetId: string
  expiresAt: string
  /** write / destructive。 */
  risk: string
}

/** 某 run 的待确认票据（仅所有者）。 */
export function useRunConfirmations(runId: number | null) {
  return useQuery({
    queryKey: [...CHAT_KEY, 'confirmations', runId],
    queryFn: async () => {
      const { data } = await api.get<{ items: AssistantConfirmation[] }>(
        `/assistant/runs/${runId}/confirmations`,
      )
      return data.items ?? []
    },
    enabled: runId !== null,
  })
}

/** 
 * 决定一张票据。
 *
 * 只发 `{decision}`：后端明确不接受新参数或新目标（防止「确认即改参」），
 * 因此前端也不构造其它字段——多传反而会掩盖契约意图。
 */
export function useDecideConfirmation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, decision }: { id: number; decision: 'approve' | 'reject' }) => {
      const { data } = await api.post<{ id: number; status: string }>(
        `/assistant/confirmations/${id}/decide`,
        { decision },
      )
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: [...CHAT_KEY, 'confirmations'] })
      // 确认后 run 状态会变（可能恢复执行），历史也可能更新。
      void qc.invalidateQueries({ queryKey: [...CHAT_KEY] })
    },
  })
}

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

/** 
 * 会话列表（**无限滚动**，供历史抽屉分页）。
 *
 * 与 `useAssistantConversations` 并存而不是替换它：仪表盘/浮窗首屏只要一页，
 * 用 useQuery 更简单；抽屉需要「加载更多」，才用 infinite。两者共用同一个 queryKey 前缀，
 * 失效（invalidate）时一起刷新，不会出现「抽屉里删了会话、首屏还留着」。
 */
export function useAssistantConversationsInfinite(enabled = true) {
  return useInfiniteQuery({
    queryKey: [...CHAT_KEY, 'conversations', 'infinite'],
    initialPageParam: '' as string,
    queryFn: async ({ pageParam }) => {
      const qs = pageParam ? `?cursor=${encodeURIComponent(pageParam)}` : ''
      const { data } = await api.get<{ items: AssistantConversation[]; nextCursor?: string }>(
        `/assistant/conversations${qs}`,
      )
      return { items: data.items ?? [], nextCursor: data.nextCursor ?? '' }
    },
    // 空字符串表示没有下一页（后端用空串/缺失表达结束，这里统一成 ''）。
    getNextPageParam: (last) => last.nextCursor || undefined,
    enabled,
  })
}

/** 单个会话的消息历史。 */
export function useAssistantMessages(conversationId: number | null) {
  return useQuery({
    queryKey: [...CHAT_KEY, 'messages', conversationId],
    queryFn: async () => {
      /* 
        **翻页拉全**，而不是只取后端默认的一页。

        后端 `/messages` 默认 `limit=20`（`assistantChatDefaultLimit`），长会话因此
        只显示最近 20 条、滚到顶部也看不到第 1 条——这是实测确认的可用性缺口
        （§7.4）。游标语义：`before` 是消息 **id**（后端 `WHERE id < ?`），
        每页内部按 id 升序返回，所以要用 `unshift` 把更早的一页拼到前面。

        `MAX_PAGES` 是硬上限（20 页 × 100 = 2000 条）：正常会话远达不到，
        但病态会话不该把页面拖死——超限时保留最近 2000 条，至少尾部可用。
      */
      if (conversationId === null) return []
      const PAGE = 100
      const MAX_PAGES = 20
      const all: AssistantMessage[] = []
      let before: number | undefined

      for (let page = 0; page < MAX_PAGES; page++) {
        const { data } = await api.get<{ items: AssistantMessage[] }>(
          `/assistant/conversations/${conversationId}/messages`,
          { params: { limit: PAGE, ...(before !== undefined ? { before } : {}) } },
        )
        const items = data.items ?? []
        // 更早的一页要拼在已有内容之前
        all.unshift(...items)
        if (items.length < PAGE) return all
        const earliest = items[0]?.id
        if (earliest === undefined) return all
        before = earliest
      }
      return all
    },
    enabled: conversationId !== null,
  })
}

/** 历史工具回显：某条消息当时调用的工具及其最终状态。 */
export interface AssistantToolCallRecord {
  callId: string
  name: string
  /** running / pending_confirmation / ok / failed。 */
  status: string
  errorCode?: string
  /** 调用参数原始 JSON（后端已限长）。 */
  argsJson?: string
  preview?: string
  truncated?: boolean
  seq: number
}

/** 
 * 会话内按 messageId 分组的工具调用记录。
 *
 * 这些数据一直在事件流里，此前只是没有读取面——回看历史看不到任何工具痕迹，
 * 用户无法判断「当时到底调了没有、失败在哪一步」。
 * 只回有工具调用的消息；没有的消息不出现在 map 里。
 */
export function useAssistantToolCalls(conversationId: number | null) {
  return useQuery({
    queryKey: [...CHAT_KEY, 'tool-calls', conversationId],
    queryFn: async () => {
      // 后端形状：{ byMessage: { [messageId]: [...] } }。
      // JSON 的对象键只能是字符串，取用时需按 String(id) 查。
      const { data } = await api.get<{ byMessage: Record<string, AssistantToolCallRecord[]> }>(
        `/assistant/conversations/${conversationId}/tool-calls`,
      )
      return data.byMessage ?? {}
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

// ── 历史检索、编辑与回退 ──────────────────────

/** 
 * 带关键词的会话检索（服务端 `q`）。
 *
 * **不在前端过滤全量**：会话量级随时间增长，把全部会话拉到浏览器再筛
 * 既慢又会让「搜索结果不全」变成一个难察觉的假象（只搜到了已加载的那一页）。
 */
export function useAssistantConversationSearch(keyword: string, enabled = true) {
  const q = keyword.trim()
  return useQuery({
    queryKey: [...CHAT_KEY, 'conversations', 'search', q],
    queryFn: async () => {
      const { data } = await api.get<{ items: AssistantConversation[]; nextCursor?: string }>(
        '/assistant/conversations',
        { params: q ? { q } : undefined },
      )
      return data.items ?? []
    },
    // 空关键词时不查：列表页首屏由 useAssistantConversationsInfinite 承担。
    enabled: enabled && q !== '',
  })
}

/** 编辑一条用户消息（不自动重跑）。 */
export function useEditAssistantMessage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: { messageId: number; content: string }) => {
      const { data } = await api.patch<AssistantMessage>(
        `/assistant/messages/${input.messageId}`,
        { content: input.content },
      )
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: CHAT_KEY })
    },
  })
}

/** 
 * 把会话回退到某条消息。
 *
 * **只回退会话内容，不撤销已发生的副作用**——回退到「启动实例」之前，
 * 那个实例仍然是启动着的。调用方必须在确认弹窗里把这句话写给用户看。
 */
export function useRollbackAssistantConversation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: { conversationId: number; messageId: number }) => {
      const { data } = await api.post<{ superseded: number }>(
        `/assistant/conversations/${input.conversationId}/rollback`,
        { messageId: input.messageId },
      )
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: CHAT_KEY })
    },
  })
}
