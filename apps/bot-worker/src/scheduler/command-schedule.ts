/**
 * FR-369 通用 Bot 命令编排：Bot Worker 本地集中 scheduler。
 *
 * 单一职责：每个 scheduleRunId 由一个本地 scheduler 统一管理 occurrence 计划；
 *  - 每个 occurrence 有唯一 actionRunId（CP 冻结，本地不再生成）；
 *  - 集中计时器：所有未开始 occurrence 按 plannedAtUnixMs 升序维护，单一 setTimeout 等待下个到期；
 *  - 调用 bot.chat 同步抛错即失败，未抛错即成功（ADR-075）；
 *  - V1 固定重试：bot.chat 同步抛错最多 3 次尝试，固定退避 250ms/500ms，其他错误不重试；
 *  - 取消幂等：未开始 occurrence 立即写 cancelled；正在执行的同步 bot.chat 不强制中断，完成后停。
 */

import { EventEmitter } from 'node:events'

import type {
  CommandAttemptError,
  CommandScheduleCommand,
  CommandScheduleReleaseCommand,
  CommandScheduleCancelCommand,
  CommandScheduleResultEvent,
} from '../ipc/types.js'

export type CommandSchedulerSendEvent = (event: CommandScheduleResultEvent) => void

function defaultSendEvent(event: CommandScheduleResultEvent): void {
  // 默认实现：写入 stdout 单行 JSON；测试可通过 sendEvent 注入替身避免网络副作用。
  process.stdout.write(JSON.stringify(event) + '\n')
}

interface McBotLike {
  username?: string
  chat(message: string): void
}

type OccurrenceStatus = 'pending' | 'sent' | 'failed' | 'timed_out' | 'cancelled'

interface ScheduleOccurrence {
  commandId: string
  occurrence: number
  actionRunId: string
  command: string
  baseAtMs: number
  jitterOffsetMs: number
  plannedAtUnixMs: number
  status: OccurrenceStatus
  attempt: number
  durationMs: number
  observedAtUnixMs: number
  sentAtUnixMs: number | null
  attemptErrors: CommandAttemptError[]
  skip: boolean
}

interface ScheduleEntry {
  runId: string
  runUuid: string
  botUuid: string
  generation: number
  stepId: string
  scheduleRunId: string
  correlationToken: string
  startMode: 'absolute' | 'barrier'
  scheduleStartAtUnixMs: number
  barrierKey?: string
  releaseAt?: number
  runDeadlineUnixMs: number
  jitterSeed: string
  occurrences: ScheduleOccurrence[]
  state: 'prepared' | 'released' | 'cancelling' | 'cancelled' | 'finished'
  cancelRequested: boolean
  pendingTimer: NodeJS.Timeout | null
  inflight: boolean
}

interface AttemptOutcome {
  status: 'sent' | 'failed' | 'timed_out'
  attempt: number
  durationMs: number
  observedAtUnixMs: number
  sentAtUnixMs: number | null
  plannedAtUnixMs?: number
  attemptErrors: CommandAttemptError[]
  errorCode?: string
  message?: string
}

const MAX_OCCURRENCE_ATTEMPTS = 3
const RETRY_BACKOFFS_MS: readonly number[] = [250, 500]
const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const COMMAND_ID_PATTERN = /^[A-Za-z0-9._-]+$/
const JITTER_SEED_PATTERN = /^(0|[1-9][0-9]{0,19})$/

export interface CommandSchedulerOptions {
  getBot: (botUuid: string) => McBotLike | null | undefined
  chat: (bot: McBotLike, command: string) => void
  now?: () => number
  sleep?: (ms: number) => Promise<void>
  logger?: { warn(message: string, context?: Record<string, unknown>): void }
  tombstoneTtlMs?: number
  tombstoneCapacity?: number
  sendEvent?: CommandSchedulerSendEvent
}

export type CommandSchedulerEmit =
  | { kind: 'result'; event: CommandScheduleResultEvent }

/** 结果经构造注入的 sendEvent 上报；EventEmitter 仅保留内部扩展点，不做 class/interface 合并声明。 */
export class CommandScheduler extends EventEmitter {
  private readonly schedules = new Map<string, ScheduleEntry>()
  private readonly tombstone = new Map<string, number>()
  private readonly getBot: (botUuid: string) => McBotLike | null | undefined
  private readonly chat: (bot: McBotLike, command: string) => void
  private readonly now: () => number
  private readonly sleep: (ms: number) => Promise<void>
  private readonly logger: { warn(message: string, context?: Record<string, unknown>): void }
  private readonly tombstoneTtlMs: number
  private readonly tombstoneCapacity: number
  private readonly sendEventFn: CommandSchedulerSendEvent

