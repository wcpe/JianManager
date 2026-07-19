import { createHash, randomUUID } from 'node:crypto'
import type { ActionEvent } from '../ipc/types.js'
import { createScenarioAction } from './actions.js'
import {
  parseScenarioRuntime,
  type ScenarioAction,
  type ScenarioActionContext,
  type ScenarioActionResult,
  type ScenarioActionSignal,
  type ScenarioBotCapabilities,
  type ScenarioRuntime,
  type ScenarioSignalReceipt,
  type ScenarioStep,
} from './types.js'

const MAX_RESULT_BYTES = 12 * 1024
const MAX_SIGNAL_HISTORY = 1000
const MAX_TERMINAL_HISTORY = 100

type ScenarioActionFactory = (step: ScenarioStep, context: ScenarioActionContext) => ScenarioAction

export interface ScenarioRunnerOptions {
  botId: string
  botName: string
  username: string
  runId: string
  generation: number
  cohortKey: string
  scenario: unknown
  resumeStepId?: string
  correlationSeed?: string
  stageIndex?: number
  roomKey?: string
  capabilities: ScenarioBotCapabilities
  emitActionEvent: (event: ActionEvent['action']) => void
  actionFactory?: ScenarioActionFactory
  nextActionRunId?: () => string
}

interface CurrentAttempt {
  step: ScenarioStep
  action: ScenarioAction
  context: ScenarioActionContext
}

interface TerminalAttempt {
  botId: string
  runId: string
  generation: number
  actionRunId: string
  step: ScenarioStep
  correlationToken?: string
}

/** 每 Bot 唯一的 Scenario V2 动作状态机，由外部集中 scheduler 驱动。 */
export class ScenarioRunner {
  private readonly options: ScenarioRunnerOptions
  private readonly runtime: ScenarioRuntime
  private readonly actionFactory: ScenarioActionFactory
  private readonly nextActionRunId: () => string
  private readonly signalHistory = new Map<string, ScenarioSignalReceipt>()
  private readonly terminalHistory = new Map<string, TerminalAttempt>()
  private operation = Promise.resolve()
  private current: CurrentAttempt | null = null
  private stepIndex = 0
  private attempt = 0
  private retryAt: number | undefined
  private correlationToken: string | undefined
  private correlationCounter = 0
  private readonly cancelToken = { cancelled: false, reason: undefined as string | undefined }
  private started = false
  private terminal = false
  private disposed = false

  constructor(options: ScenarioRunnerOptions) {
    this.options = options
    this.runtime = parseScenarioRuntime(options.scenario)
    if (this.runtime.key !== options.cohortKey) throw new Error('Scenario cohortKey 与 assignment 不匹配')
    this.actionFactory = options.actionFactory ?? ((step) => createScenarioAction(step))
    this.nextActionRunId = options.nextActionRunId ?? randomUUID
  }

  get currentStepId(): string | undefined {
    return this.current?.step.id ?? this.runtime.steps[this.stepIndex]?.id
  }

  get isTerminal(): boolean {
    return this.terminal
  }

  start(now = this.options.capabilities.now()): Promise<void> {
    return this.enqueue(() => this.startInternal(now))
  }

  tick(now = this.options.capabilities.now()): Promise<void> {
    return this.enqueue(() => this.tickInternal(now))
  }

  signal(signal: ScenarioActionSignal, now = this.options.capabilities.now()): Promise<ScenarioSignalReceipt> {
    return this.enqueue(() => this.signalInternal(signal, now))
  }

  cancel(reason: string, now = this.options.capabilities.now()): Promise<void> {
    return this.enqueue(() => this.cancelInternal(reason, now))
  }

  connectionEnded(reason: string, now = this.options.capabilities.now()): Promise<void> {
    return this.enqueue(() => this.connectionEndedInternal(reason, now))
  }

  dispose(): Promise<void> {
    return this.enqueue(() => this.disposeInternal())
  }

  private enqueue<T>(task: () => Promise<T>): Promise<T> {
    const next = this.operation.then(task, task)
    this.operation = next.then(() => undefined, () => undefined)
    return next
  }

  private async startInternal(now: number): Promise<void> {
    if (this.started || this.disposed) return
    this.started = true
    const resume = this.resolveResume()
    this.stepIndex = resume.index
    if (resume.rejected) {
      await this.rejectResume(now)
      return
    }
    await this.startAttempt(now)
  }

