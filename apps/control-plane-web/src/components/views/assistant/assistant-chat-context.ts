import { createContext, useContext } from 'react'
import type { useAssistantChat } from '@/components/views/assistant/useAssistantChat'

/**
 * 助手会话状态的**外壳级单例**（FR-519 §3「外壳级单一 manager」/ FR-520 §8）。
 *
 * ## 为什么必须在外壳层只调一次
 *
 * `useAssistantChat` 内部全部是组件级 `useState`（会话 id、流式消息、工具状态、失败原因…），
 * 没有模块级单例。因此**每次调用都会产生一份独立状态**。
 *
 * 原先首页（`AssistantHomePage`）与浮窗（`AssistantDock`）各自调用一次，实际是两份：
 * 首页正在逐字输出时打开浮窗，浮窗看不到这些内容（它会自己拉已落库历史）；
 * 两边甚至可能对同一会话各发一轮，后端回 409 `CONVERSATION_BUSY`，
 * 用户看到的是「莫名其妙的忙」。
 *
 * 首页在路由树里、浮窗在 Shell 层，**两者不是父子**，只能经 Context 下发。
 * 由 `DashboardPage`（两者的共同祖先）调用一次，两边消费。
 *
 * ## 消费侧的约定
 *
 * 用 {@link useAssistantChatCtx} 取状态；它会在缺失 Provider 时明确报错，
 * 而不是静默返回一个空对象——后者会让「忘了包 Provider」变成难以定位的诡异空界面。
 */
export type AssistantChatState = ReturnType<typeof useAssistantChat>

const AssistantChatContext = createContext<AssistantChatState | null>(null)

export const AssistantChatProvider = AssistantChatContext.Provider

export function useAssistantChatCtx(): AssistantChatState {
  const v = useContext(AssistantChatContext)
  if (v === null) {
    throw new Error(
      'useAssistantChatCtx 必须在 AssistantChatProvider 内使用：' +
        '会话状态由外壳层统一持有，组件不得自行调用 useAssistantChat（会分裂成两份状态）。',
    )
  }
  return v
}