  constructor(options: CommandSchedulerOptions) {
    super()
    this.getBot = options.getBot
    this.chat = options.chat
    this.now = options.now ?? Date.now
    this.sleep = options.sleep ?? defaultSleep
    this.logger = options.logger ?? { warn() {} }
    this.tombstoneTtlMs = options.tombstoneTtlMs ?? 30 * 60 * 1000
    this.tombstoneCapacity = options.tombstoneCapacity ?? 10_000
    this.sendEventFn = options.sendEvent ?? defaultSendEvent
  }

  apply(command: CommandScheduleCommand):
    | { ok: true; accepted: true; alreadyCancelled?: boolean }
    | { ok: false; errorCode: string; error: string } {
    const tombKey = scheduleTombstoneKey(command.runUuid, command.botUuid, command.generation, command.scheduleRunId)
    this.pruneTombstone()
    if (this.tombstone.has(tombKey)) {
      return { ok: true, accepted: true, alreadyCancelled: true }
    }
    if (command.runDeadlineUnixMs <= this.now()) {
      return { ok: false, errorCode: 'COMMAND_DEADLINE_EXCEEDED', error: '运行 deadline 已过' }
    }
    const validation = validateApply(command)
    if (validation !== true) return { ok: false, errorCode: validation.errorCode, error: validation.error }
    const skipSet = new Set<string>()
    for (const skip of command.skipOccurrences ?? []) {
      skipSet.add(commandOccurrenceKey(skip.commandId, skip.occurrence))
      const matched = command.plan.occurrences.some((occ) => occ.commandId === skip.commandId && occ.occurrence === skip.occurrence)
      if (!matched) {
        return { ok: false, errorCode: 'COMMAND_ARGUMENT_INVALID', error: `skipOccurrence 引用不存在 ${skip.commandId}/${skip.occurrence}` }
      }
    }
    const existing = this.schedules.get(command.scheduleRunId)
    if (existing && existing.state !== 'cancelled' && existing.state !== 'finished') {
      return { ok: false, errorCode: 'COMMAND_SCHEDULE_REJECTED', error: 'scheduleRunId 已存在活动计划' }
    }
    if (existing) this.schedules.delete(command.scheduleRunId)
    const state: ScheduleEntry['state'] = command.startMode === 'barrier' ? 'prepared' : 'released'
    const occurrences = command.plan.occurrences.map<ScheduleOccurrence>((occ) => ({
      commandId: occ.commandId,
      occurrence: occ.occurrence,
      actionRunId: occ.actionRunId,
      command: occ.command,
      baseAtMs: occ.baseAtMs,
      jitterOffsetMs: occ.jitterOffsetMs,
      plannedAtUnixMs: 0,
      status: 'pending',
      attempt: 0,
      durationMs: 0,
      observedAtUnixMs: 0,
      sentAtUnixMs: null,
      attemptErrors: [],
      skip: skipSet.has(commandOccurrenceKey(occ.commandId, occ.occurrence)),
    }))
    const entry: ScheduleEntry = {
      runId: command.runId,
      runUuid: command.runUuid,
      botUuid: command.botUuid,
      generation: command.generation,
      stepId: command.stepId,
      scheduleRunId: command.scheduleRunId,
      correlationToken: command.correlationToken,
      startMode: command.startMode,
      scheduleStartAtUnixMs: command.startMode === 'absolute' ? command.scheduleStartAtUnixMs ?? 0 : 0,
      barrierKey: command.barrierKey,
      runDeadlineUnixMs: command.runDeadlineUnixMs,
      jitterSeed: command.jitterSeed,
      occurrences,
      state,
      cancelRequested: false,
      pendingTimer: null,
      inflight: false,
    }
    this.schedules.set(command.scheduleRunId, entry)
    if (state === 'released') this.activateEntry(entry)
    return { ok: true, accepted: true }
  }

