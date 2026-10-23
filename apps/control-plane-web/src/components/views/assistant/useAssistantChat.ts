import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  newIdempotencyKey,
  streamRunEvents,
  useAssistantConversations,
  useAssistantMessages,
  useCancelRun,
  useCreateConversation,
  useSubmitRun,
} from '@/api/assistant-chat'
import type { AssistantEventFrame } from '@/api/assistant-chat'
import { useAssistantModels } from '@/api/assistant'
import { applyEventToMessages } from '@/components/views/assistant/assistant-chat-events'
import type { ChatMessage } from '@/components/views/assistant/assistant-chat-types'

/**
 * 对话状态容器（FR-519）。
 *
 * 取数、提交、事件流与断线补读都在这里；`AssistantChatPanel` 只负责渲染。
 * 这里集中了几处**必须一次做对、否则会重复回话**的语义：
 *
 *  1. **幂等键在一轮内固定**：重试沿用同一个键，后端才不会产出第二条回复。
 *     （同键同内容回原 run；同键异内容 409。）
 *  2. **断线只补读、不重提**：用最后收到的 seq 重新订阅事件流。重提会产生新 run。
 *  3. **卸载/切换会话必须 abort**：否则旧 run 的事件会串到新会话的列表里。
 *  4. **停止 ≠ 关闭**：停止调 cancel，关闭面板只是不渲染。
 */
