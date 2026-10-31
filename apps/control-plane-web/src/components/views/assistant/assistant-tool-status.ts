import type { AssistantEventFrame } from '@/api/assistant-chat'

/** 
 * 工具调用状态的聚合（§5「工具状态徽标」）。
 *
 * 后端把一次工具调用拆成三类事件、用 `callId` 串起来（见 service/assistant_chat.go）：
 *  - `tool_call`   { callId, name, argsJson }                      —— 调用被派发
 *  - `tool_status` { callId, status, errorCode? }                   —— running / pending_confirmation / ok / failed
 *  - `tool_result` { callId, preview, truncated }                   —— 结果预览（后端已按 2048 rune 截断一次）
 *
 * 因此这里必须**按 callId 聚合而不是按事件顺序逐条渲染**：一轮 run 里工具事件与
 * text_delta 交错到达，逐条渲染会得到「先一条空状态、再一条结果」这种半成品列表。
 *
 * 单独成文件（而不是塞进 useAssistantChat）的理由与 `assistant-chat-events` 相同：
 * 聚合是纯函数，能脱离 React 单测，也避免 hook 里堆一层内联 reducer。
 */

/** 已知状态词。未列出的取值不擅自映射——后端加了新状态时徽标显示原词，而不是假装成 ok。 */
export type ToolCallPhase = 'running' | 'pending_confirmation' | 'ok' | 'failed'

/** 一条工具调用的聚合视图（渲染所需的全部信息都在这里）。 */
export interface ToolCallView {
  callId: string
  /** 工具名；只有状态/结果帧而没有调用帧时回退为 callId，至少让用户知道「有这一条」。 */
  name: string
  /** 后端原始状态词（未知取值也原样保留，供界面直接展示）。 */
  status: string
  /** 失败时的分类错误码（后端 ai.Classify 的结果，如 upstream_timeout）。 */
  errorCode?: string
  /** 结果预览：保持后端给的原样，截断交给展示层（版面宽窄只有渲染时才知道）。 */
  preview?: string
  /** 后端声明结果已被截断（超过 ToolResultMax）。 */
  truncated?: boolean
}

/** 
 * 预览在界面上最多显示多少字符。
 *
 * 后端已按 2048 rune 截断过一次（`assistantChatToolPreviewRunes`），但那个量级是给
 * 模型回灌用的，塞进聊天侧栏会把整条消息流顶走——这里按**版面**再截一次。
 */
export const TOOL_PREVIEW_MAX_CHARS = 240

/** 
 * 按字符（而非 UTF-16 码元）截断预览。
 *
 * 用 `Array.from` 而不是 `slice`：`slice` 会从中间劈开代理对（emoji、部分生僻字），
 * 渲染出半个字符的乱码。返回 `clipped` 供界面决定是否补一句「已截断」。
 */
export function truncateToolPreview(
  preview: string,
  max = TOOL_PREVIEW_MAX_CHARS,
): { text: string; clipped: boolean } {
  const flat = preview.trim()
  const chars = Array.from(flat)
  if (chars.length <= max) return { text: flat, clipped: false }
  return { text: chars.slice(0, max).join('') + '…', clipped: true }
}

/** 状态词 → 徽标语义；未知取值返回 null（调用方据此显示原词）。 */
export function toolCallPhase(status: string): ToolCallPhase | null {
  switch (status) {
    case 'running':
      return 'running'
    case 'pending_confirmation':
      return 'pending_confirmation'
    case 'ok':
      return 'ok'
    case 'failed':
      return 'failed'
    default:
      return null
  }
}

/** `tool_status` / `tool_result` / `tool_call` 三种帧的载荷（其余字段用不上）。 */
interface ToolEventData {
  callId?: string
  name?: string
  status?: string
  errorCode?: string
  preview?: string
  truncated?: boolean
}

/** 
 * 事件 → 工具调用列表（纯函数，保持首次出现顺序）。
 *
 * 非工具事件返回**原数组引用**：hook 里直接 `setState(prev => apply(...))`，
 * 引用不变时 React 不会重渲染——文本流每秒几十帧，不该连带工具列表一起重建。
 */
export function applyEventToToolCalls(prev: ToolCallView[], frame: AssistantEventFrame): ToolCallView[] {
  if (frame.type !== 'tool_call' && frame.type !== 'tool_status' && frame.type !== 'tool_result') {
    return prev
  }
  const data = (frame.data ?? {}) as ToolEventData
  // 帧顶层与载荷里都可能带 callId（后端两处都写了），任取其一即可。
  const callId = (data.callId ?? frame.callId ?? '').trim()
  // 没有 callId 就无法聚合：宁可丢这一帧，也不要凭空造一条既无名字也无状态的空行。
  if (callId === '') return prev

  const idx = prev.findIndex((c) => c.callId === callId)
  /** 就地合并：新调用则追加，已有则覆盖给定字段。 */
  const patch = (next: Partial<ToolCallView>): ToolCallView[] => {
    if (idx < 0) {
      return [...prev, { callId, name: callId, status: 'running', ...next }]
    }
    const copy = prev.slice()
    copy[idx] = { ...copy[idx]!, ...next }
    return copy
  }

  switch (frame.type) {
    case 'tool_call': {
      // 已存在时**不重置状态**：断线补读会重放同一帧，若把它打回 running，
      // 已完成的行会闪回「执行中」，看起来像工具重跑了一次。
      if (idx >= 0) {
        return data.name ? patch({ name: data.name }) : prev
      }
      return [...prev, { callId, name: data.name || callId, status: 'running' }]
    }
    case 'tool_status': {
      const status = (data.status ?? '').trim()
      // 空状态词无信息量，保留上一状态（通常是 tool_call 写入的 running）。
      if (status === '') return prev
      const errorCode = (data.errorCode ?? '').trim()
      // 状态推进时显式清掉旧错误码：否则「失败后重试成 ok」会留着上一次的失败原因。
      return patch({ status, errorCode: errorCode || undefined })
    }
    case 'tool_result': {
      return patch({
        preview: typeof data.preview === 'string' ? data.preview : undefined,
        truncated: data.truncated === true,
      })
    }
    default:
      return prev
  }
}

/** 
 * 元工具名（后端渐进式披露三件套）。
 *
 * 为什么这层要在前端识别：`search_tools` / `describe_tool` 本身**不产生业务副作用**，
 * 它们只是「查有哪些工具」「读某个工具的参数定义」。若和真正执行工具的操作混在一起
 * 显示成同一类，用户会以为助手在一堆操作里乱点，而实际它只是在翻工具手册。
 */
export const META_TOOL_NAMES = [
  'system_search_tools',
  'system_describe_tool',
  'system_call_tool',
] as const

/** 该工具是否属于「探索工具目录」这一类（search / describe）。 */
export function isToolExploration(name: string): boolean {
  // call_tool 是「真正执行」，不算探索——它下面才是业务动作。
  return name === 'system_search_tools' || name === 'system_describe_tool'
}

/** 该工具是否是分发入口（call_tool），需要把内层工具名展示出来。 */
export function isToolDispatcher(name: string): boolean {
  return name === 'system_call_tool'
}
