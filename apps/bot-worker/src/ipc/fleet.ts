import type { Behavior } from '../behavior/base.js'
import type {
  ActionEvent,
  ActionSignal,
  BotConfig,
  BotItemResult,
  BotStateSnapshot,
  CreateBotsCommand,
  FleetHeartbeatEvent,
  GetFleetSnapshotCommand,
  IpcCommand,
  SignalActionsCommand,
  SignalItemResult,
  StopBotsCommand,
  WorkerReadyEvent,
} from './types.js'

const MAX_BATCH_SIZE = 50
const DEFAULT_IDEMPOTENCY_TTL_MS = 60 * 60 * 1000
const DEFAULT_IDEMPOTENCY_CACHE_SIZE = 1000

interface McBotLike {
  username: string
  health: number
  food: number
  entity?: { position?: { x: number; y: number; z: number } }
  on(event: string, listener: (...args: never[]) => void): unknown
  quit(): void
  chat(message: string): void
}

interface BotInstance {
  config: BotConfig
  behavior: Behavior
  status: string
  mcBot: McBotLike | null
  connectTimer: unknown | null
  tickTimer: ReturnType<typeof setInterval> | null
}

interface CachedBatchResult {
  fingerprint: string
  expiresAt: number
  batchId?: string
  idempotencyKey?: string
  results: BotItemResult[]
}

interface StopDiagnostic {
  botId: string
  generation?: number
  reason?: string
}

/** Fleet 控制器依赖，Mineflayer 和时钟均可替换以避免单元测试真实联网。 */
export interface FleetControllerOptions {
  maxBots: number
  workerEpoch: string
  workerEpochGeneration: number
  sendEvent: (event: object) => void
  createBot: (options: Record<string, unknown>) => McBotLike
  createBehavior: (botId: string, behavior: string, config?: unknown) => Behavior
  now?: () => number
  setTimeout?: (callback: () => void, delayMs: number) => unknown
  clearTimeout?: (timer: unknown) => void
  idempotencyTtlMs?: number
  idempotencyCacheSize?: number
  signalRouter?: (signal: ActionSignal, config: BotConfig) => SignalItemResult
  onBotStopped?: (diagnostic: StopDiagnostic) => void
}

/** 管理 Fleet IPC 的批量准入、连接门控、快照与通用信号回执。 */
export class FleetController {
  private readonly bots = new Map<string, BotInstance>()
  private readonly idempotencyCache = new Map<string, CachedBatchResult>()
  private readonly maxBots: number
  private readonly workerEpoch: string
  private readonly workerEpochGeneration: number
  private readonly sendEvent: FleetControllerOptions['sendEvent']
  private readonly createBot: FleetControllerOptions['createBot']
  private readonly createBehavior: FleetControllerOptions['createBehavior']
  private readonly now: () => number
  private readonly schedule: NonNullable<FleetControllerOptions['setTimeout']>
  private readonly cancelSchedule: NonNullable<FleetControllerOptions['clearTimeout']>
  private readonly idempotencyTtlMs: number
  private readonly idempotencyCacheSize: number
  private readonly signalRouter?: FleetControllerOptions['signalRouter']
  private readonly onBotStopped?: FleetControllerOptions['onBotStopped']
  private eventSeq = 0
  private stopped = false

  constructor(options: FleetControllerOptions) {
    this.maxBots = options.maxBots
    this.workerEpoch = options.workerEpoch
    this.workerEpochGeneration = options.workerEpochGeneration
    this.sendEvent = options.sendEvent
    this.createBot = options.createBot
    this.createBehavior = options.createBehavior
    this.now = options.now ?? Date.now
    this.schedule = options.setTimeout ?? ((callback, delayMs) => setTimeout(callback, delayMs))
    this.cancelSchedule = options.clearTimeout ?? ((timer) => clearTimeout(timer as ReturnType<typeof setTimeout>))
    this.idempotencyTtlMs = options.idempotencyTtlMs ?? DEFAULT_IDEMPOTENCY_TTL_MS
    this.idempotencyCacheSize = options.idempotencyCacheSize ?? DEFAULT_IDEMPOTENCY_CACHE_SIZE
    this.signalRouter = options.signalRouter
    this.onBotStopped = options.onBotStopped
  }