  private resolveResume(): { index: number; rejected: boolean } {
    const resumeStepId = this.options.resumeStepId
    if (!resumeStepId) return { index: 0, rejected: false }
    const index = this.runtime.steps.findIndex((step) => step.id === resumeStepId)
    if (index < 0) throw new Error('resumeStepId 不存在于 Scenario JSON')
    const policy = this.runtime.steps[index].resumePolicy
    if (policy === 'restart_scenario') return { index: 0, rejected: false }
    return { index, rejected: policy === 'fail' }
  }

  private async rejectResume(now: number): Promise<void> {
    await this.createCurrent(now)
    if (!this.current) return
    this.emit('running', {}, now)
    await this.completeAttempt('failed', {
      state: 'failed', errorCode: 'ACTION_INTERNAL_ERROR', message: 'resumePolicy=fail 拒绝恢复动作',
    }, now)
  }

  private async tickInternal(now: number): Promise<void> {
    if (!this.started || this.terminal || this.disposed) return
    if (!this.current) {
      if (this.retryAt !== undefined && now >= this.retryAt) await this.startAttempt(now)
      return
    }
    const attempt = this.current
    const result = await this.callAction(() => attempt.action.tick(attempt.context, now))
    if (this.current !== attempt) return
    if (result.state !== 'running') {
      await this.completeAttempt(result.state, result, now)
      return
    }
    if (now >= attempt.context.deadline) await this.timeoutAttempt(now)
  }

  private async startAttempt(now: number): Promise<void> {
    if (this.terminal || this.disposed || this.stepIndex >= this.runtime.steps.length) {
      this.terminal = true
      return
    }
    this.retryAt = undefined
    this.attempt++
    await this.createCurrent(now)
    if (!this.current) return
    const result = await this.callAction(() => this.current!.action.start(this.current!.context))
    this.emit('running', result, now)
    if (result.state !== 'running') await this.completeAttempt(result.state, result, now)
  }

  private async createCurrent(now: number): Promise<void> {
    const step = this.runtime.steps[this.stepIndex]
    const actionRunId = this.nextActionRunId()
    const context = this.createContext(step, actionRunId, now)
    context.ensureCorrelationToken()
    this.current = { step, context, action: this.actionFactory(step, context) }
  }

  private createContext(step: ScenarioStep, actionRunId: string, now: number): ScenarioActionContext {
    return {
      botId: this.options.botId, botName: this.options.botName, username: this.options.username,
      runId: this.options.runId, generation: this.options.generation, cohortKey: this.options.cohortKey,
      stageIndex: this.options.stageIndex ?? 0, step, attempt: this.attempt, actionRunId,
      startedAt: now, deadline: now + step.timeoutMs, cancelToken: this.cancelToken,
      capabilities: this.options.capabilities,
      currentCorrelationToken: () => this.correlationToken,
      ensureCorrelationToken: () => this.ensureCorrelationToken(),
      newCorrelationToken: () => this.newCorrelationToken(),
      templateVariables: () => this.templateVariables(step),
    }
  }

  private async completeAttempt(
    status: 'succeeded' | 'failed',
    result: ScenarioActionResult,
    now: number
  ): Promise<void> {
    if (!this.current) return
    this.emit(status, result, now)
    this.rememberTerminal(this.current)
    const step = this.current.step
    await this.cleanupCurrent()
    if (status !== 'succeeded' && this.attempt < step.maxAttempts) {
      this.retryAt = now + step.retryBackoffMs
      return
    }
    if (status !== 'succeeded') {
      this.terminal = true
      return
    }
    this.stepIndex++
    this.attempt = 0
    if (this.stepIndex >= this.runtime.steps.length) {
      this.terminal = true
      return
    }
    await this.startAttempt(now)
  }

  private async timeoutAttempt(now: number): Promise<void> {
    if (!this.current) return
    const errorCode = timeoutErrorCode(this.current.step.type)
    this.emit('timed_out', { state: 'failed', errorCode, message: '动作执行超时' }, now)
    this.rememberTerminal(this.current)
    const step = this.current.step
    await this.cleanupCurrent()
    if (this.attempt < step.maxAttempts) {
      this.retryAt = now + step.retryBackoffMs
    } else {
      this.terminal = true
    }
  }

