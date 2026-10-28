import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, History, Maximize2, MessageSquarePlus, Sparkles, X } from 'lucide-react'
import { Badge, Button } from '@jianmanager/ui'
import { AssistantChatPanel } from '@/components/views/assistant/AssistantChatPanel'
import { useAssistantChatCtx } from '@/components/views/assistant/assistant-chat-context'
import { useAssistantConversationsInfinite } from '@/api/assistant-chat'
import { useAuthStore } from '@/stores/auth'

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

/**
 * 「上次会话」的存储键（按用户隔离，FR-519「换用户不串号」）。
 *
 * 此前键是全局固定的：用户 A 停在会话 7，登出后用户 B 登录，
 * 浮窗会尝试恢复到会话 7 —— 后端按归属会拒（403/404），但那仍是一次
 * 「用别人的游标导航」的可见异常。带上 userId 后，B 有自己的空位，
 * 不会继承 A 的停留点。
 */
function lastConversationKey(userId: number): string {
  return `${LAST_CONVERSATION_KEY}:${userId > 0 ? userId : 'anon'}`
}

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
  // 「上次会话」按用户隔离，需要当前用户 id（来自 JWT claims，非敏感展示字段）。
  const authUserId = useAuthStore((st) => st.claimsUserId ?? 0)
  /*
    会话状态来自外壳层（DashboardPage 里的单一实例），**不在这里自建**。
    自建会导致首页与浮窗各持一份：首页正在输出的内容浮窗看不到，
    两边甚至可能对同一会话各发一轮而撞 409 CONVERSATION_BUSY。
  */
  const chat = useAssistantChatCtx()
  // 历史抽屉单独用无限分页：首屏只取一页，滚到底再加载（会话可能上千条）。
  const historyQuery = useAssistantConversationsInfinite(open && historyOpen)
  // 分页结果摊平成一维列表（infinite 的 pages 是二维的）。
  const historyList = (historyQuery.data?.pages ?? []).flatMap((p) => p.items)
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
  //
  // 依赖里必须有 `authUserId`：写入的 key 按用户分片，换用户后若不同步重写，
  // 新用户的 key 会一直空着、而旧用户的键停在旧游标上（切回旧用户时又跳回去）。
  useEffect(() => {
    if (chat.conversationId !== null) {
      try {
        sessionStorage.setItem(lastConversationKey(authUserId), String(chat.conversationId))
      } catch {
        // sessionStorage 不可用（隐私模式）时忽略：只是少了个便利，不影响功能。
      }
    }
  }, [chat.conversationId, authUserId])

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
      /*
        形态随断点切换（FR-519 §7「移动端浮窗为全屏 sheet」）：
        - 窄屏（<640px，含验收指定的 390px）→ **全屏 sheet**：铺满视口、无圆角无边框。
          移动端浮层若只占 72% 高，底部会露出页面内容，容易误触下层操作。
        - 宽屏 → 原有右下角浮层形态（固定尺寸，不遮挡主要内容）。

        用 Tailwind 的 `max-sm:` / `sm:` 前缀而不是 JS 断点：纯 CSS 切换没有首帧抖动，
        也不需要监听 resize（避免「先按浮层渲染一帧再变成全屏」的闪跳）。
      */
      className="fixed z-40 flex flex-col overflow-hidden bg-card shadow-lg max-sm:inset-0 max-sm:h-full max-sm:w-full max-sm:rounded-none sm:right-4 sm:bottom-4 sm:h-[min(38rem,calc(100vh-6rem))] sm:w-[min(26rem,calc(100vw-2rem))] sm:rounded-lg sm:border sm:border-border"
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
          {historyList.length === 0 ? (
            <p className="px-3 pb-2 text-xs text-muted-foreground">
              {t('assistant.dockHistoryEmpty', '还没有历史会话。')}
            </p>
          ) : (
            <ul>
              {historyList.map((c) => (
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
          {/* 分页：有下一页才显示；加载中禁用避免重复请求。 */}
          {historyQuery.hasNextPage ? (
            <div className="px-3 pb-2 pt-1">
              <Button
                variant="ghost"
                size="sm"
                className="w-full text-xs"
                disabled={historyQuery.isFetchingNextPage}
                onClick={() => void historyQuery.fetchNextPage()}
              >
                {historyQuery.isFetchingNextPage
                  ? t('common.loading')
                  : t('assistant.dockLoadMore', '加载更多')}
              </Button>
            </div>
          ) : null}
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
        draft={chat.draft}
        onDraftChange={chat.setDraft}
        contextChips={chat.contextChips}
        runError={chat.runError}
        contextEntities={chat.contextEntities}
        onAddContextEntity={chat.addContextEntity}
        confirmationRunId={chat.runId}
        toolCalls={chat.toolCalls}
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
