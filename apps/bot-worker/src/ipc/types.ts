/**
 * IPC 消息类型定义。
 * Bot Worker 通过 stdin/stdout JSON 行协议与 Worker Node 通信。
 */

import type { CustomBehaviorConfig } from '../behavior/custom.js'
import type { OrchestratedBehaviorConfig } from '../behavior/orchestrated.js'

/** 行为配置载荷。 */
export type BehaviorConfig = CustomBehaviorConfig | OrchestratedBehaviorConfig

/** Worker Node → Bot Worker 的命令。 */
export type IpcCommand =
  | CreateBotsCommand
  | StopBotsCommand
  | SignalActionsCommand
  | GetFleetSnapshotCommand
  | SetBehaviorCommand
  | SendBotCommand
  | RunScriptCommand
  | StopScriptCommand

/** 批量创建 Bot；requestId 缺省时兼容旧单 Bot 调用。 */
export interface CreateBotsCommand {
  cmd: 'create-bots'
  requestId?: string
  batchId?: string
  idempotencyKey?: string
  bots: BotConfig[]
}

/** 批量停止 Bot；generation/reason 由新 Fleet 调用携带。 */
export interface StopBotsCommand {
  cmd: 'stop-bots'
  requestId?: string
  botIds: string[]
  generation?: number
  reason?: string
}

/** 通用动作信号批量投递，具体场景语义由后续 FR 实现。 */
export interface SignalActionsCommand {
  cmd: 'signal-actions'
  requestId: string
  signals: ActionSignal[]
}

/** 请求当前 Bot Fleet 完整快照。 */
export interface GetFleetSnapshotCommand {
  cmd: 'get-fleet-snapshot'
  requestId: string
}

/** 切换行为模式命令。 */
export interface SetBehaviorCommand {
  cmd: 'set-behavior'
  botId: string
  behavior: string
  target?: string
  config?: BehaviorConfig
}

/** 向 Bot 发送命令。 */
export interface SendBotCommand {
  cmd: 'send-command'
  botId: string
  command: string
}

/** 执行脚本命令。 */
export interface RunScriptCommand {
  cmd: 'run-script'
  scriptId: string
  steps: Array<{
    action: 'chat' | 'move' | 'wait' | 'command' | 'log'
    message?: string
    pos?: { x: number; y: number; z: number }
    duration?: number
    command?: string
    text?: string
  }>
  botIds: string[]
}

/** 停止脚本命令。 */
export interface StopScriptCommand {
  cmd: 'stop-script'
  scriptId: string
}

/** Bot assignment 配置；Fleet 字段均可选以保持旧调用兼容。 */
export interface BotConfig {
  id: string
  name: string
  host: string
  port: number
  username?: string
  version?: string
  auth?: 'offline' | 'microsoft'
  behavior?: string
  behaviorConfig?: BehaviorConfig
  server?: string
  sessionId?: string
  generation?: number
  configHash?: string
  cohortKey?: string
  scenario?: unknown
  resumeStepId?: string
  connectNotBefore?: number
  connectNotBeforeUnixMs?: number
  correlationSeed?: string
}

/** probe/barrier/cancel 等外部动作信号的冻结信封。 */
export interface ActionSignal {
  signalId: string
  botId: string
  sessionId: string
  generation: number
  actionRunId: string
  stepId: string
  type: string
  correlationToken?: string
  payload?: unknown
  observedAt?: number
}

/** create-bots/stop-bots 的逐 Bot 同步回执。 */
export interface BotItemResult {
  botId: string
  accepted: boolean
  skipped: boolean
  status?: string
  errorCode?: string
  error?: string
}

/** signal-actions 的逐信号同步回执。 */
export interface SignalItemResult {
  signalId: string
  accepted: boolean
  skipped: boolean
  status?: string
  errorCode?: string
  error?: string
}

/** Bot runtime 快照及 bot-state 冻结字段。 */
export interface BotStateSnapshot {
  id: string
  status: string
  name?: string
  health?: number
  food?: number
  position?: { x: number; y: number; z: number }
  dimension?: string
  behavior?: string
  sessionId?: string
  generation?: number
  configHash?: string
  workerEpoch: string
  workerEpochGeneration: number
  eventSeq: number
  currentStepId?: string
  reconnectCount: number
  errorCode?: string
  lastError?: string
  observedAt: number
}

/** worker-ready 的 Fleet capability 事件。 */
export interface WorkerReadyEvent {
  evt: 'worker-ready'
  workerEpoch: string
  workerEpochGeneration: number
  botWorkerVersion: string
  maxBots: number
  features: string[]
  capacityGeneration: number
}

/** heartbeat 的 Fleet 容量与资源字段。 */
export interface FleetHeartbeatEvent {
  evt: 'heartbeat'
  activeBots: number
  connectingBots: number
  rssBytes: number
  eventLoopP95Ms: number
  droppedEvents: number
  capacityGeneration: number
}

/** batch-result 同步回执。 */
export interface BatchResultEvent {
  evt: 'batch-result'
  requestId?: string
  batchId?: string
  idempotencyKey?: string
  results: BotItemResult[]
}

/** signal-result 同步回执。 */
export interface SignalResultEvent {
  evt: 'signal-result'
  requestId: string
  signalResults: SignalItemResult[]
}

/** fleet-snapshot-result 同步回执。 */
export interface FleetSnapshotResultEvent {
  evt: 'fleet-snapshot-result'
  requestId: string
  bots: BotStateSnapshot[]
}

/** 异步 bot-state 事件。 */
export interface BotStateEvent {
  evt: 'bot-state'
  bots: BotStateSnapshot[]
}

/** 异步动作开始或终态事件。 */
export interface ActionEvent {
  evt: 'action-event'
  action: {
    botId: string
    sessionId: string
    generation: number
    actionRunId: string
    stepId: string
    attempt: number
    status: 'running' | 'succeeded' | 'failed' | 'timed_out' | 'cancelled'
    errorCode?: string
    message?: string
    correlationToken?: string
    result?: unknown
    durationMs?: number
    observedAt: number
  }
}
