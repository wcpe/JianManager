/** FR-372 压测观测共享类型（对齐 bot-load-platform/api.md）。 */

export type BotLoadRunState =
  | 'pending'
  | 'preflighting'
  | 'ready'
  | 'starting'
  | 'running'
  | 'degraded'
  | 'stopping'
  | 'cancelling'
  | 'completed'
  | 'failed'
  | 'cancelled'

export type BotLoadVerdict = 'pending' | 'passed' | 'failed' | 'aborted'

export type BotLoadVerdictReasonState = 'pass' | 'fail' | 'pending' | 'not_applicable'

export type BotLoadVerdictReasonKey =
  | 'online_rate'
  | 'command_sent_rate'
  | 'schedule_completion_rate'
  | 'worker_health_rate'
  | 'barrier_arrival_rate'
  | 'schedule_lag_p95_ms'
  | 'process_crashes'
  | 'sample_coverage_rate'
  | 'consecutive_sample_gap_seconds'
  | 'safety_executor_memory_rate'
  | 'safety_event_loop_p95_ms'

export interface BotLoadVerdictReason {
  key: BotLoadVerdictReasonKey
  state: BotLoadVerdictReasonState
  expected?: number | string
  actual?: number | string
  unit?: 'ratio' | 'ms' | 'seconds' | 'count'
  message: string
  stageIndex?: number
}

export interface BotLoadCommand {
  id: string
  atMs: number
  command: string
  repeat?: { intervalMs: number; count: number }
}

export interface BotLoadCommandSchedule {
  commands: BotLoadCommand[]
  durationMs: number
  jitterMs?: number
}

export type BotLoadProfile =
  | { type: 'stable'; targetBots: number; rampUpSeconds: number; durationSeconds: number }
  | { type: 'step'; stages: Array<{ targetBots: number; holdSeconds: number }>; stopOnThresholdFailure: boolean }
  | {
      type: 'spike'
      targetBots: number
      connectWindowSeconds: number
      barrier?: { key: string; releaseWindowMs: number }
      holdSeconds: number
    }

export interface BotLoadThresholds {
  minOnlineRate: number
  minCommandSentRate: number
  minScheduleCompletionRate: number
  minWorkerHealthRate: number
  minBarrierArrivalRate: number
  maxScheduleLagP95Ms: number
  maxProcessCrashes: number
  safety?: {
    maxExecutorMemoryRate: number
    maxEventLoopP95Ms: number
    sustainSeconds: number
  }
  legacy?: {
    enabled: boolean
    minTps?: number
    maxMsptP95?: number
    requireBusinessObservation?: boolean
  }
}

export interface BotLoadAllocation {
  batchId: string
  ordinal: number
  executorNodeId: number
  executorNodeUuid: string
  executorNodeName: string
  plannedCount: number
  connectStartAt: string
  connectIntervalMs: number
  idempotencyKey: string
}

export interface BotLoadLoadCounts {
  planned: number
  accepted: number
  connecting: number
  connected: number
  disconnected: number
  failed: number
  stopped: number
}

export interface BotLoadCommandCounts {
  planned: number
  sent: number
  failed: number
  timedOut: number
  cancelled: number
}

export interface BotLoadBarrierCounts {
  waiting: number
  arrived: number
  released: number
  timedOut: number
}

export interface BotLoadRunV2 {
  schemaVersion: 2
  id: number
  uuid: string
  instanceId: number
  instanceName?: string
  name: string
  namePrefix: string
  count: number
  behavior: string
  config: Record<string, unknown>
  orchestrationSummary?: Record<string, unknown>
  status: string
  counts: { total: number; byStatus: Record<string, number> }
  allocations: BotLoadAllocation[]
  batches: Array<{
    id: number
    uuid: string
    executorNodeId: number
    ordinal: number
    plannedCount: number
    acceptedCount: number
    connectedCount: number
    failedCount: number
    state: string
    startedAt?: string
    endedAt?: string
  }>
  startedAt?: string
  stoppedAt?: string
  endedAt?: string
  createdAt: string
  updatedAt: string
  templateId?: number
  targetBots: number
  runState: BotLoadRunState
  verdict: BotLoadVerdict
  verdictReasons: BotLoadVerdictReason[]
  currentStage: number
  loadProfile: BotLoadProfile
  thresholds: BotLoadThresholds
  loadCounts: BotLoadLoadCounts
  commandCounts: Record<string, BotLoadCommandCounts>
  barrier: BotLoadBarrierCounts
  maxStableBots: number
  failureSummary: Record<string, number>
  commandSchedule?: BotLoadCommandSchedule
  scenario?: unknown
  orchestrationYaml?: string
}

export type BotLoadFailureCategory = 'target' | 'executor' | 'network' | 'scenario' | 'internal'

export interface BotLoadFailure {
  id: string
  runUuid: string
  botUuid?: string
  executorNodeId?: number
  actionRunId?: string
  stepId?: string
  commandId?: string
  category: BotLoadFailureCategory
  legacyCategory?: 'probe'
  errorCode: string
  message: string
  retryable: boolean
  occurredAt: string
}

