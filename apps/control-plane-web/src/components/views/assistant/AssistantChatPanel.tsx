import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { ArrowUp, Square, Sparkles } from 'lucide-react'
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
import type { ChatMessage } from '@/components/views/assistant/assistant-chat-types'

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
const chatMarkdownComponents: Components = {
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
  // 外链：不应用内直跳，交由浏览器新标签打开（noopener 防反向 window.opener 访问）。
  a: ({ children, href }) => (
    <a
      href={typeof href === 'string' ? href : undefined}
      target="_blank"
      rel="noopener noreferrer nofollow"
      className="text-primary underline underline-offset-2"
    >
      {children}
    </a>
  ),
}

/** 单条消息气泡。 */
function MessageBubble({ message }: { message: ChatMessage }) {
  const { t } = useTranslation()
  const isUser = message.role === 'user'
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
          <p className="whitespace-pre-wrap leading-relaxed">{message.content}</p>
        ) : message.streaming ? (
          // 流式期间按纯文本追加：未闭合的 ``` 或 * 会被渲染成半截结构。
          <p className="whitespace-pre-wrap leading-relaxed">
            {message.content}
            <span className="ml-0.5 inline-block h-4 w-1.5 animate-pulse bg-foreground/60 align-middle" />
          </p>
        ) : (
          <ReactMarkdown remarkPlugins={[remarkGfm]} components={chatMarkdownComponents}>
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
  onSend: (text: string) => Promise<void> | void
  onStop: () => void
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
  onSend,
  onStop,
  className,
}: AssistantChatPanelProps) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState('')
  const scrollRef = useRef<HTMLDivElement | null>(null)
  /** 用户是否停在底部：滚上去看历史时不要被强制拽回底部。 */
  const stickToBottom = useRef(true)

  const canSend = useMemo(() => draft.trim() !== '' && !running, [draft, running])

  const send = useCallback(async () => {
    const text = draft.trim()
    if (text === '' || running) return
    setDraft('')
    stickToBottom.current = true
    await onSend(text)
  }, [draft, running, onSend])

  // 只在「贴着底部」时跟随，避免用户向上翻阅时被拉回。
  useEffect(() => {
    const el = scrollRef.current
    if (!el || !stickToBottom.current) return
    el.scrollTop = el.scrollHeight
  }, [messages])

  const onScroll = () => {
    const el = scrollRef.current
    if (!el) return
    // 留 24px 容差：亚像素与圆角会让 scrollHeight 差一点点。
    stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  }

  return (
    <div className={className ?? 'flex min-h-0 flex-1 flex-col'}>
      {/* 消息流：role=log + aria-live=polite（spec §5.5）。
          逐 token 不播报——流式期间 text_delta 每秒可能几十条，播报会把屏幕阅读器刷屏；
          播报粒度交给浏览器按「内容稳定」自行决定即可。 */}
      <div
        ref={scrollRef}
        onScroll={onScroll}
        role="log"
        aria-live="polite"
        aria-label={t('assistant.chatLog', '对话消息')}
        className="min-h-0 flex-1 space-y-3 overflow-y-auto px-1 py-2"
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
          messages.map((m) => <MessageBubble key={m.key} message={m} />)
        )}
      </div>

      {hint ? (
        <Alert variant="warning" className="mx-1 my-2">
          <AlertDescription>{hint}</AlertDescription>
        </Alert>
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
