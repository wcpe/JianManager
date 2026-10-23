import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, History, Maximize2, MessageSquarePlus, Sparkles, X } from 'lucide-react'
import { Badge, Button } from '@jianmanager/ui'
import { AssistantChatPanel } from '@/components/views/assistant/AssistantChatPanel'
import { useAssistantChat } from '@/components/views/assistant/useAssistantChat'

/**
 * 全局助手浮窗（FR-519 §5.4）。
 *
 * 三条与「窗口」相关的语义，写在这里以免后来者按直觉改错：
 *  - **关闭 ≠ 停止**：`×` 只是不渲染，run 继续在服务端推进；再打开回到同一处。
 *  - **收起 ≠ 停止**：收成窄条，会话与 run 不变。
 *  - **停止是独立按钮**：只有它调 `/runs/:id/cancel`。
 *
 * 会话状态刻意**不随浮窗卸载而重置**：状态在 useAssistantChat 里，而这个 hook 的
 * 生命周期由外层（Dashboard 级）持有——但为了让「关闭后再打开能回同一处」成立，
 * 这里把上次的 conversationId 记在 sessionStorage，而不是靠组件不被卸载。
 */
const LAST_CONVERSATION_KEY = 'jm-assistant-last-conversation'

export interface AssistantDockProps {
  /** 是否打开（受控：顶栏按钮切换）。 */
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** 右侧非模态浮窗。 */
export function AssistantDock({ open, onOpenChange }: AssistantDockProps) {
  const { t } = useTranslation()
  const [collapsed, setCollapsed] = useState(false)
  const [historyOpen, setHistoryOpen] = useState(false)
  const chat = useAssistantChat(open)
  const panelRef = useRef<HTMLDivElement | null>(null)
  /** 打开前聚焦的元素：关闭后要把焦点还回去（spec §5.5）。 */
  const restoreFocusRef = useRef<HTMLElement | null>(null)

  // 打开时焦点移入，关闭时归还。
  useEffect(() => {
    if (open) {
      restoreFocusRef.current = document.activeElement as HTMLElement | null
      // 等浮窗渲染完成再聚焦输入框，否则 ref 还没挂上。
      const id = window.setTimeout(() => {
        panelRef.current?.querySelector<HTMLTextAreaElement>('textarea')?.focus()
      }, 0)
      return () => window.clearTimeout(id)
    }
    restoreFocusRef.current?.focus?.()
  }, [open])

  // 记住上次会话 id，供「关闭后再打开回到同一处」。
  useEffect(() => {
    if (chat.conversationId !== null) {
      try {
        sessionStorage.setItem(LAST_CONVERSATION_KEY, String(chat.conversationId))
      } catch {
        // sessionStorage 不可用（隐私模式）时忽略：只是少了个便利，不影响功能。
      }
    }
  }, [chat.conversationId])

  // Esc 关闭（不停止 run）。
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !historyOpen) onOpenChange(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [historyOpen, onOpenChange, open])

  if (!open) return null

  const headerActions = (
    <div className="flex items-center gap-0.5">
      <Button
        variant="ghost"
        size="icon"
        aria-label={t('assistant.dockNew', '新会话')}
        title={t('assistant.dockNew', '新会话')}
        onClick={() => void chat.startNewConversation()}
      >
        <MessageSquarePlus className="size-4" />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        aria-label={t('assistant.dockHistory', '历史会话')}
        title={t('assistant.dockHistory', '历史会话')}
        onClick={() => setHistoryOpen((v) => !v)}
      >
        <History className="size-4" />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        aria-label={t('assistant.dockCollapse', '收起')}
        title={t('assistant.dockCollapse', '收起')}
        onClick={() => setCollapsed((v) => !v)}
      >
        <ChevronDown className="size-4" />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        aria-label={t('assistant.dockClose', '关闭')}
        title={t('assistant.dockClose', '关闭（不会停止生成）')}
        onClick={() => onOpenChange(false)}
      >
        <X className="size-4" />
      </Button>
    </div>
  )

  // 收起态：只留一条窄条，仍能看出「有会话、可展开」。
  if (collapsed) {
    return (
      <div className="fixed bottom-4 right-4 z-40">
        <Button variant="outline" size="sm" onClick={() => setCollapsed(false)}>
          <Sparkles className="mr-1.5 size-4" />
          {t('assistant.dockExpand', '展开助手')}
        </Button>
      </div>
    )
  }

  return (
    <div
      ref={panelRef}
      role="complementary"
      aria-label={t('assistant.dockTitle', 'AI 助手')}
      className="fixed bottom-4 right-4 z-40 flex h-[min(38rem,calc(100vh-6rem))] w-[min(26rem,calc(100vw-2rem))] flex-col overflow-hidden rounded-lg border border-border bg-card shadow-lg"
    >
      <div className="flex items-center justify-between gap-2 border-b border-border/60 px-3 py-2">
        <div className="flex min-w-0 items-center gap-1.5">
          <Sparkles className="size-4 shrink-0 text-primary" />
          <span className="truncate text-sm font-medium">{t('assistant.dockTitle', 'AI 助手')}</span>
          {chat.running ? (
            <Badge variant="secondary">{t('assistant.dockRunning', '生成中')}</Badge>
          ) : null}
        </div>
        {headerActions}
      </div>

      {historyOpen ? (
        <div className="max-h-56 shrink-0 overflow-y-auto border-b border-border/60">
          <p className="px-3 py-1.5 text-[11px] text-muted-foreground">
            {t('assistant.dockHistoryHint', '按最近更新排序')}
          </p>
          {chat.conversations.length === 0 ? (
            <p className="px-3 pb-2 text-xs text-muted-foreground">
              {t('assistant.dockHistoryEmpty', '还没有历史会话。')}
            </p>
          ) : (
            <ul>
              {chat.conversations.map((c) => (
                <li key={c.id}>
                  <button
                    type="button"
                    onClick={() => {
                      chat.selectConversation(c.id)
                      setHistoryOpen(false)
                    }}
                    className={
                      c.id === chat.conversationId
                        ? 'w-full truncate bg-primary/10 px-3 py-1.5 text-left text-xs font-medium text-primary'
                        : 'w-full truncate px-3 py-1.5 text-left text-xs hover:bg-accent/60'
                    }
                  >
                    {c.title || t('assistant.dockUntitled', '未命名会话')}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : null}

      <AssistantChatPanel
        messages={chat.messages}
        running={chat.running}
        providerName={chat.providerName}
        modelName={chat.modelName}
        hint={
          chat.noModel
            ? t('assistant.chatNoModelHint', '尚未配置可用模型。请让平台管理员在「AI 助手」设置中添加供应商与模型。')
            : null
        }
        onSend={chat.send}
        onStop={chat.stop}
      />
    </div>
  )
}

/** 顶栏入口按钮（供 Dashboard 顶栏挂载）。 */
export function AssistantDockTrigger({ onOpen }: { onOpen: () => void }) {
  const { t } = useTranslation()
  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label={t('assistant.openDock', '打开 AI 助手')}
      title={t('assistant.openDock', '打开 AI 助手')}
      onClick={onOpen}
    >
      <Sparkles className="size-4" />
    </Button>
  )
}

/** 供「展开为整页」用的图标（首页入口复用）。 */
export const AssistantExpandIcon = Maximize2