export interface BotLoadRunBot {
  id: number
  uuid: string
  name: string
  status: string
  executorNodeId?: number
  stepId?: string
  commandId?: string
  reconnectCount: number
  lastSeenAt?: string
  lastError?: string
}

export interface BotLoadRetryResult {
  requested: number
  accepted: number
  skipped: number
  errors: Array<{ botUuid?: string; errorCode: string; message: string }>
}

export interface BotLoadLatencySummary {
  connectP50Ms: number | null
  connectP95Ms: number | null
  connectP99Ms: number | null
  scheduleLagP50Ms: number | null
  scheduleLagP95Ms: number | null
  scheduleLagP99Ms: number | null
  barrierReleaseLagP50Ms: number | null
  barrierReleaseLagP95Ms: number | null
  barrierReleaseLagP99Ms: number | null
}

export interface BotLoadMetricPoint {
  timestamp: string
  stageIndex: number
  counts: Record<string, number>
  command: Record<string, number>
  barrier: Record<string, number>
  executor: Array<{
    nodeId: number
    activeBots: number
    rssBytes?: number
    eventLoopP95Ms?: number
    cpuPercent?: number
    health: string
  }>
  latency: BotLoadLatencySummary
  targetLegacy?: { tps?: number; msptP95?: number; onlinePlayers?: number }
  errors: Record<string, number>
}

export type BotLoadRunEventType =
  | 'run-state'
  | 'stage'
  | 'barrier'
  | 'scenario-action'
  | 'command-schedule'
  | 'command-send'
  | 'worker-health'
  | 'executor-crash'
  | 'safety-stop'
  | 'report-ready'

export interface BotLoadRunEventBase {
  eventId: string
  runId: number
  runUuid: string
  timestamp: string
  legacy?: { category?: string; data?: Record<string, unknown> }
}

export type BotLoadRunEvent = BotLoadRunEventBase & {
  type: BotLoadRunEventType
  stepId?: string
  actionRunId?: string
  botUuid?: string
  commandId?: string
  executorNodeId?: number
  stageIndex?: number
  payload: Record<string, unknown>
}

export interface Page<T> {
  items: T[]
  total: number
  page: number
  pageSize: number
}

export interface BotLoadRunEventPage extends Page<BotLoadRunEvent> {
  snapshotEventId: string
}

export interface BotLoadReport {
  run: BotLoadRunV2
  stages: Array<{
    stageIndex: number
    targetBots: number
    state: string
    startedAt?: string
    endedAt?: string
    verdict: BotLoadVerdict
    verdictReasons: BotLoadVerdictReason[]
  }>
  verdictReasons: BotLoadVerdictReason[]
  maxStableBots: number
  latency: BotLoadLatencySummary
  failures: { summary: Record<BotLoadFailureCategory, number>; items: BotLoadFailure[] }
  executors: Array<{
    nodeId: number
    nodeUuid: string
    health: string
    peakActiveBots: number
    peakRssBytes?: number
    peakEventLoopP95Ms?: number
  }>
  commands: Record<
    string,
    BotLoadCommandCounts & {
      lagP50Ms: number | null
      lagP95Ms: number | null
      lagP99Ms: number | null
    }
  >
  barriers: Record<
    string,
    {
      stageIndex: number
      barrierKey: string
      round: number
      expected: number
      arrived: number
      released: number
      timedOut: number
      releaseLagP50Ms: number | null
      releaseLagP95Ms: number | null
      releaseLagP99Ms: number | null
    }
  >
  legacy?: Record<string, unknown>
  disclaimer: string
}

/** 会话级 SSE 聚合事件（非 history 真源）。 */
export type BotLoadStreamEventName =
  | 'init'
  | 'run-state'
  | 'counts'
  | 'stage'
  | 'metric'
  | 'command'
  | 'failure'
  | 'warning'
  | 'history'
  | 'complete'

export interface BotLoadStreamWarning {
  code: string
  message: string
  timestamp: string
}

/** 命令发送成功边界免责声明（ADR-075）。 */
export const BOT_CHAT_SUCCESS_DISCLAIMER_ZH =
  '命令发送成功仅表示 Bot Worker 调用 bot.chat 时未同步抛错，不证明服务器接受、权限校验通过或产生预期业务效果。'

export const BOT_CHAT_SUCCESS_DISCLAIMER_EN =
  'Command send success only means Bot Worker called bot.chat without a synchronous error; it does not prove server acceptance, permission, or business effect.'

export const TERMINAL_RUN_STATES: readonly BotLoadRunState[] = [
  'completed',
  'failed',
  'cancelled',
] as const

export function isTerminalRunState(state: string | undefined): boolean {
  return state === 'completed' || state === 'failed' || state === 'cancelled'
}

export function isLiveRunState(state: string | undefined): boolean {
  return (
    state === 'starting' ||
    state === 'running' ||
    state === 'degraded' ||
    state === 'stopping' ||
    state === 'cancelling'
  )
}

export type SessionTab = 'overview' | 'bots' | 'metrics' | 'failures' | 'events' | 'config'

export const SESSION_TABS: readonly SessionTab[] = [
  'overview',
  'bots',
  'metrics',
  'failures',
  'events',
  'config',
] as const