  release(command: CommandScheduleReleaseCommand):
    | { ok: true; accepted: true; alreadyReleased?: boolean }
    | { ok: false; errorCode: string; error: string } {
    const entry = this.schedules.get(command.scheduleRunId)
    if (!entry) {
      return { ok: false, errorCode: 'COMMAND_SCHEDULE_REJECTED', error: '计划不存在或已结束' }
    }
    if (entry.barrierKey !== command.barrierKey) {
      return { ok: false, errorCode: 'COMMAND_SCHEDULE_REJECTED', error: 'barrierKey 不匹配' }
    }
    if (entry.state === 'cancelled' || entry.state === 'finished') {
      return { ok: false, errorCode: 'COMMAND_SCHEDULE_REJECTED', error: '计划已结束' }
    }
    if (entry.state === 'released') {
      if (entry.releaseAt === command.releaseAtUnixMs) {
        return { ok: true, accepted: true, alreadyReleased: true }
      }
      return { ok: false, errorCode: 'COMMAND_SCHEDULE_REJECTED', error: '同一计划使用不同 releaseAt' }
    }
    if (command.releaseAtUnixMs <= this.now() || command.releaseAtUnixMs >= entry.runDeadlineUnixMs) {
      return { ok: false, errorCode: 'COMMAND_DEADLINE_EXCEEDED', error: 'releaseAt 非法' }
    }
    entry.state = 'released'
    entry.scheduleStartAtUnixMs = command.releaseAtUnixMs
    entry.releaseAt = command.releaseAtUnixMs
    this.activateEntry(entry)
    return { ok: true, accepted: true }
  }

  cancel(command: CommandScheduleCancelCommand):
    | { ok: true; accepted: true; alreadyCancelled?: boolean }
    | { ok: false; errorCode: string; error: string } {
    const tombKey = scheduleTombstoneKey(command.runUuid, command.botUuid, command.generation, command.scheduleRunId)
    this.pruneTombstone()
    if (this.tombstone.has(tombKey)) {
      return { ok: true, accepted: true, alreadyCancelled: true }
    }
    const entry = this.schedules.get(command.scheduleRunId)
    this.recordTombstone(tombKey)
    if (!entry) return { ok: true, accepted: true, alreadyCancelled: true }
    if (entry.state === 'cancelled' || entry.state === 'finished') {
      return { ok: true, accepted: true, alreadyCancelled: true }
    }
    entry.cancelRequested = true
    entry.state = 'cancelling'
    if (entry.pendingTimer) {
      clearTimeout(entry.pendingTimer)
      entry.pendingTimer = null
    }
    this.cancelPendingOccurrences(entry)
    if (!entry.inflight) this.finalizeEntry(entry)
    return { ok: true, accepted: true }
  }

  shutdown(): void {
    for (const entry of this.schedules.values()) {
      entry.cancelRequested = true
      if (entry.pendingTimer) clearTimeout(entry.pendingTimer)
      this.cancelPendingOccurrences(entry)
      entry.state = 'cancelled'
      this.schedules.delete(entry.scheduleRunId)
    }
  }

  debugState(): { schedules: number; tombstones: number } {
    this.pruneTombstone()
    return { schedules: this.schedules.size, tombstones: this.tombstone.size }
  }

  private activateEntry(entry: ScheduleEntry): void {
    const start = entry.scheduleStartAtUnixMs
    // 按 plannedAtUnixMs 升序排序：baseAtMs + jitterOffsetMs，单毫秒按声明索引+occurrence 稳定次序。
    entry.occurrences.sort((a, b) => {
      const plannedA = start + a.baseAtMs + a.jitterOffsetMs
      const plannedB = start + b.baseAtMs + b.jitterOffsetMs
      if (plannedA !== plannedB) return plannedA - plannedB
      if (a.occurrence !== b.occurrence) return a.occurrence - b.occurrence
      return a.commandId < b.commandId ? -1 : 1
    })
    let previous = start
    for (const occ of entry.occurrences) {
      const raw = start + occ.baseAtMs + occ.jitterOffsetMs
      const clamped = Math.min(Math.max(raw, previous), entry.runDeadlineUnixMs - 1)
      occ.plannedAtUnixMs = clamped
      previous = clamped
    }
    this.scheduleNextWakeup(entry)
  }

