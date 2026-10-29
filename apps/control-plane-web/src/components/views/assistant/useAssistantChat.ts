import { usePageContextChips } from '@/lib/assistant/page-context'
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { clearDraftShared, getDraft, setDraftShared, subscribeDraft } from '@/lib/assistant/chat-draft'
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
import { applyEventToToolCalls } from '@/components/views/assistant/assistant-tool-status'
import type { ToolCallView } from '@/components/views/assistant/assistant-tool-status'

/** 
 * 对话状态容器。
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
  /** 
   * 本轮的工具调用状态（§5）。
   *
   * 与消息流分开存而不是塞进 `ChatMessage`：工具状态由三类事件（call/tool_status/result）
   * 按 callId 聚合而来，是多对一的，塞进消息会让「一条回复」与「若干调用」的层级关系
   * 变成平铺的一串气泡。run 结束后**刻意保留**：用户往往要在回复出来后回看「它刚跑了哪些工具」，
   * 只有开新一轮或切会话时才清空。
   */
  const [toolCalls, setToolCalls] = useState<ToolCallView[]>([])
  /** 
   * 本轮失败原因（与消息流分开）。
   *
   * 失败轮只落库 user 提问、没有 assistant 消息，因此错误提示没有「消息」可以挂——
   * 挂在乐观项上又会在清理时随之一并消失。放到容器级状态、由面板用告警条渲染，
   * 既不会重复，也不会被清掉。发送新一轮时清空。
   */
  const [runError, setRunError] = useState<string | null>(null)
  const [runId, setRunId] = useState<number | null>(null)
  const [running, setRunning] = useState(false)

  // 上下文芯片：当前页声明 + 本地移除；快照在 send 那一刻冻结（spec §5.2）。
  const {
    chips: contextChips,
    entities: contextEntities,
    addEntity: addContextEntity,
    buildSnapshot,
  } = usePageContextChips()

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

  /** 
   * 草稿（按会话保存，切会话恢复§5.1）。
   *
   * 状态放在容器而不是面板：面板随浮窗开关卸载/挂载，草稿若在面板内部，
   * 关一次浮窗就丢一次内容——正是「关闭不丢状态」要防的。
   *
   * 恢复用「渲染期检测 key 变化」而不是 `useEffect` + `setState`：
   * 后者会多渲染一帧空输入框（用户能看见草稿闪一下），且级联渲染本身是该规则
   * 明确要避免的写法。这里存的是「上次恢复的会话 id」，一旦与当前会话不符，
   * 就在渲染期直接算出新值。
   */
  const draft = useSyncExternalStore(
    subscribeDraft,
    () => getDraft(effectiveConversationId),
    () => '',
  )

  /** 改草稿：写入共享 store（同标签页内所有视图立即同步，并落 sessionStorage）。 */
  const setDraft = useCallback(
    (text: string) => setDraftShared(effectiveConversationId, text),
    [effectiveConversationId],
  )

  /** 切换会话/卸载：断开旧订阅，清掉流式态。 */
  const resetStream = useCallback(() => {
    abortRef.current?.()
    abortRef.current = null
    lastSeq.current = 0
    setStreamingMessages([])
    // 工具状态属于某一轮 run：换会话后继续显示上一条会话的调用记录是纯粹的误导。
    setToolCalls([])
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
  const observeRef = useRef<
    ((id: number, key: string, retried?: boolean, userKey?: string) => void) | null
  >(null)

  const observe = useCallback(
    (targetRunId: number, runKey: string, retried = false, userKey?: string) => {
      abortRef.current?.()
      abortRef.current = streamRunEvents({
        runId: targetRunId,
        after: lastSeq.current,
        onEvent: (frame: AssistantEventFrame) => {
          if (typeof frame.seq === 'number' && frame.seq > lastSeq.current) {
            lastSeq.current = frame.seq
          }
          setStreamingMessages((prev) => applyEventToMessages(prev, frame, runKey))
          // 工具事件（call/status/result）走这里聚合。非工具帧原样返回同一引用，
          // 因此文本流不会连带工具列表一起重渲染。
          setToolCalls((prev) => applyEventToToolCalls(prev, frame))
          if (frame.type === 'run_end') {
            setRunning(false)
            setRunId(null)
            // run 结束后把历史拉一遍：服务端的消息表才是真源，
            // 本地的流式拼接只是观感（可能因断线漏掉中间片段）。
            //
            // **必须在历史到位后再清流式**：两者是直接拼接的（见 messages 的 useMemo），
            // 先清会让回复闪空一下，先不清理则重复显示同一条回复——真实供应商链路实测
            // 出现过「一轮问答显示两遍」。因此这里等 refetch 落定，再把本轮流式丢掉。
            void refetchHistory()
              .then(() => {
                // 历史已包含本轮的 user 提问（后端对成功轮与失败轮都会写它），
                // 因此本地的乐观副本一律可以丢掉——**必须丢**，否则同一句提问显示两遍
                // （真实链路实测：失败重生一次就多一条并排的提问）。
                //
                // 但失败轮的 assistant 项承载着错误提示（`note`），直接丢会让用户
                // 看不到失败原因。因此先把提示提取出来转存到容器级 `runError`，
                // 由面板用独立的告警条渲染，而不是留在消息流里。
                setStreamingMessages((prev) => {
                  const mine = prev.find((m) => m.key === runKey)
                  const note = mine?.note
                  if (note) setRunError(note)
                  return prev.filter((m) => m.key !== runKey && m.key !== userKey)
                })
              })
              .catch(() => {
                // refetch 失败就别动本地副本：宁可短暂重复，也不能让内容凭空消失。
              })
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
          observeRef.current?.(targetRunId, runKey, true, userKey)
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
      //
      // 两个 key 用同一个时间戳后缀：分开取两次 `Date.now()` 在同一毫秒内会得到相同值，
      // 一旦 `u-<ts>` 与 `run-<ts>` 相撞，run 结束时的清理就会误删不该删的那条。
      const turnStamp = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
      const userKey = `u-${turnStamp}`
      const runKey = `run-${turnStamp}`
      // 新一轮开始：上一轮的错误提示不该继续显示。
      setRunError(null)

      // 提交前先丢掉**上一轮遗留**的乐观项。
      //
      // 失败轮（上游不可达等）不写消息表，因此它的乐观项永远不会被「已落库」清理掉
      // （见 run_end 处的判据）。不在这里清掉，它们会一轮轮累积在列表里——
      // 实测：连发三次失败请求，界面上就出现三条并排的提问。
      // 此刻还没开始新一轮，历史就是唯一真源，清空是安全的。
      setStreamingMessages([
        { key: userKey, role: 'user', content: text },
        { key: runKey, role: 'assistant', content: '', streaming: true },
      ])

      // 发出即清草稿（同时清 sessionStorage 副本，否则刷新后又填回输入框）。
      clearDraftShared(convId)
      if (convId !== effectiveConversationId) clearDraftShared(effectiveConversationId)

      // 新一轮开始：上一轮的工具调用记录属于上一轮，留着会让人以为是本轮在跑的。
      setToolCalls([])

      idemKey.current = newIdempotencyKey()
      setRunning(true)
      lastSeq.current = 0
      try {
        const accepted = await submitRun.mutateAsync({
          conversationId: convId,
          text,
          // 发送这一刻冻结上下文：之后用户在页面上换筛选，不该影响已发出的这一轮。
          contextSnapshot: buildSnapshot(),
          idempotencyKey: idemKey.current,
        })
        setRunId(accepted.runId)
        observe(accepted.runId, runKey, false, userKey)
      } catch (err) {
        setRunning(false)
        const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
        toast.error(msg || t('assistant.chatSendFailed', '发送失败'))
      }
    },
    [buildSnapshot, createConversation, effectiveConversationId, enabled, observe, running, submitRun, t],
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
    draft,
    setDraft,
    contextChips,
    contextEntities,
    addContextEntity,
    runError,
    /** 当前 run（供确认卡拉取票据）。 */
    runId,
    /** 本轮工具调用状态（按 callId 聚合，run 结束后保留到下轮开始）。 */
    toolCalls,
    selectConversation,
    startNewConversation,
    isLoadingHistory: messagesQuery.isLoading,
  }
}
