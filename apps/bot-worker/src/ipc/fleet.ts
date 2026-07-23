import type { Behavior } from '../behavior/base.js'
import { createScenarioAction } from '../scenario/actions.js'
import { ScenarioRunner } from '../scenario/runner.js'
import type { ScenarioAction, ScenarioBotCapabilities, ScenarioStep } from '../scenario/types.js'
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
const MAX_SIGNAL_BATCH_SIZE = 100
const DEFAULT_IDEMPOTENCY_TTL_MS = 60 * 60 * 1000
const DEFAULT_IDEMPOTENCY_CACHE_SIZE = 1000
/** 自动重连：base=1s、max=60s、jitter=20%；连续失败 ≥10 次后固定 max delay（FR-365）。 */
const RECONNECT_BASE_MS = 1_000
const RECONNECT_MAX_MS = 60_000
const RECONNECT_JITTER_RATIO = 0.2
const RECONNECT_DEGRADED_ATTEMPTS = 10

interface McEntityLike {
  id: string | number
  type?: string
  kind?: string
  name?: string
  mobType?: string
  username?: string
  health?: number
  isValid?: boolean
  position?: { x: number; y: number; z: number }
}

interface McBotLike {
  username: string
  health: number
  food: number
  entity?: McEntityLike
  entities?: Record<string, McEntityLike>
  pathfinder?: { setGoal(goal: unknown): void }
  loadPlugin?(plugin: unknown): void
  attack?(entity: McEntityLike): void
  respawn?(): void
  on(event: string, listener: (...args: never[]) => void): unknown
  off?(event: string, listener: (...args: never[]) => void): unknown
  removeListener?(event: string, listener: (...args: never[]) => void): unknown
  quit(): void
  chat(message: string): void
}

interface ScenarioCapabilityState {
  spawned: boolean
  dead: boolean
  spawnEventSeq: number
  endReason?: string
  mcBot: McBotLike | null
  pathfinderEvents: { goalReached: number; pathFailed: number }
  pathfinderInit: Promise<boolean> | null
  pathfinderGoals: typeof import('mineflayer-pathfinder').goals | null
  capabilities: ScenarioBotCapabilities
}

interface BotInstance {
  config: BotConfig
  fleetManaged: boolean
  /** desired running 时断线自动重连；stop 后为 false，禁止复活。 */
  desiredRunning: boolean
  behavior: Behavior | null
  scenarioRunner: ScenarioRunner | null
  scenarioState: ScenarioCapabilityState | null
  status: string
  mcBot: McBotLike | null
  connectTimer: unknown | null
  reconnectTimer: unknown | null
  reconnectCount: number
  consecutiveFailures: number
  /** 持有进程级连接信号量时为 true，释放时必须归还。 */
  holdingConnectSlot: boolean
  eventCleanup: (() => void) | null
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
  setInterval?: (callback: () => void, intervalMs: number) => unknown
  clearInterval?: (timer: unknown) => void
  idempotencyTtlMs?: number
  idempotencyCacheSize?: number
  /** 进程级同时 connecting 上限；默认 min(10, maxBots/5)，至少 1。 */
  maxConcurrentConnecting?: number
  /** 可注入抖动 [0,1)，测试可固定为 0。 */
  random?: () => number
  signalRouter?: (signal: ActionSignal, config: BotConfig) => SignalItemResult
  onBotStopped?: (diagnostic: StopDiagnostic) => void
}

