export type ScenarioResumePolicy = 'restart_step' | 'restart_scenario' | 'fail'

export interface ScenarioBarrierRelease {
  type: 'all' | 'count' | 'percent'
  value?: number
}

export interface ScenarioStep {
  id: string
  type: string
  observationStep?: boolean
  timeoutMs: number
  maxAttempts: number
  retryBackoffMs: number
  resumePolicy: ScenarioResumePolicy
  durationMs?: number
  command?: string
  event?: string
  key?: string
  release?: ScenarioBarrierRelease
  timeoutPolicy?: 'fail' | 'release-arrived'
  [key: string]: unknown
}

export interface ScenarioRuntime {
  key: string
  percent: number
  seed?: number
  botOrdinal?: number
  steps: ScenarioStep[]
}

export type ScenarioActionState = 'running' | 'succeeded' | 'failed'

export interface ScenarioActionResult {
  state: ScenarioActionState
  errorCode?: string
  message?: string
  result?: unknown
  correlationToken?: string
  signalAccepted?: boolean
  jumpToStepId?: string
}

export type ActionStartResult = ScenarioActionResult
export type ActionTickResult = ScenarioActionResult

export interface ScenarioPosition {
  x: number
  y: number
  z: number
}

export type ScenarioEntityId = string | number

export interface ScenarioEntity {
  id: ScenarioEntityId
  kind?: string
  type?: string
  name?: string
  health?: number
  dead: boolean
  position: ScenarioPosition
}

export interface ScenarioPathfinderGoal {
  position: ScenarioPosition
  radius: number
}

export interface ScenarioPathfinderResult {
  status: 'set' | 'unavailable' | 'failed'
  message?: string
}

export interface ScenarioPathfinderEvents {
  goalReached: number
  pathFailed: number
}

export interface ScenarioBotCapabilities {
  now(): number
  isSpawned(): boolean
  connectionEndReason(): string | undefined
  getPosition(): ScenarioPosition | undefined
  setPathfinderGoal(goal: ScenarioPathfinderGoal): Promise<ScenarioPathfinderResult>
  clearPathfinderGoal(): void
  pathfinderEvents(): ScenarioPathfinderEvents
  entities(): ScenarioEntity[]
  attack(entityId: ScenarioEntityId): boolean
  isDead(): boolean
  respawn(): void
  spawnEventSeq(): number
  chat(message: string): void
}

export interface ScenarioCancelToken {
  readonly cancelled: boolean
  readonly reason?: string
}

export interface ScenarioActionContext {
  botId: string
  botName: string
  username: string
  runId: string
  generation: number
  cohortKey: string
  stageIndex: number
  step: ScenarioStep
  attempt: number
  actionRunId: string
  startedAt: number
  deadline: number
  runDeadline?: number
  seed?: number
  botOrdinal?: number
  cancelToken: ScenarioCancelToken
  capabilities: ScenarioBotCapabilities
  currentCorrelationToken(): string | undefined
  ensureCorrelationToken(): string
  newCorrelationToken(): string
  lockedEntityId(): ScenarioEntityId | undefined
  setLockedEntityId(entityId: ScenarioEntityId | undefined): void
  templateVariables(): Readonly<Record<string, string | undefined>>
}

export interface ScenarioActionSignal {
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

export interface ScenarioAction {
  start(ctx: ScenarioActionContext): Promise<ActionStartResult>
  tick(ctx: ScenarioActionContext, now: number): Promise<ActionTickResult>
  signal?(ctx: ScenarioActionContext, signal: ScenarioActionSignal): Promise<ActionTickResult>
  cancel(ctx: ScenarioActionContext, reason: string): Promise<void>
  dispose(): Promise<void>
}

export interface ScenarioSignalReceipt {
  signalId: string
  accepted: boolean
  skipped: boolean
  status: string
  errorCode?: string
  error?: string
}

/** 只解析 Control Plane 下发的规范 JSON，不承担 YAML 或复杂业务校验。 */
export function parseScenarioRuntime(input: unknown): ScenarioRuntime {
  const decoded = typeof input === 'string' ? JSON.parse(input) as unknown : input
  const envelope = isRecord(decoded) && isRecord(decoded.scenario) ? decoded : undefined
  const value = envelope?.scenario ?? decoded
  if (!isRecord(value) || typeof value.key !== 'string' || !Array.isArray(value.steps)) {
    throw new Error('Scenario JSON 缺少 key 或 steps')
  }
  if (value.key.trim() === '' || value.steps.length === 0) {
    throw new Error('Scenario JSON 的 key 和 steps 不能为空')
  }
  return {
    key: value.key,
    percent: finiteNumber(value.percent, 100),
    seed: optionalFiniteNumber(envelope?.seed) ?? optionalFiniteNumber(value.seed),
    botOrdinal: optionalFiniteNumber(envelope?.botOrdinal) ?? optionalFiniteNumber(value.botOrdinal),
    steps: value.steps.map((step, index) => parseStep(step, index)),
  }
}

function parseStep(input: unknown, index: number): ScenarioStep {
  if (!isRecord(input) || typeof input.id !== 'string' || typeof input.type !== 'string') {
    throw new Error(`Scenario step ${index} 缺少 id 或 type`)
  }
  return {
    ...input,
    id: input.id,
    type: input.type,
    timeoutMs: finiteNumber(input.timeoutMs, defaultTimeout(input.type)),
    maxAttempts: finiteNumber(input.maxAttempts, 1),
    retryBackoffMs: finiteNumber(input.retryBackoffMs, 0),
    resumePolicy: parseResumePolicy(input.resumePolicy),
  } as ScenarioStep
}

function parseResumePolicy(value: unknown): ScenarioResumePolicy {
  if (value === 'restart_scenario' || value === 'fail') return value
  return 'restart_step'
}

function defaultTimeout(type: string): number {
  if (type === 'send_command') return 10_000
  if (type === 'barrier') return 60_000
  if (type === 'wait') return 3_600_000
  return 30_000
}

function finiteNumber(value: unknown, fallback: number): number {
  return optionalFiniteNumber(value) ?? fallback
}

function optionalFiniteNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}