  /** 处理 Fleet 命令；返回 false 表示应交给旧 IPC 分支。 */
  handleCommand(command: IpcCommand | { cmd: string }): boolean {
    switch (command.cmd) {
      case 'create-bots': {
        this.createBots(command as CreateBotsCommand)
        return true
      }
      case 'stop-bots': {
        this.stopBots(command as StopBotsCommand)
        return true
      }
      case 'signal-actions': {
        const signal = command as SignalActionsCommand
        this.signalActions(signal.requestId, signal.signals)
        return true
      }
      case 'get-fleet-snapshot': {
        const snapshot = command as GetFleetSnapshotCommand
        this.sendEvent({
          evt: 'fleet-snapshot-result',
          requestId: snapshot.requestId,
          bots: this.snapshots(),
        })
        return true
      }
      default:
        return false
    }
  }

  /** 返回 worker-ready 的冻结能力字段。 */
  workerReady(botWorkerVersion: string): WorkerReadyEvent {
    return {
      evt: 'worker-ready',
      workerEpoch: this.workerEpoch,
      workerEpochGeneration: this.workerEpochGeneration,
      botWorkerVersion,
      maxBots: this.maxBots,
      features: ['fleet-v1'],
      capacityGeneration: 1,
    }
  }

  /** 路由场景引擎产生的冻结动作事件，不在本 FR 解释动作语义。 */
  emitActionEvent(action: ActionEvent['action']): void {
    this.sendEvent({ evt: 'action-event', action })
  }

  /** 返回 heartbeat 的 Fleet 容量与进程指标。 */
  heartbeat(eventLoopP95Ms: number): FleetHeartbeatEvent {
    const metrics = this.metrics()
    return {
      evt: 'heartbeat',
      activeBots: metrics.activeBots,
      connectingBots: metrics.connectingBots,
      rssBytes: process.memoryUsage().rss,
      eventLoopP95Ms,
      droppedEvents: 0,
      capacityGeneration: 1,
    }
  }

  /** 返回当前利用率。 */
  metrics(): { activeBots: number; connectingBots: number; maxBots: number } {
    let connectingBots = 0
    for (const instance of this.bots.values()) {
      if (instance.status === 'connecting') connectingBots++
    }
    return { activeBots: this.bots.size, connectingBots, maxBots: this.maxBots }
  }

