/**
 * 聊天的共享类型。
 *
 * 单独成文件是为了打破一个循环导入：`AssistantChatPanel`（组件）与
 * `assistant-chat-events`（纯函数）都需要 `ChatMessage`，而 event 模块不应反向依赖组件文件
 * （react-refresh 也要求组件文件只导出组件）。
 */

/** 一条已渲染的消息。 */
export interface ChatMessage {
  /** 前端本地键：历史消息用 `h-<id>`，流式消息用 `run-<ts>`，避免与库内 id 混淆。 */
  key: string
  role: 'user' | 'assistant'
  content: string
  /** 流式进行中：按纯文本追加（未闭合的 Markdown 会被误渲染成半截结构）。 */
  streaming?: boolean
  /** run 终态为失败/取消时的说明，供界面提示。 */
  note?: string
}
