/**
 * Bot 实时事件契约（FR-086 / FR-136，ADR-097）。
 * 与后端 SSE `event: bot` 载荷对齐，供 `@jianmanager/ui` 内视图逻辑与应用侧 api 共用。
 */

/** Bot 实时事件（SSE event: bot）。 */
export interface BotRealtimeEvent {
  botId: number
  botUuid: string
  type: string
  data: Record<string, unknown>
  timestamp: number
}

/** 单 Bot 实时状态。 */
export interface BotRealtimeState {
  status?: string
  health?: number
  food?: number
  behavior?: string
  position?: { x: number; y: number; z: number }
  events: BotRealtimeEvent[]
  connected: boolean
}