  /** 返回当前全部 Bot 的完整运行快照。 */
  snapshots(): BotStateSnapshot[] {
    return [...this.bots.entries()]
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([botId, instance]) => this.snapshot(botId, instance))
  }

  /** 切换旧单 Bot 行为。 */
  setBehavior(botId: string, behaviorType: string, target?: string, config?: unknown): boolean {
    const instance = this.bots.get(botId)
    if (!instance) return false
    instance.behavior.stop()
    const next = this.createBehavior(botId, behaviorType, config ?? target)
    if (instance.mcBot) next.setMcBot(instance.mcBot as never)
    next.start()
    instance.behavior = next
    return true
  }

  /** 向旧单 Bot 发送聊天命令。 */
  sendBotCommand(botId: string, command: string): 'sent' | 'missing' | 'disconnected' {
    const instance = this.bots.get(botId)
    if (!instance) return 'missing'
    if (!instance.mcBot) return 'disconnected'
    instance.mcBot.chat(command)
    return 'sent'
  }

  /** 获取脚本执行所需的 Mineflayer Bot。 */
  getBot(botId: string): McBotLike | null {
    return this.bots.get(botId)?.mcBot ?? null
  }

  /** 停止全部连接并取消尚未触发的连接定时器。 */
  shutdown(): void {
    this.stopped = true
    for (const [botId, instance] of [...this.bots]) {
      this.disposeInstance(botId, instance)
    }
    this.idempotencyCache.clear()
  }

  private createBots(command: CreateBotsCommand): void {
    if (command.idempotencyKey) {
      const replayed = this.replayBatch(command)
      if (replayed) return
    }

    const results = command.bots.length > MAX_BATCH_SIZE
      ? command.bots.map((config) => this.rejected(config.id, 'batch_limit_exceeded', '单批最多 50 个 Bot'))
      : command.bots.map((config) => this.admit(config))

    if (command.requestId) {
      this.sendBatchResult(command, results)
    } else if (results.some((item) => !item.accepted)) {
      this.sendEvent({ evt: 'bot-error', error: '部分 Bot 未通过准入，请检查容量或批量大小' })
    }
    if (command.idempotencyKey) this.rememberBatch(command, results)
  }

  private replayBatch(command: CreateBotsCommand): boolean {
    this.pruneIdempotencyCache()
    const cached = this.idempotencyCache.get(command.idempotencyKey ?? '')
    if (!cached) return false
    if (cached.fingerprint !== batchFingerprint(command)) {
      const results = command.bots.map((config) => this.rejected(
        config.id,
        'idempotency_conflict',
        '相同 idempotencyKey 对应不同载荷',
        'conflict'
      ))
      this.sendBatchResult(command, results)
      return true
    }
    this.sendEvent({
      evt: 'batch-result',
      requestId: command.requestId,
      batchId: cached.batchId,
      idempotencyKey: cached.idempotencyKey,
      results: cached.results.map((item) => ({ ...item })),
    })
    return true
  }

  private rememberBatch(command: CreateBotsCommand, results: BotItemResult[]): void {
    const key = command.idempotencyKey
    if (!key) return
    this.pruneIdempotencyCache()
    while (this.idempotencyCache.size >= this.idempotencyCacheSize) {
      const oldest = this.idempotencyCache.keys().next().value as string | undefined
      if (oldest === undefined) break
      this.idempotencyCache.delete(oldest)
    }
    this.idempotencyCache.set(key, {
      fingerprint: batchFingerprint(command),
      expiresAt: this.now() + this.idempotencyTtlMs,
      batchId: command.batchId,
      idempotencyKey: key,
      results: results.map((item) => ({ ...item })),
    })
  }

  private pruneIdempotencyCache(): void {
    const now = this.now()
    for (const [key, entry] of this.idempotencyCache) {
      if (entry.expiresAt <= now) this.idempotencyCache.delete(key)
    }
  }

  private admit(config: BotConfig): BotItemResult {
    const existing = this.bots.get(config.id)
    if (existing) {
      const decision = this.generationDecision(existing.config, config)
      if (decision) return decision
    }
    if (!existing && this.bots.size >= this.maxBots) {
      return this.rejected(config.id, 'capacity_insufficient', 'Bot Worker 容量不足', 'capacity_insufficient')
    }
    if (existing) this.disposeInstance(config.id, existing)
    try {
      this.installInstance(config)
      return { botId: config.id, accepted: true, skipped: false, status: 'accepted' }
    } catch (error) {
      return this.rejected(config.id, 'ephemeral_unavailable', String(error), 'ephemeral_unavailable')
    }
  }

  private generationDecision(current: BotConfig, incoming: BotConfig): BotItemResult | null {
    if (current.generation === undefined || incoming.generation === undefined) return null
    if (incoming.generation < current.generation) {
      return this.rejected(incoming.id, 'stale_generation', 'Bot generation 已过期', 'stale')
    }
    if (incoming.generation > current.generation) return null
    if (incoming.configHash === current.configHash) {
      return { botId: incoming.id, accepted: true, skipped: true, status: 'accepted' }
    }
    return this.rejected(
      incoming.id,
      'config_hash_conflict',
      '相同 generation 的 configHash 不一致',
      'conflict'
    )
  }

  private installInstance(config: BotConfig): void {
    const behavior = this.createBehavior(config.id, config.behavior || 'idle', config.behaviorConfig)
    behavior.start()
    const instance: BotInstance = {
      config,
      behavior,
      status: 'connecting',
      mcBot: null,
      connectTimer: null,
      tickTimer: null,
    }
    this.bots.set(config.id, instance)
    this.emitBotState(config.id, instance)

    const connectAt = config.connectNotBeforeUnixMs ?? config.connectNotBefore ?? this.now()
    this.scheduleConnection(config.id, instance, connectAt)
  }

  private scheduleConnection(botId: string, instance: BotInstance, connectAt: number): void {
    const remaining = connectAt - this.now()
    if (remaining <= 0) {
      this.connectBot(botId, instance)
      return
    }
    const delay = Math.min(remaining, 2_147_483_647)
    instance.connectTimer = this.schedule(() => {
      instance.connectTimer = null
      if (this.stopped || this.bots.get(botId) !== instance) return
      this.scheduleConnection(botId, instance, connectAt)
    }, delay)
  }

  private connectBot(botId: string, instance: BotInstance): void {
    try {
      const config = instance.config
      const mcBot = this.createBot({
        host: config.host,
        port: config.port || 25565,
        username: config.username || `Bot_${botId.slice(0, 6)}`,
        version: config.version,
        auth: config.auth,
        hideErrors: true,
      })
      if (this.bots.get(botId) !== instance) {
        mcBot.quit()
        return
      }
      instance.mcBot = mcBot
      this.bindBotEvents(botId, instance, mcBot)
      this.startBehaviorTicks(botId, instance, mcBot)
    } catch (error) {
      instance.status = 'error'
      this.emitBotState(botId, instance, { lastError: String(error), errorCode: 'connect_failed' })
      this.sendEvent({ evt: 'bot-error', botId, error: String(error) })
    }
  }

  private bindBotEvents(botId: string, instance: BotInstance, mcBot: McBotLike): void {
    mcBot.on('spawn', (() => {
      if (this.bots.get(botId) !== instance) return
      instance.status = 'connected'
      this.emitBotState(botId, instance)
      this.sendEvent({ evt: 'bot-event', botId, type: 'spawn', data: {} })
    }) as (...args: never[]) => void)
    mcBot.on('chat', ((username: string, message: string) => {
      if (username === mcBot.username || this.bots.get(botId) !== instance) return
      this.sendEvent({ evt: 'bot-event', botId, type: 'chat', data: { username, message } })
    }) as (...args: never[]) => void)
    mcBot.on('kicked', ((reason: string) => {
      if (this.bots.get(botId) !== instance) return
      instance.status = 'disconnected'
      this.emitBotState(botId, instance)
      this.sendEvent({ evt: 'bot-event', botId, type: 'kicked', data: { reason } })
    }) as (...args: never[]) => void)
    mcBot.on('error', ((error: Error) => {
      if (this.bots.get(botId) !== instance) return
      instance.status = 'error'
      this.emitBotState(botId, instance, { lastError: error.message, errorCode: 'connection_error' })
      this.sendEvent({ evt: 'bot-error', botId, error: error.message })
    }) as (...args: never[]) => void)
    mcBot.on('end', (() => {
      if (this.bots.get(botId) !== instance) return
      instance.status = 'disconnected'
      this.emitBotState(botId, instance)
    }) as (...args: never[]) => void)
  }

  private startBehaviorTicks(botId: string, instance: BotInstance, mcBot: McBotLike): void {
    instance.tickTimer = setInterval(() => {
      if (this.bots.get(botId) !== instance) {
        if (instance.tickTimer) clearInterval(instance.tickTimer)
        instance.tickTimer = null
        return
      }
      if (instance.status !== 'connected') return
      instance.behavior.setMcBot(mcBot as never)
      instance.behavior.tick().catch((error: Error) => {
        this.sendEvent({ evt: 'bot-error', botId, error: error.message })
      })
    }, 250)
    instance.tickTimer.unref?.()
  }

  private stopBots(command: StopBotsCommand): void {
    const results = command.botIds.map((botId) => this.stopBot(botId, command))
    if (command.requestId) {
      this.sendEvent({ evt: 'batch-result', requestId: command.requestId, results })
    }
  }

  private stopBot(botId: string, command: StopBotsCommand): BotItemResult {
    const instance = this.bots.get(botId)
    if (!instance) {
      this.emitMissingState(botId)
      return this.rejected(botId, 'ephemeral_unavailable', 'Bot 不存在', 'ephemeral_unavailable')
    }
    if (this.isStaleStop(instance.config, command.generation)) {
      return this.rejected(botId, 'stale_generation', '停止 generation 已过期', 'stale')
    }
    this.stopInstanceResources(instance)
    instance.status = 'stopped'
    this.emitBotState(botId, instance)
    this.bots.delete(botId)
    this.onBotStopped?.({
      botId,
      generation: command.generation,
      reason: sanitizeStopReason(command.reason),
    })
    return { botId, accepted: true, skipped: false, status: 'accepted' }
  }

  private isStaleStop(config: BotConfig, generation?: number): boolean {
    return generation !== undefined
      && config.generation !== undefined
      && config.generation > generation
  }

  private signalActions(requestId: string, signals: ActionSignal[]): void {
    const signalResults = signals.map((signal) => {
      const instance = this.bots.get(signal.botId)
      if (!instance) return this.rejectedSignal(signal.signalId, 'ephemeral_unavailable', 'Bot 不存在')
      if (signal.generation !== 0 && instance.config.generation !== undefined && signal.generation !== instance.config.generation) {
        return this.rejectedSignal(signal.signalId, 'generation_conflict', 'Bot generation 不匹配', 'conflict')
      }
      return this.signalRouter?.(signal, instance.config) ?? {
        signalId: signal.signalId,
        accepted: true,
        skipped: false,
        status: 'accepted',
      }
    })
    this.sendEvent({ evt: 'signal-result', requestId, signalResults })
  }

  private disposeInstance(botId: string, instance: BotInstance): void {
    this.stopInstanceResources(instance)
    this.bots.delete(botId)
  }

  private stopInstanceResources(instance: BotInstance): void {
    if (instance.connectTimer !== null) {
      this.cancelSchedule(instance.connectTimer)
      instance.connectTimer = null
    }
    if (instance.tickTimer) {
      clearInterval(instance.tickTimer)
      instance.tickTimer = null
    }
    instance.behavior.stop()
    if (!instance.mcBot) return
    try {
      instance.mcBot.quit()
    } catch {
      // 连接已经关闭时 quit 可能抛错，清理仍需继续。
    }
    instance.mcBot = null
  }

  private snapshot(botId: string, instance: BotInstance): BotStateSnapshot {
    const snapshot: BotStateSnapshot = {
      id: botId,
      status: instance.status,
      name: instance.config.name,
      behavior: instance.behavior.name,
      sessionId: instance.config.sessionId,
      generation: instance.config.generation,
      configHash: instance.config.configHash,
      workerEpoch: this.workerEpoch,
      workerEpochGeneration: this.workerEpochGeneration,
      eventSeq: ++this.eventSeq,
      currentStepId: instance.config.resumeStepId,
      reconnectCount: 0,
      observedAt: this.now(),
    }
    if (instance.mcBot && instance.status === 'connected') {
      snapshot.health = instance.mcBot.health
      snapshot.food = instance.mcBot.food
      const position = instance.mcBot.entity?.position
      if (position) snapshot.position = { x: position.x, y: position.y, z: position.z }
    }
    return snapshot
  }

  private emitBotState(
    botId: string,
    instance: BotInstance,
    overrides: Partial<BotStateSnapshot> = {}
  ): void {
    this.sendEvent({
      evt: 'bot-state',
      bots: [{ ...this.snapshot(botId, instance), ...overrides }],
    })
  }

  private emitMissingState(botId: string): void {
    this.sendEvent({
      evt: 'bot-state',
      bots: [{
        id: botId,
        status: 'not_found',
        workerEpoch: this.workerEpoch,
        workerEpochGeneration: this.workerEpochGeneration,
        eventSeq: ++this.eventSeq,
        reconnectCount: 0,
        observedAt: this.now(),
      }],
    })
  }

  private sendBatchResult(command: CreateBotsCommand, results: BotItemResult[]): void {
    this.sendEvent({
      evt: 'batch-result',
      requestId: command.requestId,
      batchId: command.batchId,
      idempotencyKey: command.idempotencyKey,
      results,
    })
  }

  private rejected(
    botId: string,
    errorCode: string,
    error: string,
    status = errorCode
  ): BotItemResult {
    return { botId, accepted: false, skipped: true, status, errorCode, error }
  }

  private rejectedSignal(
    signalId: string,
    errorCode: string,
    error: string,
    status = errorCode
  ): SignalItemResult {
    return { signalId, accepted: false, skipped: true, status, errorCode, error }
  }
}

function sanitizeStopReason(reason?: string): string | undefined {
  if (!reason) return reason
  return reason
    .replace(/\b(token|password|secret|authorization)\s*=\s*\S+/gi, '$1=[已脱敏]')
    .replace(/\bBearer\s+\S+/gi, 'Bearer [已脱敏]')
}

function batchFingerprint(command: CreateBotsCommand): string {
  return stableStringify({ batchId: command.batchId, bots: command.bots })
}

function stableStringify(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(',')}]`
  if (value && typeof value === 'object') {
    const record = value as Record<string, unknown>
    return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${stableStringify(record[key])}`).join(',')}}`
  }
  return JSON.stringify(value)
}