  private async cancelInternal(reason: string, now: number): Promise<void> {
    if (this.terminal || this.disposed) return
    this.cancelToken.cancelled = true
    this.cancelToken.reason = reason
    if (this.current) {
      await this.ignoreErrors(() => this.current!.action.cancel(this.current!.context, reason))
      this.emit('cancelled', {
        state: 'failed', errorCode: 'ACTION_CANCELLED', message: reason,
      }, now)
      this.rememberTerminal(this.current)
      await this.cleanupCurrent()
    }
    this.retryAt = undefined
    this.terminal = true
  }

  private async connectionEndedInternal(reason: string, now: number): Promise<void> {
    if (this.current?.step.type === 'wait_spawn') {
      await this.tickInternal(now)
      if (this.terminal || !this.current) return
    }
    await this.cancelInternal(`连接已结束: ${reason}`, now)
  }

  private async disposeInternal(): Promise<void> {
    if (this.disposed) return
    if (!this.terminal) {
      this.cancelToken.cancelled = true
      this.cancelToken.reason = 'Runner 已释放'
    }
    await this.cleanupCurrent()
    this.retryAt = undefined
    this.disposed = true
  }

  private async signalInternal(signal: ScenarioActionSignal, now: number): Promise<ScenarioSignalReceipt> {
    const replay = this.signalHistory.get(signal.signalId)
    if (replay) return { ...replay, accepted: true, skipped: true }
    const receipt = await this.routeSignal(signal, now)
    this.rememberSignal(receipt)
    return receipt
  }

  private async routeSignal(signal: ScenarioActionSignal, now: number): Promise<ScenarioSignalReceipt> {
    if (!signal.signalId) return skippedSignal(signal.signalId, 'association_mismatch', 'signalId 不能为空')
    if (this.terminalHistory.has(signal.actionRunId) || !this.current) return this.routeLateSignal(signal)
    const mismatch = associationMismatch(this.current, this.options, signal)
    if (mismatch) return skippedSignal(signal.signalId, 'association_mismatch', mismatch)
    if (signal.type === 'cancel') {
      await this.cancelInternal('收到运行取消信号', now)
      return acceptedSignal(signal.signalId)
    }
    if (!this.current.action.signal) return skippedSignal(signal.signalId, 'signal_type_mismatch', '当前动作不接受信号')
    const attempt = this.current
    const result = await this.callAction(() => attempt.action.signal!(attempt.context, signal))
    if (this.current !== attempt) return acceptedSignal(signal.signalId)
    if (!result.signalAccepted && result.state === 'running') {
      return skippedSignal(signal.signalId, 'signal_type_mismatch', result.message ?? '信号类型不匹配')
    }
    if (result.state !== 'running') await this.completeAttempt(result.state, result, now)
    return acceptedSignal(signal.signalId)
  }

  private routeLateSignal(signal: ScenarioActionSignal): ScenarioSignalReceipt {
    const terminal = this.terminalHistory.get(signal.actionRunId)
    if (!terminal) return skippedSignal(signal.signalId, 'action_not_waiting', '动作已终态或不存在')
    const mismatch = terminalAssociationMismatch(terminal, signal)
    if (mismatch) return skippedSignal(signal.signalId, 'association_mismatch', mismatch)
    if (!signalTypeMatches(terminal.step, signal)) {
      return skippedSignal(signal.signalId, 'signal_type_mismatch', '信号类型不匹配')
    }
    return { ...acceptedSignal(signal.signalId), skipped: true }
  }

  private emit(status: ActionEvent['action']['status'], result: Partial<ScenarioActionResult>, now: number): void {
    if (!this.current) return
    const { context } = this.current
    this.options.emitActionEvent({
      botId: this.options.botId, sessionId: this.options.runId, generation: this.options.generation,
      actionRunId: context.actionRunId, stepId: context.step.id, attempt: context.attempt,
      status, errorCode: result.errorCode, message: result.message,
      correlationToken: result.correlationToken ?? this.correlationToken,
      result: boundedResult(result.result), durationMs: Math.max(0, now - context.startedAt), observedAt: now,
    })
  }

  private async cleanupCurrent(): Promise<void> {
    const current = this.current
    if (!current) return
    this.current = null
    await this.ignoreErrors(() => current.action.dispose())
    this.options.capabilities.clearPathfinderGoal()
  }

  private async callAction(call: () => Promise<ScenarioActionResult>): Promise<ScenarioActionResult> {
    try {
      return await call()
    } catch (error) {
      return { state: 'failed', errorCode: 'ACTION_INTERNAL_ERROR', message: String(error) }
    }
  }

