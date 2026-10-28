import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { AlertCircle, ArrowUp, Square, Sparkles } from 'lucide-react'
import {
  Alert,
  AlertDescription,
  Button,
  Panel,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui'
import { Textarea } from '@jianmanager/ui/components/textarea'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import type { ChatMessage } from '@/components/views/assistant/assistant-chat-types'
import { useVirtualRows } from '@/lib/shared/virtual-list'
import { AddEntityButton } from '@/components/views/assistant/AddEntityButton'
import { ConfirmationCards } from '@/components/views/assistant/ConfirmationCards'
import { ContextChip } from '@/components/views/assistant/ContextChip'
import { ToolStatusList } from '@/components/views/assistant/ToolStatusList'
import type { ToolCallView } from '@/components/views/assistant/assistant-tool-status'
import type { PageContextEntityRef } from '@/lib/assistant/page-context'
import { isServerDrag, readServerDragId } from '@/lib/assistant/server-drag'
import { isSafeExternalLink, confirmOpenMessage } from '@/lib/system-update/release-notes-link'

/**
 * 单条消息气泡的**估算高度**（px），仅用于尚未测量过的消息。
 *
 * 为什么需要估算：变高虚拟化要在首次渲染前就知道总高，才能算出滚动窗口与占位。
 * 常见气泡（一两行文字带内边距）在 1280 宽下约 60～90px，取中位数 80。
 * 估算只影响**未渲染过**的消息；一旦滚到就换成实测值，误差不会累积到可视区。
 */
const ESTIMATED_BUBBLE_HEIGHT = 80

/**
 * 聊天消息流与输入器（FR-519 §5）。
 *
 * 这个组件刻意**不取数、不发请求**（ADR-102 b 范式）：会话、run、事件流都由容器
 * 经 props 注入。原因不只是分层口味——聊天状态机（流式追加、取消、断线补读）
 * 是很容易出竞态的地方，把它和取数混在一个组件里，测试就只能靠假后端才敢碰。
 */

/**
 * Markdown 渲染器（聊天用）。
 *
 * 与 ReleaseNotes 的配置**有意不同**：那里 `img` 会直接加载远程图片，而那里的
 * 外链确认只覆盖 `<a>`。聊天内容来自模型输出，属不可信输入——若沿用那条配置，
 * 模型只要输出 `![](https://evil/x.png)`，浏览器就会**在无人点击的情况下**自动发起请求，
 * 泄漏用户 IP 与阅读行为。因此这里**完全不渲染图片**（spec §5.2 的默认取舍）。
 * 同样不引入 iframe/video/audio 或任何远程子资源。
 */
const createChatMarkdownComponents = (onExternalLink: (href: string) => void): Components => ({
  p: ({ children }) => <p className="my-1.5 leading-relaxed first:mt-0 last:mb-0">{children}</p>,
  ul: ({ children }) => <ul className="my-1.5 ml-4 list-disc space-y-0.5">{children}</ul>,
  ol: ({ children }) => <ol className="my-1.5 ml-4 list-decimal space-y-0.5">{children}</ol>,
  li: ({ children }) => <li className="leading-relaxed">{children}</li>,
  blockquote: ({ children }) => (
    <blockquote className="my-1.5 border-l-2 border-border pl-3 text-muted-foreground">{children}</blockquote>
  ),
  code: ({ children, className }) => (
    <code className={`rounded bg-background/70 px-1 py-0.5 font-mono text-[0.85em] ${className ?? ''}`}>
      {children}
    </code>
  ),
  pre: ({ children }) => (
    <pre className="my-2 overflow-auto rounded-md bg-background/70 p-3 font-mono text-xs leading-relaxed">
      {children}
    </pre>
  ),
  // 表格交给设计系统（自带横向滚动容器；markdown 表格需要每格边框，故覆盖默认的 border-separate）。
  table: ({ children }) => (
    <Table className="border-collapse text-xs" containerClassName="my-2">
      {children}
    </Table>
  ),
  thead: ({ children }) => <TableHeader>{children}</TableHeader>,
  tbody: ({ children }) => <TableBody>{children}</TableBody>,
  tr: ({ children }) => <TableRow className="border-b-0 hover:bg-transparent">{children}</TableRow>,
  th: ({ children }) => (
    <TableHead className="border border-border px-2 py-1">{children}</TableHead>
  ),
  td: ({ children }) => <TableCell className="border border-border px-2 py-1">{children}</TableCell>,
  // 图片：不渲染（见上）。给一个纯文本占位，让用户知道这里原本有图，
  // 而不是静默丢掉——静默丢掉会让人以为模型没输出。
  img: ({ alt }) => (
    <span className="text-xs text-muted-foreground">[图片已省略{alt ? `：${alt}` : ''}]</span>
  ),
  // 外链：不在应用内直跳；也**不直接开新标签**——先经宿主确认（spec §7「经宿主确认才在新标签打开」）。
  // 只拦 http/https 绝对地址：相对链接与锚点属站内导航，不代表要离开宿主，交给默认行为。
  a: ({ children, href }) => (
    <a
      href={typeof href === 'string' ? href : undefined}
      target="_blank"
      rel="noopener noreferrer nofollow"
      className="text-primary underline underline-offset-2"
      onClick={(e) => {
        if (typeof href === 'string' && isSafeExternalLink(href)) {
          e.preventDefault()
          onExternalLink(href)
        }
      }}
    >
      {children}
    </a>
    ),
    })

    /** 单条消息气泡。 */
    function MessageBubble({
    message,
    onExternalLink,
    }: {
    message: ChatMessage
    /** 点击外链时上交宿主确认（不在气泡里直接开新标签）。 */
    onExternalLink: (href: string) => void
    }) {
    const { t } = useTranslation()
    const isUser = message.role === 'user'
    const markdownComponents = useMemo(() => createChatMarkdownComponents(onExternalLink), [onExternalLink])
  return (
    <div className={isUser ? 'flex justify-end' : 'flex justify-start'}>
      <div
        className={
          isUser
            ? 'max-w-[85%] rounded-lg bg-primary px-3 py-2 text-sm text-primary-foreground'
            : 'max-w-[92%] rounded-lg border border-border/60 bg-muted/40 px-3 py-2 text-sm'
        }
      >
        {isUser ? (
          // 用户输入按纯文本显示：不把用户自己写的 Markdown 渲染成结构，
          // 否则「我以为我在发一段代码」会被渲染成代码块，反而看不清原文。
          //
          // `break-words`（overflow-wrap:break-word）不可省：工具参数、路径、URL 这类
          // **长不可断 token** 在窄屏会超出气泡宽度被裁掉，且会把消息列表撑出隐藏的
          // 横向滚动（实测 375px 视口下气泡 259px、文本 282px，列表可横滚 27px）。
          // 用户粘贴的长串同样会触发，故用户气泡也要加。
          <p className="whitespace-pre-wrap break-words leading-relaxed">{message.content}</p>
        ) : message.streaming ? (
          // 流式期间按纯文本追加：未闭合的 ``` 或 * 会被渲染成半截结构。
          <p className="whitespace-pre-wrap break-words leading-relaxed">
            {message.content}
            <span className="ml-0.5 inline-block h-4 w-1.5 animate-pulse bg-foreground/60 align-middle" />
          </p>
        ) : (
          <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>
            {message.content}
          </ReactMarkdown>
        )}
        {message.note ? (
          <p className="mt-1 text-xs text-muted-foreground">{message.note}</p>
        ) : null}
        {message.streaming && message.content === '' ? (
          <p className="text-xs text-muted-foreground">{t('assistant.chatThinking', '正在生成…')}</p>
        ) : null}
      </div>
    </div>
  )
}

export interface AssistantChatPanelProps {
  messages: ChatMessage[]
  /** run 进行中：禁用发送、显示停止。 */
  running: boolean
  /** 可用模型（用于「发送给谁」的告知）。 */
  providerName?: string | null
  modelName?: string | null
  /** 错误/空态提示（模型未配置、连接中断等）。 */
  hint?: string | null
  /**
   * 本轮失败原因（如上游不可达）。
   *
   * 单独传而不是塞进消息流：失败轮没有 assistant 消息可挂，而乐观项会被清理。
   * 与 `hint`（配置类引导，如「尚未配置模型」）分开渲染，避免两件事互相顶掉。
   */
  runError?: string | null
  onSend: (text: string) => Promise<void> | void
  onStop: () => void
  /**
   * 当前会话的草稿（受控）。传 undefined 表示不启用草稿持久化。
   *
   * 草稿由容器持有而不是面板内部 state：面板会随浮窗开关卸载/挂载，
   * 若草稿在面板里，关一次浮窗就丢一次内容（正是 spec §5.4「关闭不丢状态」要防的）。
   */
  draft?: string
  onDraftChange?: (text: string) => void
  /**
   * 页面上下文芯片（FR-518 §5.2）。消费端已就绪，注册端属 FR-518 自身实现。
   *
   * 每项可移除，移除即不再外发该项（shared-contracts §6 第 5 条）——
   * 所以这里只渲染调用方给的数组，不缓存、不补全。
   */
  contextChips?: Array<{
    key: string
    label: string
    onRemove: () => void
    /**
     * 该芯片对应的实体（可选）。
     *
     * 只有实例类芯片会给：它让芯片能就地展开指标摘要、并提供跳转到实例详情的入口。
     * 不带实体的芯片（如页面级上下文）仍按纯标签渲染——**不做类型猜测**：
     * 从 label 反解 id 是「一改文案就坏」的做法，与上面用 contextEntities 的理由相同。
     */
    entity?: PageContextEntityRef
  }>
  /**
   * 已附加的实体（FR-518 §5.3）：用于「添加实体」的去重与上限判定。
   *
   * 与 `contextChips` 分开传而不是从芯片反解：芯片是展示用标签（已本地化、可能被
   * 移除过），实体是外发内容。把展示态反解成数据态是典型的「看起来能用、
   * 一改文案就坏」做法。
   */
  contextEntities?: PageContextEntityRef[]
  /** 追加一个实体（由容器写回上下文）。 */
  onAddContextEntity?: (entity: PageContextEntityRef) => void
  /** 当前观察的 run（用于拉取待确认票据；null 表示没有进行中的 run）。 */
  confirmationRunId?: number | null
  /** 用户决策后的回调（容器据此继续观察或刷新）。 */
  onConfirmationDecided?: (id: number, decision: 'approve' | 'reject') => void
  /**
   * 本轮的工具调用状态（FR-519 §5）。
   *
   * 由容器从 `useAssistantChat` 取出后原样传入——面板仍然只渲染、不聚合：
   * 聚合依赖 run 的事件流，而事件流属于容器（面板拿不到、也不该去订阅）。
   * 可选，缺省即不渲染：没有工具调用的普通问答不该多出一块空壳。
   */
  toolCalls?: ToolCallView[]
  /**
   * 芯片指标是否取数（缺省 true）。
   *
   * 由容器按浮窗可见性传入：面板在浮窗收起时**仍挂载**，若无条件取数，
   * 隐藏的聊天框会持续每枚芯片发一次指标请求。
   */
  chipMetricsEnabled?: boolean
  /** 内容变化时把滚动位置固定到底部（只在用户本来就在底部时才自动跟随）。 */
  className?: string
}

/** 聊天面板：消息流 + 输入器。 */
export function AssistantChatPanel({
  messages,
  running,
  providerName,
  modelName,
  hint,
  runError,
  onSend,
  onStop,
  draft: draftProp,
  onDraftChange,
  contextChips,
  contextEntities,
  onAddContextEntity,
  confirmationRunId,
  onConfirmationDecided,
  toolCalls,
  chipMetricsEnabled = true,
  className,
}: AssistantChatPanelProps) {
  const { t } = useTranslation()
  /** 内部草稿：仅当调用方不接管控件时使用（保持无草稿需求的用法不变）。 */
  const [innerDraft, setInnerDraft] = useState('')
  const draft = draftProp ?? innerDraft
  const setDraft = (text: string) => {
    if (draftProp === undefined) setInnerDraft(text)
    onDraftChange?.(text)
  }
  const scrollRef = useRef<HTMLDivElement | null>(null)
  /** 用户是否停在底部：滚上去看历史时不要被强制拽回底部。 */
  const stickToBottom = useRef(true)

  // 服务器拖入（侧边栏 → 聊天框，FR-518/519 联动）。
  //
  // 用**计数**而不是布尔：dragenter/dragleave 会在子元素之间反复触发，
  // 布尔值会在指针掠过内部节点时闪烁，表现为高亮一闪一闪。
  const [dragDepth, setDragDepth] = useState(0)
  const dragging = dragDepth > 0

  const onDragEnter = useCallback((e: React.DragEvent) => {
    if (!isServerDrag(e.dataTransfer?.types)) return
    e.preventDefault()
    setDragDepth((d) => d + 1)
  }, [])
  const onDragOver = useCallback((e: React.DragEvent) => {
    // dragover 必须 preventDefault，否则 drop 事件根本不会触发
    //（`types` 在 dragover 阶段可读，`getData` 不可读，故判据只能用 types）。
    if (!isServerDrag(e.dataTransfer?.types)) return
    e.preventDefault()
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
  }, [])
  const onDragLeave = useCallback((e: React.DragEvent) => {
    if (!isServerDrag(e.dataTransfer?.types)) return
    setDragDepth((d) => Math.max(0, d - 1))
  }, [])
  const onDrop = useCallback(
    (e: React.DragEvent) => {
      if (!isServerDrag(e.dataTransfer?.types)) return
      e.preventDefault()
      setDragDepth(0)
      const id = readServerDragId(e.dataTransfer)
      // 拖进来只是「附带为上下文」，不改输入框文字：用户那句话还没说，
      // 先往输入框塞文本会打断正在写的句子（这正是选芯片而不是 @ 提及的理由）。
      if (id > 0) onAddContextEntity?.({ type: 'instance', id })
    },
    [onAddContextEntity],
  )

  const canSend = useMemo(() => draft.trim() !== '' && !running, [draft, running])
  // 外链待确认地址：模型输出不可信，点击不得直接开新标签（spec §7 末条）。
  const [externalHref, setExternalHref] = useState<string | null>(null)
  const openExternal = useCallback(() => {
    if (!externalHref) return
    window.open(externalHref, '_blank', 'noopener,noreferrer')
    setExternalHref(null)
  }, [externalHref])

  const send = useCallback(async () => {
    const text = draft.trim()
    if (text === '' || running) return
    stickToBottom.current = true
    // 草稿清空交给容器：它要同时清掉 sessionStorage 里的副本，
    // 否则刷新后又把刚发出去的内容填回输入框。
    await onSend(text)
  }, [draft, running, onSend])

  // ── 长会话虚拟化（FR-519 §7）──────────────────────────────────────────
  //
  // 实测过的缺口：500 条会话只渲染最近 20 条（后端默认 limit=20），滚到顶部看不到第 1 条。
  // 数据侧已改为翻页拉全（useAssistantMessages）；渲染侧用变高虚拟窗口，只挂载可视区内的气泡。
  //
  // 【为什么用 `sizes` 变高模式】消息高度天然不等（Markdown 列表/代码块/表格），
  // 用等高 `itemSize` 定位会随行数累积偏差——项目护栏明确要求 itemSize 与真实行盒严格一致。
  // 这里给每条消息维护一个**实测高度**，未测得（尚未渲染过）的先按估算，滚到时测量并回填。
  const [measuredHeights, setMeasuredHeights] = useState<Record<string, number>>({})
  const bubbleRefs = useRef(new Map<string, HTMLDivElement>())

  const bubbleSizes = useMemo(
    () => messages.map((m) => measuredHeights[m.key] ?? ESTIMATED_BUBBLE_HEIGHT),
    [messages, measuredHeights],
  )

  const virtual = useVirtualRows({
    total: messages.length,
    itemSize: ESTIMATED_BUBBLE_HEIGHT,
    sizes: bubbleSizes,
    overscan: 4,
    // 复用组件自己的 scrollRef：「贴底跟随」与虚拟窗口必须盯同一个滚动容器
    containerRef: scrollRef,
  })

  /*
    渲染后测量真实高度。

    两处必须小心，否则会踩出级联渲染或死循环：
    1. **依赖里要有 `measuredHeights`**：测量结果回填后需要再算一次窗口，
       且「无变化就不 setState」是收敛的——高度稳定后 effect 不再触发。
    2. **推迟到下一帧再 setState**：在本 effect 里同步 setState 会触发
       `react-hooks` 的 "calling setState synchronously within an effect" 规则，
       也确实会多阻塞一帧绘制。用 rAF 把回填挪到绘制之后。
  */
  const messagesKey = useMemo(() => messages.map((m) => m.key).join('\u0000'), [messages])
  useLayoutEffect(() => {
    const raf = requestAnimationFrame(() => {
      let changed = false
      const next: Record<string, number> = { ...measuredHeights }
      bubbleRefs.current.forEach((el, key) => {
        const h = el.offsetHeight
        if (h > 0 && Math.abs((next[key] ?? 0) - h) > 1) {
          next[key] = h
          changed = true
        }
      })
      if (changed) setMeasuredHeights(next)
    })
    return () => cancelAnimationFrame(raf)
  }, [messagesKey, measuredHeights])
  // 只在「贴着底部」时跟随，避免用户向上翻阅时被拉回。
  // 工具状态列表也在滚动区内，故一并作为触发源：新出现的工具行同样该被带进视野。
  useEffect(() => {
    const el = scrollRef.current
    if (!el || !stickToBottom.current) return
    el.scrollTop = el.scrollHeight
  }, [messages, toolCalls])

  const onScroll = () => {
    const el = scrollRef.current
    // 虚拟窗口要跟着滚动位置更新可视区（内部已按 rAF 合并同帧多次事件）。
    virtual.onScroll()
    if (!el) return
    // 留 24px 容差：亚像素与圆角会让 scrollHeight 差一点点。
    stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  }

  return (
    <>
    <div
      className={
        // 拖拽中给出明确可见反馈：用户从侧边栏拖过来时，没有高亮就不知道「能不能放这儿」。
        // ring 用 inset 形式（ring-inset）避免被父容器 overflow 裁掉。
        `${className ?? 'flex min-h-0 flex-1 flex-col'}${
          dragging ? ' rounded-md ring-2 ring-inset ring-primary/60' : ''
        }`
      }
      onDragEnter={onDragEnter}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
      data-assistant-drop-target={dragging ? 'active' : undefined}
      aria-label={
        dragging
          ? t('assistant.dropServerHint', '松开即把该服务器加入本轮上下文')
          : undefined
      }
    >
      {/* 消息流：role=log + aria-live=polite（spec §5.5）。
          逐 token 不播报——流式期间 text_delta 每秒可能几十条，播报会把屏幕阅读器刷屏；
          播报粒度交给浏览器按「内容稳定」自行决定即可。 */}
      <div
        ref={scrollRef}
        onScroll={onScroll}
        role="log"
        aria-live="polite"
        aria-label={t('assistant.chatLog', '对话消息')}
        // 不再用 `space-y-3` 做行距：它不计入行高，虚拟化按行盒定位时会把间距漏算，
        // 行数一多就累积成可见漂移（项目护栏明确要求把间距折进行盒）。
        className="min-h-0 flex-1 overflow-y-auto px-1 py-2"
        >
        {messages.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center gap-2 px-4 text-center">
            <Sparkles className="size-6 text-muted-foreground" />
            <p className="text-sm font-medium">{t('assistant.chatEmptyTitle', '问我任何运维问题')}</p>
            <p className="max-w-sm text-xs text-muted-foreground">
              {t(
                'assistant.chatEmptyHint',
                '例如「哪些实例最近崩溃过」「帮我看看这台机器的内存趋势」。',
              )}
            </p>
          </div>
        ) : (
          <>
            {/* 可视区上方未挂载的消息，用等高占位撑出滚动条位置 */}
            {virtual.range.before > 0 ? (
              <div style={{ height: virtual.range.before }} aria-hidden />
            ) : null}
            {messages.slice(virtual.range.start, virtual.range.end).map((m) => (
              <div
                key={m.key}
                ref={(el) => {
                  if (el) bubbleRefs.current.set(m.key, el)
                  else bubbleRefs.current.delete(m.key)
                }}
                // 行距折进行盒：pb-3 会计入 offsetHeight，虚拟化量到的就是真实占位
                className="pb-3"
              >
                <MessageBubble message={m} onExternalLink={setExternalHref} />
              </div>
            ))}
            {virtual.range.after > 0 ? (
              <div style={{ height: virtual.range.after }} aria-hidden />
            ) : null}
          </>
        )}
        {/* 工具状态紧跟在消息流末尾（= 当前 run 之后）：它是本轮正在发生的事，
            放在输入框上方那条带子里会与「上一条回复」的视觉归属脱节。
            它不进虚拟窗口——本轮工具状态数量有限，且必须始终可见。 */}
        <ToolStatusList items={toolCalls} />
      </div>

      {confirmationRunId ? (
        <ConfirmationCards runId={confirmationRunId} onDecided={onConfirmationDecided} />
      ) : null}
      {runError ? (
        <Alert variant="destructive" className="mb-2">
          <AlertCircle className="size-4" />
          <AlertDescription className="text-xs">{runError}</AlertDescription>
        </Alert>
      ) : null}
      {hint ? (
        <Alert variant="warning" className="mx-1 my-2">
          <AlertDescription>{hint}</AlertDescription>
        </Alert>
      ) : null}

      {/* 上下文芯片（FR-518 §5.2）：放在输入框上方一行；每项可单独移除。
          「添加实体」也在这条带子里——它属于同一件事（本轮要带什么上下文）。 */}
      {onAddContextEntity ? (
        <div className="flex flex-wrap items-center gap-1.5 px-1 pb-1">
          <AddEntityButton entities={contextEntities ?? []} onAdd={onAddContextEntity} />
        </div>
      ) : null}
      {contextChips && contextChips.length > 0 ? (
        <div className="flex flex-wrap items-center gap-1.5 px-1 pb-1">
          {contextChips.map((chip) => (
            <ContextChip
              key={chip.key}
              chipKey={chip.key}
              label={chip.label}
              onRemove={chip.onRemove}
              entity={chip.entity}
              // 容器可传 false 暂停取数（如浮窗收起但未卸载时）。
              // 缺省视为可见——面板自己不知道浮窗状态，不臆造。
              metricsEnabled={chipMetricsEnabled}
            />
          ))}
        </div>
      ) : null}

      {/* 输入器（spec §5.1）：Enter 发送 / Shift+Enter 换行。 */}
      <div className="border-t border-border/60 px-1 pt-2">
        <div className="flex items-end gap-2">
          <Textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault()
                void send()
              }
            }}
            rows={1}
            aria-label={t('assistant.chatInput', '输入消息')}
            placeholder={
              running
                ? t('assistant.chatGenerating', '生成中…可点停止')
                : t('assistant.chatPlaceholder', '输入问题，Enter 发送，Shift+Enter 换行')
            }
            className="max-h-40 min-h-9 flex-1 resize-none"
          />
          {running ? (
            <Button
              type="button"
              variant="outline"
              size="icon"
              aria-label={t('assistant.chatStop', '停止生成')}
              title={t('assistant.chatStop', '停止生成')}
              onClick={onStop}
            >
              <Square className="size-4" />
            </Button>
          ) : (
            <Button
              type="button"
              size="icon"
              aria-label={t('assistant.chatSend', '发送')}
              title={t('assistant.chatSend', '发送')}
              disabled={!canSend}
              onClick={() => void send()}
            >
              <ArrowUp className="size-4" />
            </Button>
          )}
        </div>
        {/* 「数据发给谁」的告知（spec §5.1）：这是用户知情权，不是可选的装饰。 */}
        <p className="mt-1.5 text-[11px] text-muted-foreground">
          {providerName
            ? t('assistant.chatDisclosure', '相关数据将发送给 {{provider}}{{model}}', {
                provider: providerName,
                model: modelName ? `（${modelName}）` : '',
              })
            : t('assistant.chatNoModel', '尚未配置可用模型，无法对话。')}
        </p>
      </div>
    </div>

      {/* 外链确认（spec §7 末条）：弹出前不发生 navigate，关闭即纯取消；
          目标 URL 完整展示，便于用户核对再决定是否离开宿主。 */}
      <Dialog
        open={externalHref !== null}
        onOpenChange={(open) => {
          if (!open) setExternalHref(null)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('assistant.openExternalTitle', '打开外部链接？')}</DialogTitle>
            <DialogDescription className="whitespace-pre-wrap break-all">
              {externalHref ? confirmOpenMessage(externalHref) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setExternalHref(null)}>
              {t('common.cancel', '取消')}
            </Button>
            <Button onClick={openExternal}>{t('assistant.openExternal', '打开')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

/** 空 Panel 包装（供浮窗/首页复用同一套外观）。 */
export function AssistantChatShell({
  title,
  actions,
  children,
}: {
  title: string
  actions?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <Panel title={title} actions={actions} bodyClassName="flex min-h-0 flex-1 flex-col p-2">
      {children}
    </Panel>
  )
}