/** 管理 Fleet IPC 的批量准入、连接门控、自动重连、快照与通用信号回执。 */
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
  private readonly scheduleTick: NonNullable<FleetControllerOptions['setInterval']>
  private readonly cancelTick: NonNullable<FleetControllerOptions['clearInterval']>
  private readonly idempotencyTtlMs: number
  private readonly idempotencyCacheSize: number
  private readonly maxConcurrentConnecting: number
  private readonly random: () => number
  private readonly signalRouter?: FleetControllerOptions['signalRouter']
  private readonly onBotStopped?: FleetControllerOptions['onBotStopped']
  private eventSeq = 0
  private tickTimer: unknown | null = null
  private tickInFlight = false
  private stopped = false
  /** 当前占用的 connecting 信号量。 */
  private connectingSlots = 0
  /** 等待信号量的重连队列（FIFO）。 */
  private readonly connectWaiters: string[] = []

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
    this.scheduleTick = options.setInterval ?? ((callback, intervalMs) => setInterval(callback, intervalMs))
    this.cancelTick = options.clearInterval ?? ((timer) => clearInterval(timer as ReturnType<typeof setInterval>))
    this.idempotencyTtlMs = options.idempotencyTtlMs ?? DEFAULT_IDEMPOTENCY_TTL_MS
    this.idempotencyCacheSize = options.idempotencyCacheSize ?? DEFAULT_IDEMPOTENCY_CACHE_SIZE
    this.maxConcurrentConnecting = options.maxConcurrentConnecting
      ?? Math.max(1, Math.min(10, Math.floor(options.maxBots / 5) || 1))
    this.random = options.random ?? Math.random
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
      case 'run-script':
        return this.rejectScenarioScript(command as { botIds?: string[] })
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

  /** 路由场景引擎产生的冻结动作事件，并沿用 Fleet 统一事件序列。 */
  emitActionEvent(action: ActionEvent['action']): void {
    this.eventSeq++
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

  /** 由单一集中 scheduler 驱动全部旧行为与 ScenarioRunner。 */
  async tick(now = this.now()): Promise<void> {
    if (this.tickInFlight || this.stopped) return
    this.tickInFlight = true
    try {
      for (const [botId, instance] of this.bots) {
        await this.tickInstance(botId, instance, now)
      }
    } finally {
      this.tickInFlight = false
    }
  }

  /** 返回当前全部 Bot 的完整运行快照。 */
  snapshots(): BotStateSnapshot[] {
    return [...this.bots.entries()]
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([botId, instance]) => this.snapshot(botId, instance))
  }

  /** 切换旧单 Bot 行为。 */
  setBehavior(botId: string, behaviorType: string, target?: string, config?: unknown): 'changed' | 'missing' | 'fleet_managed' {
    const instance = this.bots.get(botId)
    if (!instance) return 'missing'
    if (instance.fleetManaged && hasScenario(instance.config.scenario)) return 'fleet_managed'
    instance.behavior?.stop()
    const next = this.createBehavior(botId, behaviorType, config ?? target)
    if (instance.mcBot) next.setMcBot(instance.mcBot as never)
    next.start()
    instance.behavior = next
    return 'changed'
  }

  private rejectScenarioScript(command: { botIds?: string[] }): boolean {
    const blocked = command.botIds?.find((botId) => {
      const instance = this.bots.get(botId)
      return instance?.fleetManaged === true && hasScenario(instance.config.scenario)
    })
    if (!blocked) return false
    this.sendEvent({
      evt: 'bot-error', botId: blocked, errorCode: 'fleet_managed',
      error: 'Fleet 场景 Bot 不接受旧 run-script，请使用 Scenario 动作入口',
    })
    return true
  }

  /** 仅通过 mcBot.chat 发送聊天，不驱动或修改 ScenarioRunner。 */
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

