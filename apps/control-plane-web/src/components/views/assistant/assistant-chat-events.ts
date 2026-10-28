import type { AssistantEventFrame } from '@/api/assistant-chat'
import type { ChatMessage } from '@/components/views/assistant/assistant-chat-types'

/** 会话内事件 → 消息列表的纯函数（便于单测，不依赖 React）。 */
export function applyEventToMessages(
  prev: ChatMessage[],
  frame: AssistantEventFrame,
  runKey: string,
): ChatMessage[] {
  const idx = prev.findIndex((m) => m.key === runKey)
  const patch = (next: Partial<ChatMessage>): ChatMessage[] => {
    if (idx < 0) {
      return [...prev, { key: runKey, role: 'assistant', content: '', streaming: true, ...next }]
    }
    const copy = prev.slice()
    copy[idx] = { ...copy[idx]!, ...next }
    return copy
  }

  switch (frame.type) {
    case 'text_delta': {
      const text = (frame.data as { text?: string } | null)?.text ?? ''
      if (text === '') return prev
      const cur = idx >= 0 ? (prev[idx]!.content ?? '') : ''
      return patch({ content: cur + text, streaming: true })
    }
    case 'error': {
      const msg = (frame.data as { message?: string } | null)?.message ?? ''
      return patch({ streaming: false, note: msg })
    }
    case 'run_end': {
      // 终态：run_end 每 run 恰好一条。
      //
      // `run_end` 通常**不带** note（失败原因在它前面那条 `error` 帧里）。因此这里有值时
      // 才覆盖：无条件写 `note: note || undefined` 会把上一条 `error` 刚写好的失败原因抹掉，
      // 界面上就只剩一条没有回复、也没有任何提示的提问（真实上游不可达时实测到）。
      const note = (frame.data as { note?: string } | null)?.note ?? ''
      return patch(note ? { streaming: false, note } : { streaming: false })
    }
    default:
      // 工具相关与 usage 事件在 FR-516 前不会出现；出现了也不该让文本流断掉，
      // 故这里显式忽略而不是抛错（前向兼容）。
      return prev
  }
}