  private scheduleNextWakeup(entry: ScheduleEntry): void {
    let nextOccurrence: ScheduleOccurrence | undefined
    for (const occ of entry.occurrences) {
      if (occ.skip || occ.status !== 'pending') continue
      if (!nextOccurrence || occ.plannedAtUnixMs < nextOccurrence.plannedAtUnixMs) {
        nextOccurrence = occ
      }
    }
    if (!nextOccurrence) {
      this.finalizeEntry(entry)
      return
    }
    if (entry.pendingTimer) clearTimeout(entry.pendingTimer)
    const delay = Math.max(nextOccurrence.plannedAtUnixMs - this.now(), 0)
    // unref：避免单元测试/无活动引用时因悬挂定时器阻止进程退出。
    const timer = setTimeout(() => this.tickEntry(entry), delay)
    if (typeof timer.unref === 'function') timer.unref()
    entry.pendingTimer = timer
  }

  private tickEntry(entry: ScheduleEntry): void {
    entry.pendingTimer = null
    if (entry.state === 'cancelled' || entry.state === 'finished') return
    if (entry.inflight) return
    if (entry.cancelRequested) entry.state = 'cancelling'
    const now = this.now()
    const due = entry.occurrences.find((occ) => !occ.skip && occ.status === 'pending' && occ.plannedAtUnixMs <= now)
    if (!due) {
      this.scheduleNextWakeup(entry)
      return
    }
    if (now >= entry.runDeadlineUnixMs) {
      due.status = 'timed_out'
      due.attempt = 1
      due.observedAtUnixMs = now
      this.emitResult(entry, due, {
        status: 'timed_out',
        attempt: 1,
        durationMs: 0,
        observedAtUnixMs: now,
        sentAtUnixMs: null,
        attemptErrors: [],
        errorCode: 'COMMAND_DEADLINE_EXCEEDED',
        message: '运行 deadline 已到',
      })
      this.scheduleNextWakeup(entry)
      return
    }
    if (entry.cancelRequested) {
      due.status = 'cancelled'
      due.attempt = 1
      due.observedAtUnixMs = now
      this.emitResult(entry, due, {
        status: 'failed',
        attempt: 1,
        durationMs: 0,
        observedAtUnixMs: now,
        sentAtUnixMs: null,
        attemptErrors: [],
        errorCode: 'ACTION_CANCELLED',
        message: '取消已生效',
      })
      this.scheduleNextWakeup(entry)
      return
    }
    entry.inflight = true
    void this.runOccurrence(entry, due).finally(() => {
      entry.inflight = false
      this.scheduleNextWakeup(entry)
    })
  }

  private async runOccurrence(entry: ScheduleEntry, occ: ScheduleOccurrence): Promise<void> {
    const outcome = await executeOccurrence({
      plannedAtUnixMs: occ.plannedAtUnixMs,
      command: occ.command,
      deadlineUnixMs: entry.runDeadlineUnixMs,
      now: () => this.now(),
      getBot: () => this.getBot(entry.botUuid),
      chat: (bot, command) => { this.chat(bot, command) },
      sleep: (ms) => this.sleep(ms),
    })
    occ.attempt = outcome.attempt
    occ.durationMs = outcome.durationMs
    occ.observedAtUnixMs = outcome.observedAtUnixMs
    occ.attemptErrors = outcome.attemptErrors
    occ.sentAtUnixMs = outcome.sentAtUnixMs
    occ.status = outcome.status
    this.emitResult(entry, occ, outcome)
  }

  private emitResult(entry: ScheduleEntry, occ: ScheduleOccurrence, outcome: AttemptOutcome): void {
    const status = (occ.status === 'pending' ? 'failed' : occ.status) as CommandScheduleResultEvent['status']
    const event: CommandScheduleResultEvent = {
      evt: 'command-schedule-result',
      runId: entry.runId,
      runUuid: entry.runUuid,
      botUuid: entry.botUuid,
      generation: entry.generation,
      stepId: entry.stepId,
      scheduleRunId: entry.scheduleRunId,
      actionRunId: occ.actionRunId,
      correlationToken: entry.correlationToken,
      commandId: occ.commandId,
      occurrence: occ.occurrence,
      attempt: outcome.attempt,
      durationMs: outcome.durationMs,
      observedAtUnixMs: outcome.observedAtUnixMs,
      status,
      plannedAtUnixMs: occ.plannedAtUnixMs,
      sentAtUnixMs: outcome.sentAtUnixMs,
      attemptErrors: outcome.attemptErrors,
      errorCode: outcome.errorCode,
      message: outcome.message,
    }
    this.emit('emit', { kind: 'result', event })
    this.sendEventFn(event)
  }