/** 通过 Bot UUID 查找 Mineflayer 实例（FR-369 命令编排使用）。 */
  getBotByUuid(botUuid: string): McBotLike | null {
    for (const instance of this.bots.values()) {
      if (instance.config.id === botUuid || instance.config.id.toLowerCase() === botUuid.toLowerCase()) {
        return instance.mcBot ?? null
      }
    }
    return null
  }

  /** 停止全部连接并取消尚未触发的连接/重连定时器。 */

  shutdown(): void {
    this.stopped = true
    this.connectWaiters.length = 0
    for (const [botId, instance] of [...this.bots]) {
      this.disposeInstance(botId, instance, 'Bot Worker 关闭')
    }
    this.connectingSlots = 0
    this.stopTickLoop()
    this.idempotencyCache.clear()
  }

  /** 测试/诊断：当前 connecting 信号量占用与上限。 */
  connectSemaphore(): { used: number; max: number; waiting: number } {
    return {
      used: this.connectingSlots,
      max: this.maxConcurrentConnecting,
      waiting: this.connectWaiters.length,
    }
  }

  private createBots(command: CreateBotsCommand): void {
    if (command.idempotencyKey) {
      const replayed = this.replayBatch(command)
      if (replayed) return
    }

    const fleetManaged = isFleetCreateCommand(command)
    const results = command.bots.length > MAX_BATCH_SIZE
      ? command.bots.map((config) => this.rejected(config.id, 'batch_limit_exceeded', '单批最多 50 个 Bot'))
      : command.bots.map((config) => this.admit(config, fleetManaged))

    if (command.requestId) {
      this.sendBatchResult(command, results)
    } else if (results.some((item) => !item.accepted)) {
      const conflict = results.find((item) => item.errorCode === 'fleet_managed')
      this.sendEvent({ evt: 'bot-error', error: conflict?.error ?? '部分 Bot 未通过准入，请检查容量或批量大小' })
    }
    if (command.idempotencyKey && results.every(isStableBatchResult)) {
      this.rememberBatch(command, results)
    }
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

  private admit(config: BotConfig, fleetManaged: boolean): BotItemResult {
    const existing = this.bots.get(config.id)
    if (existing) {
      if (!fleetManaged && existing.fleetManaged) {
        return this.rejected(config.id, 'fleet_managed', 'Bot 由 Fleet 管理，请使用 Fleet RPC 操作', 'conflict')
      }
      const decision = this.generationDecision(existing.config, config)
      if (decision) return decision
    }
    if (!existing && this.bots.size >= this.maxBots) {
      return this.rejected(config.id, 'capacity_insufficient', 'Bot Worker 容量不足', 'capacity_insufficient')
    }
    if (existing) this.disposeInstance(config.id, existing, 'Bot assignment 已替换')
    try {
      this.installInstance(config, fleetManaged)
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

  private installInstance(config: BotConfig, fleetManaged: boolean): void {
    const scenarioState = hasScenario(config.scenario) ? createScenarioCapabilityState(this.now) : null
    const behavior = scenarioState ? null : this.createBehavior(
      config.id,
      config.behavior || 'idle',
      config.behaviorConfig
    )
    behavior?.start()
    const instance: BotInstance = {
      config, fleetManaged, desiredRunning: true, behavior, scenarioRunner: null, scenarioState,
      status: 'connecting', mcBot: null, connectTimer: null, reconnectTimer: null,
      reconnectCount: 0, consecutiveFailures: 0, holdingConnectSlot: false, eventCleanup: null,
    }
    if (scenarioState) instance.scenarioRunner = this.createScenarioRunner(config, scenarioState, instance)
    this.bots.set(config.id, instance)
    this.ensureTickLoop()
    this.emitBotState(config.id, instance)
    if (instance.scenarioRunner) {
      void instance.scenarioRunner.start().catch((error) => this.sendEvent({ evt: 'bot-error', botId: config.id, error: String(error) }))
    }
    const connectAt = config.connectNotBeforeUnixMs ?? config.connectNotBefore ?? this.now()
    this.scheduleConnection(config.id, instance, connectAt)
  }

  private scheduleConnection(botId: string, instance: BotInstance, connectAt: number): void {
    const remaining = connectAt - this.now()
    if (remaining <= 0) {
      this.tryAcquireConnectAndSpawn(botId, instance)
      return
    }
    const delay = Math.min(remaining, 2_147_483_647)
    instance.connectTimer = this.schedule(() => {
      instance.connectTimer = null
      if (this.stopped || this.bots.get(botId) !== instance || !instance.desiredRunning) return
      this.scheduleConnection(botId, instance, connectAt)
    }, delay)
  }

  /** 进程级 semaphore：限制同时 connecting，默认 min(10, maxBots/5)。 */
  private tryAcquireConnectAndSpawn(botId: string, instance: BotInstance): void {
    if (this.stopped || this.bots.get(botId) !== instance || !instance.desiredRunning) return
    if (instance.mcBot || instance.status === 'connected') return
    if (instance.holdingConnectSlot) {
      this.connectBot(botId, instance)
      return
    }
    if (this.connectingSlots >= this.maxConcurrentConnecting) {
      if (!this.connectWaiters.includes(botId)) this.connectWaiters.push(botId)
      return
    }
    this.connectingSlots++
    instance.holdingConnectSlot = true
    instance.status = 'connecting'
    this.emitBotState(botId, instance)
    this.connectBot(botId, instance)
  }

  private releaseConnectSlot(instance: BotInstance): void {
    if (!instance.holdingConnectSlot) return
    instance.holdingConnectSlot = false
    this.connectingSlots = Math.max(0, this.connectingSlots - 1)
    this.drainConnectWaiters()
  }

  private drainConnectWaiters(): void {
    while (this.connectingSlots < this.maxConcurrentConnecting && this.connectWaiters.length > 0) {
      const botId = this.connectWaiters.shift()
      if (!botId) break
      const instance = this.bots.get(botId)
      if (!instance || !instance.desiredRunning || instance.mcBot || instance.status === 'connected') continue
      this.tryAcquireConnectAndSpawn(botId, instance)
    }
  }

  private removeConnectWaiter(botId: string): void {
    const index = this.connectWaiters.indexOf(botId)
    if (index >= 0) this.connectWaiters.splice(index, 1)
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
      if (this.bots.get(botId) !== instance || !instance.desiredRunning) {
        try { mcBot.quit() } catch { /* 已取消 */ }
        this.releaseConnectSlot(instance)
        return
      }
      instance.mcBot = mcBot
      // createBot 返回即表示登录已发起：释放信号量允许下一批，避免卡在 await spawn。
      this.releaseConnectSlot(instance)
      if (instance.scenarioState) {
        instance.scenarioState.mcBot = mcBot
        instance.scenarioState.pathfinderInit = null
        instance.scenarioState.pathfinderGoals = null
      }
      instance.behavior?.setMcBot(mcBot as never)
      this.bindBotEvents(botId, instance, mcBot)
    } catch (error) {
      this.releaseConnectSlot(instance)
      instance.status = 'error'
      this.emitBotState(botId, instance, { lastError: String(error), errorCode: 'connect_failed' })
      this.sendEvent({ evt: 'bot-error', botId, error: String(error) })
      this.scheduleReconnect(botId, instance, 'connect_failed')
    }
  }

  private bindBotEvents(botId: string, instance: BotInstance, mcBot: McBotLike): void {
    const listeners: Array<{ event: string; listener: (...args: never[]) => void }> = []
    const bind = (event: string, listener: (...args: never[]) => void): void => {
      mcBot.on(event, listener)
      listeners.push({ event, listener })
    }
    instance.eventCleanup = () => {
      for (const item of listeners) {
        if (mcBot.off) mcBot.off(item.event, item.listener)
        else mcBot.removeListener?.(item.event, item.listener)
      }
      listeners.length = 0
      instance.eventCleanup = null
    }
    bind('spawn', (() => {
      if (this.bots.get(botId) !== instance) return
      instance.status = 'connected'
      // 成功 spawn 清连续失败计数，累计 reconnectCount 保留。
      instance.consecutiveFailures = 0
      if (instance.scenarioState) {
        instance.scenarioState.spawned = true
        instance.scenarioState.dead = false
        instance.scenarioState.spawnEventSeq++
        instance.scenarioState.endReason = undefined
      }
      this.emitBotState(botId, instance)
      this.sendEvent({ evt: 'bot-event', botId, type: 'spawn', data: {} })
      if (instance.scenarioRunner) void instance.scenarioRunner.tick(this.now())
    }) as (...args: never[]) => void)
    bind('death', (() => {
      if (this.bots.get(botId) !== instance || !instance.scenarioState) return
      instance.scenarioState.dead = true
      instance.scenarioState.spawned = false
    }) as (...args: never[]) => void)
    bind('goal_reached', (() => {
      if (this.bots.get(botId) !== instance || !instance.scenarioState) return
      instance.scenarioState.pathfinderEvents.goalReached++
      if (instance.scenarioRunner) void instance.scenarioRunner.tick(this.now())
    }) as (...args: never[]) => void)
    bind('path_update', ((result: { status?: string }) => {
      if (this.bots.get(botId) !== instance || !instance.scenarioState) return
      if (result?.status !== 'noPath' && result?.status !== 'timeout') return
      instance.scenarioState.pathfinderEvents.pathFailed++
      if (instance.scenarioRunner) void instance.scenarioRunner.tick(this.now())
    }) as (...args: never[]) => void)
    bind('chat', ((username: string, message: string) => {
      if (username === mcBot.username || this.bots.get(botId) !== instance) return
      this.sendEvent({ evt: 'bot-event', botId, type: 'chat', data: { username, message } })
    }) as (...args: never[]) => void)
    bind('kicked', ((reason: string) => {
      if (this.bots.get(botId) !== instance) return
      instance.status = 'disconnected'
      if (instance.scenarioState) instance.scenarioState.endReason = `kicked: ${reason}`
      this.emitBotState(botId, instance)
      this.sendEvent({ evt: 'bot-event', botId, type: 'kicked', data: { reason } })
      this.releaseDisconnectedResources(botId, instance, mcBot, 'kicked')
    }) as (...args: never[]) => void)
    bind('error', ((error: Error) => {
      if (this.bots.get(botId) !== instance) return
      instance.status = 'error'
      this.emitBotState(botId, instance, { lastError: error.message, errorCode: 'connection_error' })
      this.sendEvent({ evt: 'bot-error', botId, error: error.message })
      // error 后通常会 end；若仅 error 无 end，也触发重连路径。
      if (instance.mcBot === mcBot) {
        this.releaseDisconnectedResources(botId, instance, mcBot, 'error')
      }
    }) as (...args: never[]) => void)
    bind('end', (() => {
      if (this.bots.get(botId) !== instance) return
      instance.status = 'disconnected'
      if (instance.scenarioState) instance.scenarioState.endReason = 'end'
      this.emitBotState(botId, instance)
      this.releaseDisconnectedResources(botId, instance, mcBot, 'end')
    }) as (...args: never[]) => void)
  }

  private releaseDisconnectedResources(
    botId: string,
    instance: BotInstance,
    mcBot: McBotLike,
    reason: string
  ): void {
    if (this.bots.get(botId) !== instance || instance.mcBot !== mcBot) return
    instance.eventCleanup?.()
    instance.eventCleanup = null
    instance.scenarioState?.capabilities.clearPathfinderGoal()
    const runner = instance.scenarioRunner
    instance.scenarioRunner = null
    instance.mcBot = null
    this.releaseConnectSlot(instance)
    if (instance.scenarioState) {
      instance.scenarioState.mcBot = null
      instance.scenarioState.pathfinderInit = null
      instance.scenarioState.pathfinderGoals = null
    }
    if (runner) {
      void runner.connectionEnded(reason, this.now())
        .then(() => runner.dispose())
        .catch((error) => this.sendEvent({ evt: 'bot-error', botId, error: String(error) }))
    } else {
      this.releaseBehavior(instance)
    }
    // 非人工 stop 且 desired running：指数退避自动重连。
    this.scheduleReconnect(botId, instance, reason)
  }

  /**
   * ReconnectController：kicked/end/error 且 desiredRunning 时调度重连。
   * delay = min(base*2^attempt, max) + jitter；≥10 次后固定 max delay。
   */
  private scheduleReconnect(botId: string, instance: BotInstance, reason: string): void {
    if (this.stopped || !instance.desiredRunning || this.bots.get(botId) !== instance) return
    if (instance.reconnectTimer !== null || instance.connectTimer !== null) return
    if (instance.mcBot) return
    instance.consecutiveFailures++
    instance.reconnectCount++
    const delay = this.reconnectDelayMs(instance.consecutiveFailures)
    instance.status = 'disconnected'
    this.emitBotState(botId, instance, {
      lastError: `重连排队: ${reason}`,
      errorCode: 'reconnecting',
    })
    instance.reconnectTimer = this.schedule(() => {
      instance.reconnectTimer = null
      if (this.stopped || this.bots.get(botId) !== instance || !instance.desiredRunning) return
      this.resumeAfterReconnect(botId, instance)
    }, delay)
  }

  private reconnectDelayMs(consecutiveFailures: number): number {
    const attempt = Math.max(0, consecutiveFailures - 1)
    const base = consecutiveFailures >= RECONNECT_DEGRADED_ATTEMPTS
      ? RECONNECT_MAX_MS
      : Math.min(RECONNECT_BASE_MS * (2 ** attempt), RECONNECT_MAX_MS)
    const jitter = base * RECONNECT_JITTER_RATIO * this.random()
    return Math.floor(base + jitter)
  }

  /** 重连前按 resumeStepId 重建 ScenarioRunner，再走连接 semaphore。 */
  private resumeAfterReconnect(botId: string, instance: BotInstance): void {
    if (hasScenario(instance.config.scenario) && !instance.scenarioRunner) {
      const state = instance.scenarioState ?? createScenarioCapabilityState(this.now)
      instance.scenarioState = state
      // CP 下发的 resumeStepId 优先；否则沿用 config 内既有值。
      instance.scenarioRunner = this.createScenarioRunner(instance.config, state, instance)
      void instance.scenarioRunner.start().catch((error) => {
        this.sendEvent({ evt: 'bot-error', botId, error: String(error) })
      })
    }
    instance.status = 'connecting'
    this.emitBotState(botId, instance)
    this.tryAcquireConnectAndSpawn(botId, instance)
  }

  private cancelReconnect(instance: BotInstance, botId: string): void {
    if (instance.reconnectTimer !== null) {
      this.cancelSchedule(instance.reconnectTimer)
      instance.reconnectTimer = null
    }
    this.removeConnectWaiter(botId)
    this.releaseConnectSlot(instance)
  }

  private async tickInstance(botId: string, instance: BotInstance, now: number): Promise<void> {
    if (this.bots.get(botId) !== instance) return
    if (instance.scenarioRunner) {
      await instance.scenarioRunner.tick(now)
      return
    }
    if (instance.status !== 'connected') return
    try {
      await instance.behavior?.tick()
    } catch (error) {
      this.sendEvent({ evt: 'bot-error', botId, error: String(error) })
    }
  }

  private createScenarioRunner(
    config: BotConfig,
    state: ScenarioCapabilityState,
    instance: BotInstance
  ): ScenarioRunner {
    return new ScenarioRunner({
      botId: config.id, botName: config.name || config.id, username: config.username || config.name || config.id,
      runId: config.sessionId ?? '', generation: config.generation ?? 0, cohortKey: config.cohortKey ?? '',
      scenario: config.scenario, resumeStepId: config.resumeStepId,
      correlationSeed: config.correlationSeed, capabilities: state.capabilities,
      actionFactory: (step) => step.type === 'legacy_behavior'
        ? createLegacyBehaviorAction(
          step,
          () => this.activateLegacyBehavior(config.id, step, instance),
          (behavior) => { if (instance.behavior === behavior) instance.behavior = null },
        )
        : createScenarioAction(step),
      emitActionEvent: (action) => this.emitActionEvent(action),
    })
  }

  private activateLegacyBehavior(botId: string, step: ScenarioStep, instance: BotInstance): Behavior {
    const spec = legacyBehaviorSpec(step)
    const behavior = this.createBehavior(botId, spec.behavior, spec.config ?? spec.target)
    if (instance.mcBot) behavior.setMcBot(instance.mcBot as never)
    instance.behavior = behavior
    return behavior
  }


  private ensureTickLoop(): void {
    if (this.tickTimer !== null || this.stopped) return
    this.tickTimer = this.scheduleTick(() => { void this.tick(this.now()) }, 250)
    ;(this.tickTimer as { unref?: () => void }).unref?.()
  }

  private stopTickLoop(): void {
    if (this.tickTimer === null) return
    this.cancelTick(this.tickTimer)
    this.tickTimer = null
  }

  private stopBots(command: StopBotsCommand): void {
    const results = command.botIds.map((botId) => this.stopBot(botId, command))
    if (command.requestId) {
      this.sendEvent({ evt: 'batch-result', requestId: command.requestId, results })
    } else {
      const conflict = results.find((item) => item.errorCode === 'fleet_managed')
      if (conflict) this.sendEvent({ evt: 'bot-error', botId: conflict.botId, error: conflict.error })
    }
  }

  private stopBot(botId: string, command: StopBotsCommand): BotItemResult {
    const instance = this.bots.get(botId)
    if (!instance) {
      this.emitMissingState(botId)
      return { botId, accepted: true, skipped: true, status: 'accepted', errorCode: 'already_stopped' }
    }
    const incompleteFleetEnvelope = !command.requestId || command.generation === undefined
    if (incompleteFleetEnvelope && instance.fleetManaged) {
      return this.rejected(botId, 'fleet_managed', 'Bot 由 Fleet 管理，请使用 Fleet RPC 操作', 'conflict')
    }
    if (this.isStaleStop(instance.config, command.generation)) {
      return this.rejected(botId, 'stale_generation', '停止 generation 已过期', 'stale')
    }
    // 人工 stop：先清 desired，取消 pending reconnect，禁止复活。
    instance.desiredRunning = false
    this.cancelReconnect(instance, botId)
    this.stopInstanceResources(instance, command.reason ?? 'Bot 已停止')
    instance.status = 'stopped'
    this.emitBotState(botId, instance)
    this.bots.delete(botId)
    if (this.bots.size === 0) this.stopTickLoop()
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
    if (signals.length > MAX_SIGNAL_BATCH_SIZE) {
      const signalResults = signals.map((signal) => this.rejectedSignal(
        signal.signalId, 'batch_limit_exceeded', '单批最多 100 条动作信号'
      ))
      this.sendEvent({ evt: 'signal-result', requestId, signalResults })
      return
    }
    const results = signals.map((signal) => this.routeSignal(signal))
    if (results.some(isSignalPromise)) {
      void Promise.all(results).then((signalResults) => {
        this.sendEvent({ evt: 'signal-result', requestId, signalResults })
      })
      return
    }
    this.sendEvent({ evt: 'signal-result', requestId, signalResults: results })
  }

  private routeSignal(signal: ActionSignal): SignalItemResult | Promise<SignalItemResult> {
    const instance = this.bots.get(signal.botId)
    if (!instance) return this.rejectedSignal(signal.signalId, 'ephemeral_unavailable', 'Bot 不存在')
    if (instance.scenarioRunner) return instance.scenarioRunner.signal(signal)
    if (signal.generation !== 0 && instance.config.generation !== undefined && signal.generation !== instance.config.generation) {
      return this.rejectedSignal(signal.signalId, 'generation_conflict', 'Bot generation 不匹配', 'conflict')
    }
    return this.signalRouter?.(signal, instance.config) ?? {
      signalId: signal.signalId, accepted: true, skipped: false, status: 'accepted',
    }
  }

  private disposeInstance(botId: string, instance: BotInstance, reason: string): void {
    instance.desiredRunning = false
    this.cancelReconnect(instance, botId)
    this.stopInstanceResources(instance, reason)
    this.bots.delete(botId)
    if (this.bots.size === 0) this.stopTickLoop()
  }

  private stopInstanceResources(instance: BotInstance, reason: string): void {
    if (instance.connectTimer !== null) {
      this.cancelSchedule(instance.connectTimer)
      instance.connectTimer = null
    }
    if (instance.reconnectTimer !== null) {
      this.cancelSchedule(instance.reconnectTimer)
      instance.reconnectTimer = null
    }
    this.releaseConnectSlot(instance)
    instance.eventCleanup?.()
    instance.eventCleanup = null
    const runner = instance.scenarioRunner
    instance.scenarioRunner = null
    if (runner) {
      instance.scenarioState?.capabilities.clearPathfinderGoal()
      void runner.cancel(reason).then(() => runner.dispose())
    } else {
      this.releaseBehavior(instance)
    }
    if (!instance.mcBot) return
    try {
      instance.mcBot.quit()
    } catch {
      // 连接已经关闭时 quit 可能抛错，清理仍需继续。
    }
    instance.mcBot = null
    if (instance.scenarioState) instance.scenarioState.mcBot = null
  }

  private releaseBehavior(instance: BotInstance): void {
    const behavior = instance.behavior
    instance.behavior = null
    if (!behavior) return
    if (behavior.releaseMcBot) behavior.releaseMcBot()
    else behavior.stop()
  }

  private snapshot(botId: string, instance: BotInstance): BotStateSnapshot {
    const snapshot: BotStateSnapshot = {
      id: botId,
      status: instance.status,
      name: instance.config.name,
      behavior: instance.behavior?.name ?? (instance.scenarioRunner ? 'scenario_v2' : instance.config.behavior || 'idle'),
      sessionId: instance.config.sessionId,
      generation: instance.config.generation,
      configHash: instance.config.configHash,
      workerEpoch: this.workerEpoch,
      workerEpochGeneration: this.workerEpochGeneration,
      eventSeq: ++this.eventSeq,
      currentStepId: instance.scenarioRunner?.currentStepId ?? instance.config.resumeStepId,
      reconnectCount: instance.reconnectCount,
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

interface LegacyBehaviorSpec {
  behavior: string
  target?: string
  config?: unknown
}

function legacyBehaviorSpec(step: ScenarioStep): LegacyBehaviorSpec {
  const behavior = typeof step.behavior === 'string' ? step.behavior : 'idle'
  const target = typeof step.target === 'string' ? step.target : undefined
  const config = behavior === 'custom' && isRecord(step.step) ? { steps: [step.step] } : undefined
  return { behavior, target, config }
}

function createLegacyBehaviorAction(
  step: ScenarioStep,
  create: () => Behavior,
  release: (behavior: Behavior) => void
): ScenarioAction {
  let behavior: Behavior | null = null
  const dispose = (): void => {
    const current = behavior
    behavior = null
    if (!current) return
    if (current.releaseMcBot) current.releaseMcBot()
    else current.stop()
    release(current)
  }
  return {
    async start() {
      behavior = create()
      behavior.start()
      return { state: 'running' }
    },
    async tick(context, now) {
      if (!behavior) return { state: 'failed', errorCode: 'ACTION_INTERNAL_ERROR', message: 'legacy behavior 尚未启动' }
      await behavior.tick()
      const durationMs = typeof step.durationMs === 'number' ? step.durationMs : 0
      return now - context.startedAt >= durationMs
        ? { state: 'succeeded', result: { behavior: behavior.name } }
        : { state: 'running' }
    },
    async cancel() { dispose() },
    async dispose() { dispose() },
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function hasScenario(value: unknown): boolean {
  if (value === undefined || value === null) return false
  if (typeof value === 'string') return value.trim() !== ''
  return true
}

function createScenarioCapabilityState(now: () => number): ScenarioCapabilityState {
  const state = {
    spawned: false, dead: false, spawnEventSeq: 0, endReason: undefined, mcBot: null,
    pathfinderEvents: { goalReached: 0, pathFailed: 0 }, pathfinderInit: null, pathfinderGoals: null,
  } as ScenarioCapabilityState
  state.capabilities = {
    now,
    isSpawned: () => state.spawned,
    connectionEndReason: () => state.endReason,
    getPosition: () => scenarioPosition(state.mcBot?.entity?.position),
    setPathfinderGoal: (goal) => setScenarioPathfinderGoal(state, goal.position, goal.radius),
    clearPathfinderGoal: () => state.mcBot?.pathfinder?.setGoal(null),
    pathfinderEvents: () => ({ ...state.pathfinderEvents }),
    entities: () => scenarioEntities(state.mcBot),
    attack: (entityId) => attackScenarioEntity(state.mcBot, entityId),
    isDead: () => state.dead,
    respawn: () => state.mcBot?.respawn?.(),
    spawnEventSeq: () => state.spawnEventSeq,
    chat: (message) => {
      if (!state.mcBot) throw new Error('Bot 尚未连接，无法发送命令')
      state.mcBot.chat(message)
    },
  }
  return state
}

async function setScenarioPathfinderGoal(
  state: ScenarioCapabilityState,
  position: { x: number; y: number; z: number },
  radius: number
): Promise<{ status: 'set' | 'unavailable' | 'failed'; message?: string }> {
  const ready = await ensureScenarioPathfinder(state)
  if (!ready || !state.mcBot?.pathfinder || !state.pathfinderGoals) return { status: 'unavailable' }
  try {
    state.mcBot.pathfinder.setGoal(new state.pathfinderGoals.GoalNear(position.x, position.y, position.z, radius))
    return { status: 'set' }
  } catch (error) {
    return { status: 'failed', message: String(error) }
  }
}

async function ensureScenarioPathfinder(state: ScenarioCapabilityState): Promise<boolean> {
  if (state.mcBot?.pathfinder && state.pathfinderGoals) return true
  state.pathfinderInit ??= initializeScenarioPathfinder(state)
  return state.pathfinderInit
}

async function initializeScenarioPathfinder(state: ScenarioCapabilityState): Promise<boolean> {
  const mcBot = state.mcBot
  if (!mcBot) return false
  try {
    const module = await import('mineflayer-pathfinder')
    const commonJS = module.default as typeof import('mineflayer-pathfinder')
    const plugin = module.pathfinder ?? commonJS.pathfinder
    if (!mcBot.pathfinder) mcBot.loadPlugin?.(plugin)
    state.pathfinderGoals = module.goals ?? commonJS.goals
    return Boolean(mcBot.pathfinder && state.pathfinderGoals)
  } catch {
    return false
  }
}

function scenarioEntities(mcBot: McBotLike | null): import('../scenario/types.js').ScenarioEntity[] {
  if (!mcBot?.entities) return []
  return Object.values(mcBot.entities).flatMap((entity) => {
    const position = scenarioPosition(entity.position)
    if (!position || entity.id === mcBot.entity?.id) return []
    const type = entity.name ?? entity.type
    return [{
      id: entity.id,
      kind: scenarioEntityKind(entity),
      type: type?.toLowerCase(),
      name: entity.username ?? entity.name,
      health: entity.health,
      dead: entity.isValid === false || (entity.health !== undefined && entity.health <= 0),
      position,
    }]
  })
}

function scenarioEntityKind(entity: McEntityLike): string | undefined {
  const kind = entity.kind?.toLowerCase()
  if (kind?.includes('hostile')) return 'hostile'
  if (kind?.includes('passive')) return 'passive'
  return entity.kind ?? entity.type
}

function attackScenarioEntity(mcBot: McBotLike | null, entityId: string | number): boolean {
  const entity = mcBot?.entities && Object.values(mcBot.entities).find((candidate) => String(candidate.id) === String(entityId))
  if (!mcBot?.attack || !entity) return false
  mcBot.attack(entity)
  return true
}

function scenarioPosition(value?: { x: number; y: number; z: number }): { x: number; y: number; z: number } | undefined {
  if (!value || !Number.isFinite(value.x) || !Number.isFinite(value.y) || !Number.isFinite(value.z)) return undefined
  return { x: value.x, y: value.y, z: value.z }
}

function isSignalPromise(value: SignalItemResult | Promise<SignalItemResult>): value is Promise<SignalItemResult> {
  return value instanceof Promise
}

function sanitizeStopReason(reason?: string): string | undefined {
  if (!reason) return reason
  return reason
    .replace(/\b(token|password|secret|authorization)\s*=\s*\S+/gi, '$1=[已脱敏]')
    .replace(/\bBearer\s+\S+/gi, 'Bearer [已脱敏]')
}

function isFleetCreateCommand(command: CreateBotsCommand): boolean {
  return Boolean(command.requestId && command.batchId && command.idempotencyKey)
}

function isStableBatchResult(result: BotItemResult): boolean {
  return result.status !== 'ephemeral_unavailable' && result.status !== 'capacity_insufficient'
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