  private async ignoreErrors(call: () => Promise<void>): Promise<void> {
    try {
      await call()
    } catch {
      return
    }
  }

  private ensureCorrelationToken(): string {
    return this.correlationToken ?? this.newCorrelationToken()
  }

  private newCorrelationToken(): string {
    this.correlationCounter++
    const seed = this.options.correlationSeed
    this.correlationToken = seed
      ? createHash('sha256').update(`${seed}|${this.correlationCounter}`).digest('hex')
      : randomUUID()
    return this.correlationToken
  }

  private templateVariables(step: ScenarioStep): Readonly<Record<string, string | undefined>> {
    return {
      botName: this.options.botName, botUuid: this.options.botId, runId: this.options.runId,
      cohortKey: this.options.cohortKey, correlationToken: this.correlationToken,
      roomKey: this.options.roomKey,
      stepId: step.id,
    }
  }

  private rememberSignal(receipt: ScenarioSignalReceipt): void {
    while (this.signalHistory.size >= MAX_SIGNAL_HISTORY) deleteOldest(this.signalHistory)
    this.signalHistory.set(receipt.signalId, { ...receipt })
  }

  private rememberTerminal(current: CurrentAttempt): void {
    while (this.terminalHistory.size >= MAX_TERMINAL_HISTORY) deleteOldest(this.terminalHistory)
    this.terminalHistory.set(current.context.actionRunId, {
      botId: this.options.botId, runId: this.options.runId, generation: this.options.generation,
      actionRunId: current.context.actionRunId, step: current.step,
      correlationToken: this.correlationToken,
    })
  }
}

function timeoutErrorCode(type: string): string {
  if (type === 'wait_probe_event') return 'PROBE_EVENT_TIMEOUT'
  if (type === 'barrier') return 'BARRIER_TIMEOUT'
  return 'ACTION_INTERNAL_ERROR'
}

function associationMismatch(
  current: CurrentAttempt,
  options: ScenarioRunnerOptions,
  signal: ScenarioActionSignal
): string | undefined {
  if (signal.botId !== options.botId || signal.sessionId !== options.runId) return '运行或 Bot 关联不匹配'
  if (signal.generation !== options.generation) return 'generation 不匹配'
  if (signal.actionRunId !== current.context.actionRunId || signal.stepId !== current.step.id) return '动作关联不匹配'
  if (signal.correlationToken !== current.context.currentCorrelationToken()) return 'correlationToken 不匹配'
  return undefined
}

function terminalAssociationMismatch(terminal: TerminalAttempt, signal: ScenarioActionSignal): string | undefined {
  if (signal.botId !== terminal.botId || signal.sessionId !== terminal.runId) return '运行或 Bot 关联不匹配'
  if (signal.generation !== terminal.generation || signal.actionRunId !== terminal.actionRunId) return '动作代际关联不匹配'
  if (signal.stepId !== terminal.step.id || signal.correlationToken !== terminal.correlationToken) return '动作 token 关联不匹配'
  return undefined
}

function signalTypeMatches(step: ScenarioStep, signal: ScenarioActionSignal): boolean {
  if (signal.type === 'cancel') return true
  if (step.type === 'barrier') return signal.type === 'barrier-release'
  if (step.type !== 'wait_probe_event') return false
  if (signal.type === step.event) return true
  const payload = signal.payload as Record<string, unknown> | undefined
  return signal.type === 'probe' && payload?.eventType === step.event
}

function acceptedSignal(signalId: string): ScenarioSignalReceipt {
  return { signalId, accepted: true, skipped: false, status: 'accepted' }
}

function skippedSignal(signalId: string, errorCode: string, error: string): ScenarioSignalReceipt {
  return { signalId, accepted: false, skipped: true, status: 'skipped', errorCode, error }
}

function boundedResult(result: unknown): unknown {
  if (result === undefined) return undefined
  try {
    const raw = JSON.stringify(result)
    if (Buffer.byteLength(raw) <= MAX_RESULT_BYTES) return result
    return { truncated: true, originalBytes: Buffer.byteLength(raw) }
  } catch {
    return { truncated: true, reason: 'result 无法序列化' }
  }
}

function deleteOldest<T>(entries: Map<string, T>): void {
  const oldest = entries.keys().next().value as string | undefined
  if (oldest !== undefined) entries.delete(oldest)
}