  private finalizeEntry(entry: ScheduleEntry): void {
    if (entry.state === 'finished' || entry.state === 'cancelled') return
    if (entry.pendingTimer) {
      clearTimeout(entry.pendingTimer)
      entry.pendingTimer = null
    }
    entry.state = entry.cancelRequested ? 'cancelled' : 'finished'
    this.recordTombstone(scheduleTombstoneKey(entry.runUuid, entry.botUuid, entry.generation, entry.scheduleRunId))
    this.schedules.delete(entry.scheduleRunId)
  }

  private cancelPendingOccurrences(entry: ScheduleEntry): void {
    const now = this.now()
    for (const occ of entry.occurrences) {
      if (occ.status === 'pending' && !occ.skip) {
        occ.status = 'cancelled'
        occ.attempt = 1
        occ.durationMs = 0
        occ.observedAtUnixMs = now
        // emitResult 以 occ.status 为准，事件 status=cancelled，errorCode=ACTION_CANCELLED。
        this.emitResult(entry, occ, {
          status: 'failed',
          attempt: 1,
          durationMs: 0,
          observedAtUnixMs: now,
          sentAtUnixMs: null,
          attemptErrors: [],
          errorCode: 'ACTION_CANCELLED',
          message: '取消已生效',
        })
      }
    }
  }

  private recordTombstone(key: string): void {
    if (this.tombstone.size >= this.tombstoneCapacity) {
      const oldest = this.tombstone.keys().next().value
      if (oldest) this.tombstone.delete(oldest)
    }
    this.tombstone.set(key, this.now())
  }

  private pruneTombstone(): void {
    const cutoff = this.now() - this.tombstoneTtlMs
    for (const [key, ts] of this.tombstone.entries()) {
      if (ts < cutoff) this.tombstone.delete(key)
    }
  }
}

function commandOccurrenceKey(commandId: string, occurrence: number): string {
  return `${commandId}\u0000${occurrence}`
}

function scheduleTombstoneKey(runUuid: string, botUuid: string, generation: number, scheduleRunId: string): string {
  return `${runUuid}\u0000${botUuid.toLowerCase()}\u0000${generation}\u0000${scheduleRunId}`
}

function validateApply(command: CommandScheduleCommand): true | { errorCode: string; error: string } {
  if (!UUID_PATTERN.test(command.runUuid)) return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'runUuid 非法' }
  if (!UUID_PATTERN.test(command.botUuid)) return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'botUuid 非法' }
  if (!UUID_PATTERN.test(command.scheduleRunId)) return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'scheduleRunId 非法' }
  if (command.generation <= 0) return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'generation 非法' }
  if (!command.stepId) return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'stepId 必填' }
  if (!command.correlationToken) return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'correlationToken 必填' }
  if (!JITTER_SEED_PATTERN.test(command.jitterSeed)) return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'jitterSeed 非法' }
  if (command.plan.durationMs < 1 || command.plan.durationMs > 86_400_000) {
    return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'durationMs 越界' }
  }
  if (command.plan.jitterMs < 0 || command.plan.jitterMs > Math.min(60_000, command.plan.durationMs)) {
    return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'jitterMs 越界' }
  }
  if (command.startMode === 'absolute') {
    if (!command.scheduleStartAtUnixMs || command.scheduleStartAtUnixMs <= 0) {
      return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'absolute 模式 scheduleStartAtUnixMs 必填' }
    }
  } else if (command.startMode === 'barrier') {
    if (!command.barrierKey) return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'barrier 模式 barrierKey 必填' }
  } else {
    return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'startMode 非法' }
  }
  if (!command.plan.occurrences.length) return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'occurrence 不能为空' }
  const seen = new Set<string>()
  for (const occ of command.plan.occurrences) {
    if (!COMMAND_ID_PATTERN.test(occ.commandId) || occ.commandId.length === 0 || occ.commandId.length > 64) {
      return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: `commandId 非法 ${occ.commandId}` }
    }
    if (occ.occurrence < 0) {
      return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'occurrence 非法' }
    }
    if (occ.baseAtMs < 0 || occ.baseAtMs > command.plan.durationMs) {
      return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'baseAtMs 越界' }
    }
    if (!UUID_PATTERN.test(occ.actionRunId)) {
      return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'actionRunId 非法' }
    }
    const buf = Buffer.from(occ.command, 'utf8')
    if (buf.length === 0 || buf.length > 1024 || buf.toString('utf8') !== occ.command) {
      return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'command 文本越界或非 UTF-8' }
    }
    for (let i = 0; i < occ.command.length; i += 1) {
      const code = occ.command.charCodeAt(i)
      if (code <= 0x1f || code === 0x7f) {
        return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'command 含控制字符' }
      }
    }
    const key = commandOccurrenceKey(occ.commandId, occ.occurrence)
    if (seen.has(key)) return { errorCode: 'COMMAND_ARGUMENT_INVALID', error: 'occurrence 重复' }
    seen.add(key)
  }
  return true
}