export function useAssistantChat(enabled: boolean) {
  const { t } = useTranslation()
  const conversations = useAssistantConversations(enabled)
  const [conversationId, setConversationId] = useState<number | null>(null)
  const modelsQuery = useAssistantModels()
  const createConversation = useCreateConversation()
  const submitRun = useSubmitRun()
  const cancelRun = useCancelRun()

  /** 流式消息（当前 run 的增量）。与历史消息分开存，避免历史分页把流式内容冲掉。 */
  const [streamingMessages, setStreamingMessages] = useState<ChatMessage[]>([])
  const [runId, setRunId] = useState<number | null>(null)
  const [running, setRunning] = useState(false)
  /** 已收到的最大 seq（断线补读的游标）。 */
  const lastSeq = useRef(0)
  /** 当前订阅的 abort 句柄。 */
  const abortRef = useRef<(() => void) | null>(null)
  /** 本轮幂等键：一轮内固定，重试用同一个。 */
  const idemKey = useRef<string>('')

  /**
   * 当前会话 = 显式选择优先，否则取最近一个（派生，不在 effect 里 setState）。
   *
   * 早先版本用 effect 把「列表第一条」写进 state，那会在每次列表刷新时触发一次
   * 额外渲染，并且一旦用户点「新会话」（id=null）就会被 effect 立刻拽回旧会话——
   * 表现是「点了新会话又跳回去」。派生值没有这个问题：新会话时用户显式把 id 置空，
   * 而「置空」需要与「还没选过」区分，故用一个独立的 `pickedNew` 标记。
   */
  const [pickedNew, setPickedNew] = useState(false)
  const effectiveConversationId = useMemo(() => {
    if (pickedNew) return null
    if (conversationId !== null) return conversationId
    return conversations.data?.[0]?.id ?? null
  }, [conversationId, conversations.data, pickedNew])

  const messagesQuery = useAssistantMessages(effectiveConversationId)
  /** 历史消息的 refetch（稳定引用，供 observe 在 run 结束后回读真源）。 */
  const refetchHistory = messagesQuery.refetch

  /** 切换会话/卸载：断开旧订阅，清掉流式态。 */
  const resetStream = useCallback(() => {
    abortRef.current?.()
    abortRef.current = null
    lastSeq.current = 0
    setStreamingMessages([])
    setRunId(null)
    setRunning(false)
  }, [])

  useEffect(() => () => abortRef.current?.(), [])

  /**
   * 订阅某 run 的事件（带一次自动补读）。
   *
   * 断线表现为 onError：此时用 lastSeq 重连一次。只重试一次——连续失败说明
   * 服务端或网络确实不可用，无限重试只会把浏览器连接池耗干，且用户看不到原因。
   */
  /** observe 的自引用句柄：递归重连时经它调用，避免 useCallback 里引用自身。 */
  const observeRef = useRef<((id: number, key: string, retried?: boolean) => void) | null>(null)

  const observe = useCallback(
    (targetRunId: number, runKey: string, retried = false) => {
      abortRef.current?.()
      abortRef.current = streamRunEvents({
        runId: targetRunId,
        after: lastSeq.current,
        onEvent: (frame: AssistantEventFrame) => {
          if (typeof frame.seq === 'number' && frame.seq > lastSeq.current) {
            lastSeq.current = frame.seq
          }
          setStreamingMessages((prev) => applyEventToMessages(prev, frame, runKey))
          if (frame.type === 'run_end') {
            setRunning(false)
            setRunId(null)
            // run 结束后把历史拉一遍：服务端的消息表才是真源，
            // 本地的流式拼接只是观感（可能因断线漏掉中间片段）。
            void refetchHistory()
          }
        },
        onEnd: () => setRunning(false),
        onError: (err) => {
          if (retried) {
            setRunning(false)
            toast.error(
              t('assistant.chatStreamLost', '连接中断，点击重试观察（不会重新生成回复）'),
            )
            return
          }
          // 一次性补读：用当前 seq 继续，不重新提交 run。
          observeRef.current?.(targetRunId, runKey, true)
          void err
        },
      })
    },
    [refetchHistory, t],
  )

  // 把最新的 observe 挂到 ref 上（递归重连才拿得到当前闭包）。
  // 用 effect 而不是渲染期赋值：渲染必须是纯的，渲染期写 ref 会在并发渲染下
  // 提前生效（React 可能丢弃这次渲染），让「重连用的是哪一版闭包」变得不可预测。
  useEffect(() => {
    observeRef.current = observe
  }, [observe])

  /** 发送一轮。 */
  const send = useCallback(
    async (text: string) => {
      if (!enabled || running) return
      // 无会话时先建一个：让「打开就能问」成为可能，不必先去点「新会话」。
      let convId = effectiveConversationId
      if (convId === null) {
        try {
          const created = await createConversation.mutateAsync({ title: text.slice(0, 40) })
          convId = created.id
          setConversationId(created.id)
          setPickedNew(false)
        } catch {
          toast.error(t('assistant.chatCreateFailed', '创建会话失败'))
          return
        }
      }

      // 乐观插入用户消息（服务端也会存，但等它回来再显示会很卡）。
      const userKey = `u-${Date.now()}`
      setStreamingMessages((prev) => [
        ...prev,
        { key: userKey, role: 'user', content: text },
        { key: `run-${Date.now()}`, role: 'assistant', content: '', streaming: true },
      ])
      const runKey = `run-${Date.now()}`

      idemKey.current = newIdempotencyKey()
      setRunning(true)
      lastSeq.current = 0
      try {
        const accepted = await submitRun.mutateAsync({
          conversationId: convId,
          text,
          idempotencyKey: idemKey.current,
        })
        setRunId(accepted.runId)
        observe(accepted.runId, runKey)
      } catch (err) {
        setRunning(false)
        const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
        toast.error(msg || t('assistant.chatSendFailed', '发送失败'))
      }
    },
    [createConversation, effectiveConversationId, enabled, observe, running, submitRun, t],
  )

  /** 显式停止（与关闭浮窗无关）。 */
  const stop = useCallback(() => {
    if (runId === null) {
      // runId 还没回来（提交在途）：只能断开观察，服务端的 run 会自己跑完。
      abortRef.current?.()
      abortRef.current = null
      setRunning(false)
      return
    }
    cancelRun.mutate(
      { runId },
      {
        onSettled: () => {
          // 终态由服务端的 run_end 决定；这里只断开本地观察，不假设结果。
          setRunning(false)
        },
      },
    )
  }, [cancelRun, runId])

  const selectConversation = useCallback(
    (id: number) => {
      if (id === effectiveConversationId) return
      resetStream()
      setPickedNew(false)
      setConversationId(id)
    },
    [effectiveConversationId, resetStream],
  )

  const startNewConversation = useCallback(async () => {
    resetStream()
    setConversationId(null)
    setPickedNew(true)
    setStreamingMessages([])
  }, [resetStream])

  /** 历史 + 流式合并成一条渲染用列表。 */
  const messages = useMemo<ChatMessage[]>(() => {
    const history = (messagesQuery.data ?? []).map<ChatMessage>((m) => ({
      key: `h-${m.id}`,
      role: m.role === 'user' ? 'user' : 'assistant',
      content: m.content,
    }))
    // 流式消息追加在历史之后：历史来自服务端消息表，流式是本轮增量。
    return [...history, ...streamingMessages]
  }, [messagesQuery.data, streamingMessages])

  /** 当前选中的模型与它的供应商（用于「数据发给谁」的告知）。 */
  const selectedModel = useMemo(() => {
    const list = modelsQuery.data?.items ?? []
    return list[0] ?? null
  }, [modelsQuery.data])

  return {
    conversations: conversations.data ?? [],
    conversationId: effectiveConversationId,
    messages,
    running,
    /** 无可用模型时禁用对话并给出引导（spec §6「模型不可用」）。 */
    noModel: enabled && !modelsQuery.isLoading && (modelsQuery.data?.items?.length ?? 0) === 0,
    providerName: selectedModel?.providerName ?? null,
    modelName: selectedModel?.displayName || selectedModel?.modelId || null,
    send,
    stop,
    selectConversation,
    startNewConversation,
    isLoadingHistory: messagesQuery.isLoading,
  }
}