async function executeOccurrence(options: {
  plannedAtUnixMs: number
  command: string
  deadlineUnixMs: number
  now: () => number
  getBot: () => McBotLike | null | undefined
  chat: (bot: McBotLike, command: string) => void
  sleep: (ms: number) => Promise<void>
}): Promise<AttemptOutcome> {
  const attempts: CommandAttemptError[] = []
  let attempt = 0
  while (attempt < MAX_OCCURRENCE_ATTEMPTS) {
    attempt += 1
    if (options.now() >= options.deadlineUnixMs) {
      attempts.push({ attempt, errorCode: 'COMMAND_DEADLINE_EXCEEDED', message: '运行 deadline 已到', observedAtUnixMs: options.now() })
      return {
        status: 'timed_out',
        attempt,
        durationMs: 0,
        observedAtUnixMs: options.now(),
        sentAtUnixMs: null,
        attemptErrors: attempts.slice(-2),
        errorCode: 'COMMAND_DEADLINE_EXCEEDED',
        message: '运行 deadline 已到',
      }
    }
    const bot = options.getBot()
    if (!bot) {
      attempts.push({ attempt, errorCode: 'COMMAND_RUNTIME_UNAVAILABLE', message: 'Bot 不存在', observedAtUnixMs: options.now() })
      return {
        status: 'failed',
        attempt,
        durationMs: 0,
        observedAtUnixMs: options.now(),
        sentAtUnixMs: null,
        attemptErrors: attempts.slice(-2),
        errorCode: 'COMMAND_RUNTIME_UNAVAILABLE',
        message: 'Bot 不存在',
      }
    }
    const startMs = options.now()
    try {
      options.chat(bot, options.command)
      const observed = options.now()
      return {
        status: 'sent',
        attempt,
        durationMs: observed - startMs,
        observedAtUnixMs: observed,
        sentAtUnixMs: observed,
        plannedAtUnixMs: options.plannedAtUnixMs,
        attemptErrors: attempts.slice(-2),
      }
    } catch (error) {
      const observed = options.now()
      const message = error instanceof Error ? error.message : String(error)
      attempts.push({ attempt, errorCode: 'COMMAND_SEND_FAILED', message, observedAtUnixMs: observed })
      if (attempt < MAX_OCCURRENCE_ATTEMPTS) {
        const backoff = RETRY_BACKOFFS_MS[attempt - 1] ?? 500
        await options.sleep(backoff)
        if (options.now() >= options.deadlineUnixMs) {
          return {
            status: 'timed_out',
            attempt,
            durationMs: 0,
            observedAtUnixMs: options.now(),
            sentAtUnixMs: null,
            plannedAtUnixMs: options.plannedAtUnixMs,
            attemptErrors: attempts.slice(-2),
            errorCode: 'COMMAND_DEADLINE_EXCEEDED',
            message: '运行 deadline 已到',
          }
        }
        continue
      }
      return {
        status: 'failed',
        attempt,
        durationMs: observed - startMs,
        observedAtUnixMs: observed,
        sentAtUnixMs: null,
        plannedAtUnixMs: options.plannedAtUnixMs,
        attemptErrors: attempts.slice(-2),
        errorCode: 'COMMAND_SEND_FAILED',
        message,
      }
    }
  }
  return {
    status: 'failed',
    attempt,
    durationMs: 0,
    observedAtUnixMs: options.now(),
    sentAtUnixMs: null,
    plannedAtUnixMs: options.plannedAtUnixMs,
    attemptErrors: attempts.slice(-2),
    errorCode: 'COMMAND_SEND_FAILED',
    message: '所有重试均失败',
  }
}

function defaultSleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    if (typeof timer.unref === 'function') timer.unref()
  })
}

export const __testing = {
  commandOccurrenceKey,
  scheduleTombstoneKey,
  MAX_OCCURRENCE_ATTEMPTS,
  RETRY_BACKOFFS_MS,
}