import type {
  ActionStartResult,
  ActionTickResult,
  ScenarioAction,
  ScenarioActionContext,
  ScenarioActionResult,
  ScenarioActionSignal,
  ScenarioEntity,
  ScenarioEntityId,
  ScenarioPathfinderEvents,
  ScenarioPosition,
  ScenarioStep,
} from './types.js'

const TEMPLATE_PATTERN = /{{\s*([A-Za-z][A-Za-z0-9]*)\s*}}/g
const TEMPLATE_VARIABLES = new Set([
  'botName', 'botUuid', 'runId', 'cohortKey', 'correlationToken', 'roomKey',
])
const LOCAL_ARRIVAL_STABLE_MS = 500
const ATTACK_REACH = 4
const DEFAULT_TARGET_NOT_FOUND_MS = 5_000

abstract class BaseScenarioAction implements ScenarioAction {
  abstract start(ctx: ScenarioActionContext): Promise<ActionStartResult>
  abstract tick(ctx: ScenarioActionContext, now: number): Promise<ActionTickResult>
  async cancel(): Promise<void> {}
  async dispose(): Promise<void> {}
}

class WaitSpawnAction extends BaseScenarioAction {
  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> { return this.result(ctx) }
  async tick(ctx: ScenarioActionContext): Promise<ActionTickResult> { return this.result(ctx) }

  private result(ctx: ScenarioActionContext): ActionTickResult {
    if (ctx.capabilities.isSpawned()) return succeeded({ spawned: true })
    const reason = ctx.capabilities.connectionEndReason()
    return reason ? failed('CONNECT_ENDED', `连接已结束: ${reason}`) : running({ type: 'waiting', wait: 'spawn' })
  }
}

class WaitAction extends BaseScenarioAction {
  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> { return this.result(ctx, ctx.startedAt) }
  async tick(ctx: ScenarioActionContext, now: number): Promise<ActionTickResult> { return this.result(ctx, now) }

  private result(ctx: ScenarioActionContext, now: number): ActionTickResult {
    const durationMs = numberField(ctx.step.durationMs)
    if (durationMs === undefined) return invalidStep('wait 缺少 durationMs')
    return now - ctx.startedAt >= durationMs
      ? succeeded({ durationMs })
      : running({ type: 'waiting', wait: 'duration', durationMs })
  }
}

class SendCommandAction extends BaseScenarioAction {
  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    if (typeof ctx.step.command !== 'string') return invalidStep('send_command 缺少 command')
    ctx.newCorrelationToken()
    const expanded = expandTemplate(ctx.step.command, ctx.templateVariables())
    if (expanded.error) return invalidStep(expanded.error)
    ctx.capabilities.chat(expanded.value)
    return { ...succeeded({ sent: true }), correlationToken: ctx.currentCorrelationToken() }
  }

  async tick(): Promise<ActionTickResult> { return succeeded({ sent: true }) }
}

class WaitProbeEventAction extends BaseScenarioAction {
  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    if (typeof ctx.step.event !== 'string' || ctx.step.event === '') return invalidStep('wait_probe_event 缺少 event')
    return {
      ...running({ type: 'waiting', wait: 'external', eventType: ctx.step.event }),
      correlationToken: ctx.ensureCorrelationToken(),
    }
  }

  async tick(): Promise<ActionTickResult> { return running() }

  async signal(ctx: ScenarioActionContext, signal: ScenarioActionSignal): Promise<ActionTickResult> {
    const eventType = probeEventType(signal)
    if (eventType !== ctx.step.event) return running(undefined, '探针事件类型不匹配')
    return { ...succeeded({ eventType, payload: signal.payload ?? {} }), signalAccepted: true }
  }
}

class BarrierAction extends BaseScenarioAction {
  private releaseAtUnixMs: number | undefined
  private failAtUnixMs: number | undefined

  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    if (typeof ctx.step.key !== 'string' || !isBarrierRelease(ctx.step.release)) return invalidStep('barrier 缺少 key 或 release')
    return { ...running(barrierArrivedResult(ctx)), correlationToken: ctx.ensureCorrelationToken() }
  }

  async tick(_ctx: ScenarioActionContext, now: number): Promise<ActionTickResult> {
    if (this.releaseAtUnixMs !== undefined && now >= this.releaseAtUnixMs) {
      return succeeded({ releaseAtUnixMs: this.releaseAtUnixMs })
    }
    if (this.failAtUnixMs !== undefined && now >= this.failAtUnixMs) {
      return failed('BARRIER_TIMEOUT', '屏障在统一截止时间未达到释放条件')
    }
    return running()
  }

  async signal(ctx: ScenarioActionContext, signal: ScenarioActionSignal): Promise<ActionTickResult> {
    const payload = recordValue(signal.payload)
    if (payload?.round !== ctx.attempt) return running(undefined, '屏障信号 round 不匹配')
    if (signal.type === 'barrier-release') return this.acceptRelease(ctx, payload)
    if (signal.type === 'barrier-fail') return this.acceptFailure(ctx, payload)
    return running(undefined, '屏障信号类型不匹配')
  }

  private acceptRelease(ctx: ScenarioActionContext, payload: Record<string, unknown>): ActionTickResult {
    const releaseAt = numberField(payload.releaseAtUnixMs)
    if (releaseAt === undefined || releaseAt > ctx.deadline) return running(undefined, '屏障释放时间无效')
    this.releaseAtUnixMs = releaseAt
    this.failAtUnixMs = undefined
    return { ...running({ releaseAtUnixMs: releaseAt }), signalAccepted: true }
  }

  private acceptFailure(ctx: ScenarioActionContext, payload: Record<string, unknown>): ActionTickResult {
    const failAt = numberField(payload.failAtUnixMs)
    if (failAt === undefined || failAt > ctx.deadline) return running(undefined, '屏障失败时间无效')
    if (this.releaseAtUnixMs === undefined) this.failAtUnixMs = failAt
    return { ...running({ failAtUnixMs: failAt }), signalAccepted: true }
  }
}

class RoamInAreaAction extends BaseScenarioAction {
  private random: DeterministicRandom | undefined
  private goal: ScenarioPosition | undefined
  private pauseUntil: number | undefined
  private waypointIndex = 0
  private failures = 0
  private events: ScenarioPathfinderEvents = { goalReached: 0, pathFailed: 0 }
  private needsReplan = false

  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    const error = validateRoam(ctx)
    if (error) return invalidStep(error)
    this.random = new DeterministicRandom(`${ctx.seed}|${ctx.botOrdinal}|${ctx.step.id}`)
    this.events = ctx.capabilities.pathfinderEvents()
    this.waypointIndex = this.initialWaypoint(ctx)
    this.goal = this.nextGoal(ctx)
    return this.installGoal(ctx)
  }

  async tick(ctx: ScenarioActionContext, now: number): Promise<ActionTickResult> {
    if (now - ctx.startedAt >= numberField(ctx.step.durationMs)!) return succeeded(this.result())
    const pathResult = await this.handlePathFailures(ctx)
    if (pathResult) return pathResult
    if (this.needsReplan) return this.installGoal(ctx)
    if (this.pauseUntil !== undefined) return this.tickPause(ctx, now)
    if (this.goal && arrived(ctx.capabilities.getPosition(), this.goal, 1)) this.beginPause(ctx, now)
    return running(this.result())
  }

  private async handlePathFailures(ctx: ScenarioActionContext): Promise<ActionTickResult | undefined> {
    const current = ctx.capabilities.pathfinderEvents()
    const delta = Math.max(0, current.pathFailed - this.events.pathFailed)
    this.events = current
    if (delta === 0) return undefined
    this.failures += delta
    if (this.failures >= maxPathFailures(ctx.step)) return failed('PATH_NOT_FOUND', '连续路径失败次数达到上限', this.result())
    this.needsReplan = true
    return undefined
  }

  private async installGoal(ctx: ScenarioActionContext): Promise<ActionTickResult> {
    const result = await ctx.capabilities.setPathfinderGoal({ position: this.goal!, radius: 1 })
    if (ctx.cancelToken.cancelled) {
      ctx.capabilities.clearPathfinderGoal()
      return running(this.result())
    }
    this.needsReplan = result.status === 'failed'
    if (result.status === 'unavailable') return failed('PATHFINDER_UNAVAILABLE', 'pathfinder 不可用')
    if (result.status === 'failed') {
      this.failures++
      if (this.failures >= maxPathFailures(ctx.step)) return failed('PATH_NOT_FOUND', result.message ?? '无法规划路径')
    }
    return running(this.result())
  }

  private async tickPause(ctx: ScenarioActionContext, now: number): Promise<ActionTickResult> {
    if (now < this.pauseUntil!) return running(this.result())
    this.pauseUntil = undefined
    this.failures = 0
    this.goal = this.nextGoal(ctx)
    this.needsReplan = false
    return this.installGoal(ctx)
  }

  private beginPause(ctx: ScenarioActionContext, now: number): void {
    ctx.capabilities.clearPathfinderGoal()
    const pause = intRange(ctx.step.pauseMs)
    const delay = this.random!.int(pause.min, pause.max)
    this.pauseUntil = now + delay
  }

  private nextGoal(ctx: ScenarioActionContext): ScenarioPosition {
    const area = recordValue(ctx.step.area)!
    if (area.type === 'waypoints') return this.nextWaypoint(area)
    const center = positionField(area.center)!
    const radius = numberField(area.radius)!
    const angle = this.random!.next() * Math.PI * 2
    const distance = Math.sqrt(this.random!.next()) * radius
    return { x: center.x + Math.cos(angle) * distance, y: center.y, z: center.z + Math.sin(angle) * distance }
  }

  private nextWaypoint(area: Record<string, unknown>): ScenarioPosition {
    const waypoints = (area.waypoints as unknown[]).map(positionField) as ScenarioPosition[]
    const target = waypoints[this.waypointIndex % waypoints.length]
    this.waypointIndex = (this.waypointIndex + 1) % waypoints.length
    return { ...target }
  }

  private initialWaypoint(ctx: ScenarioActionContext): number {
    const area = recordValue(ctx.step.area)
    const count = Array.isArray(area?.waypoints) ? area.waypoints.length : 0
    return count > 0 ? this.random!.int(0, count - 1) : 0
  }

  private result(): object { return { target: this.goal, pauseUntil: this.pauseUntil, pathFailures: this.failures } }
}

class MoveToAndWaitAction extends BaseScenarioAction {
  private target: ScenarioPosition | undefined
  private arrivedAt: number | undefined
  private localArrived = false
  private probeArrived = false
  private events: ScenarioPathfinderEvents = { goalReached: 0, pathFailed: 0 }

  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    this.target = positionField(ctx.step.pos)
    const radius = numberField(ctx.step.radius)
    if (!this.target || radius === undefined) return invalidStep('move_to_and_wait 缺少 pos 或 radius')
    this.events = ctx.capabilities.pathfinderEvents()
    const result = await ctx.capabilities.setPathfinderGoal({ position: this.target, radius })
    if (ctx.cancelToken.cancelled) {
      ctx.capabilities.clearPathfinderGoal()
      return running(this.result())
    }
    if (result.status === 'unavailable') return failed('PATHFINDER_UNAVAILABLE', 'pathfinder 不可用')
    if (result.status === 'failed') return failed('PATH_NOT_FOUND', result.message ?? '无法规划路径')
    return { ...running(this.result()), correlationToken: ctx.ensureCorrelationToken() }
  }

  async tick(ctx: ScenarioActionContext, now: number): Promise<ActionTickResult> {
    const current = ctx.capabilities.pathfinderEvents()
    if (current.pathFailed > this.events.pathFailed) return failed('PATH_NOT_FOUND', '目标路径不可达', this.result())
    this.events = current
    this.updateLocalArrival(ctx, now)
    return this.completed(ctx) ? succeeded(this.result()) : running(this.result())
  }

  async signal(ctx: ScenarioActionContext, signal: ScenarioActionSignal): Promise<ActionTickResult> {
    const payload = recordValue(signal.payload)
    if (probeEventType(signal) !== 'area_arrived' || payload?.areaId !== ctx.step.areaId) {
      return running(undefined, '抵达探针事件或 areaId 不匹配')
    }
    this.probeArrived = true
    return {
      ...(this.completed(ctx) ? succeeded(this.result()) : running(this.result())),
      signalAccepted: true,
    }
  }

  private updateLocalArrival(ctx: ScenarioActionContext, now: number): void {
    if (!arrived(ctx.capabilities.getPosition(), this.target!, numberField(ctx.step.radius)!)) {
      this.arrivedAt = undefined
      this.localArrived = false
      return
    }
    this.arrivedAt ??= now
    this.localArrived = now - this.arrivedAt >= LOCAL_ARRIVAL_STABLE_MS
  }

  private completed(ctx: ScenarioActionContext): boolean {
    return this.localArrived && (ctx.step.requireProbeEvent !== 'area_arrived' || this.probeArrived)
  }

  private result(): object { return { localArrived: this.localArrived, probeArrived: this.probeArrived, target: this.target } }
}

class FindEntityAction extends BaseScenarioAction {
  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    const selected = selectEntity(ctx, ctx.step.selector)
    if (selected.error) return invalidStep(selected.error)
    if (!selected.entity) return failed('TARGET_NOT_FOUND', '未找到符合条件的实体')
    ctx.setLockedEntityId(selected.entity.id)
    return succeeded({ entityId: selected.entity.id })
  }

  async tick(ctx: ScenarioActionContext): Promise<ActionTickResult> { return this.start(ctx) }
}

class AttackUntilAction extends BaseScenarioAction {
  private damage = 0
  private kills = 0
  private damageEvents = 0
  private clientAttackAttempts = 0
  private nextAttackAt = 0
  private missingSince: number | undefined
  private hadTarget = false
  private targetEntityId: ScenarioEntityId | undefined
  private chaseGoal: ScenarioPosition | undefined
  private searchGoal: ScenarioPosition | undefined
  private searchRandom: DeterministicRandom | undefined
  private pathEvents: ScenarioPathfinderEvents = { goalReached: 0, pathFailed: 0 }
  private pathFailures = 0
  private pathfinderGoals = 0
  private reacquireCount = 0
  private respawnCount = 0
  private respawnRequestedAt: number | undefined
  private respawnSpawnSeq = 0
  private observationStart: number | undefined
  private evidenceWindowMs: number | undefined
  private evidenceSatisfied: boolean | undefined
  private readonly windowCounts = new Map<number, number>()
  private readonly probeEvents = new Set<string>()

  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    const error = validateAttack(ctx.step)
    if (error) return invalidStep(error)
    this.nextAttackAt = ctx.startedAt
    this.pathEvents = ctx.capabilities.pathfinderEvents()
    this.searchRandom = new DeterministicRandom(`${ctx.seed ?? 0}|${ctx.botOrdinal ?? 0}|${ctx.step.id}|search`)
    this.evidenceWindowMs = numberField(recordValue(ctx.step.stop)?.evidenceWindowMs)
    return this.drive(ctx, ctx.startedAt)
  }

  async tick(ctx: ScenarioActionContext, now: number): Promise<ActionTickResult> {
    const duration = numberField(recordValue(ctx.step.stop)?.durationMs)!
    if (now - ctx.startedAt >= duration) return this.finishAtDeadline(ctx)
    const trusted = this.trustedSatisfied(ctx.step)
    if (!requiresEvidenceComplete(ctx.step) && trusted) return succeeded(this.result())
    return this.drive(ctx, now)
  }

  async signal(ctx: ScenarioActionContext, signal: ScenarioActionSignal): Promise<ActionTickResult> {
    const eventType = probeEventType(signal)
    const payload = recordValue(signal.payload) ?? {}
    if (eventType === 'observation-start') return this.startObservation(signal, payload)
    if (eventType === 'observation-complete') return this.completeObservation(ctx, signal, payload)
    if (!this.applyTrustedEvent(ctx, signal, eventType, payload)) return running(undefined, '攻击信号类型不匹配')
    const satisfied = !requiresEvidenceComplete(ctx.step) && this.trustedSatisfied(ctx.step)
    const result = satisfied ? succeeded(this.result()) : running(this.result())
    return { ...result, signalAccepted: true }
  }

  private async drive(ctx: ScenarioActionContext, now: number): Promise<ActionTickResult> {
    if (ctx.cancelToken.cancelled) return running(this.result())
    const respawn = this.handleRespawn(ctx, now)
    if (respawn) return respawn
    const target = await this.resolveTarget(ctx, now)
    if (target.result) return target.result
    if (!target.entity) return running(this.result())
    const chase = await this.chase(ctx, target.entity)
    if (chase || ctx.cancelToken.cancelled) return chase ?? running(this.result())
    if (distance(ctx.capabilities.getPosition(), target.entity.position) <= ATTACK_REACH && now >= this.nextAttackAt) {
      if (!ctx.capabilities.attack(target.entity.id)) {
        this.resetTarget(ctx)
        this.missingSince ??= now
        return running(this.result())
      }
      this.clientAttackAttempts++
      this.nextAttackAt = now + numberField(ctx.step.attackIntervalMs)!
    }
    return running(this.result())
  }

  private async resolveTarget(ctx: ScenarioActionContext, now: number): Promise<{ entity?: ScenarioEntity; result?: ActionTickResult }> {
    const entities = ctx.capabilities.entities()
    const locked = entities.find((entity) => sameEntityId(entity.id, ctx.lockedEntityId()) && entityAlive(entity))
    if (locked) {
      this.hadTarget = true
      this.targetEntityId = locked.id
      this.missingSince = undefined
      return { entity: locked }
    }
    ctx.setLockedEntityId(undefined)
    this.targetEntityId = undefined
    const maySelect = !this.hadTarget || ctx.step.reacquire !== false
    const selected = maySelect ? selectEntity(ctx, ctx.step.selector) : {}
    if ('error' in selected && selected.error) return { result: invalidStep(selected.error) }
    if (selected.entity) {
      if (this.hadTarget) this.reacquireCount++
      this.hadTarget = true
      this.targetEntityId = selected.entity.id
      this.searchGoal = undefined
      ctx.setLockedEntityId(selected.entity.id)
      this.missingSince = undefined
      return { entity: selected.entity }
    }
    const search = await this.search(ctx)
    if (search) return { result: search }
    this.missingSince ??= now
    const timeout = numberField(ctx.step.targetNotFoundTimeoutMs) ?? DEFAULT_TARGET_NOT_FOUND_MS
    return now - this.missingSince >= timeout
      ? { result: failed('TARGET_NOT_FOUND', '目标空窗超过允许时长', this.result()) }
      : {}
  }

  private handleRespawn(ctx: ScenarioActionContext, now: number): ActionTickResult | undefined {
    const config = recordValue(ctx.step.respawn)
    if (!config) return undefined
    if (this.respawnRequestedAt !== undefined) {
      if (ctx.capabilities.spawnEventSeq() > this.respawnSpawnSeq && ctx.capabilities.isSpawned() && !ctx.capabilities.isDead()) {
        this.respawnRequestedAt = undefined
        this.resetTarget(ctx)
        return undefined
      }
      const timeout = numberField(config.timeoutMs) ?? 10_000
      return now - this.respawnRequestedAt >= timeout
        ? failed('RESPAWN_TIMEOUT', '重生等待超时', this.result())
        : running(this.result())
    }
    if (!ctx.capabilities.isDead()) return undefined
    const maxAttempts = Math.floor(numberField(config.maxAttempts) ?? 1)
    if (this.respawnCount >= maxAttempts) return failed('RESPAWN_LIMIT_EXCEEDED', '重生次数达到上限', this.result())
    this.respawnSpawnSeq = ctx.capabilities.spawnEventSeq()
    this.respawnRequestedAt = now
    this.respawnCount++
    this.resetTarget(ctx)
    ctx.capabilities.respawn()
    return running(this.result())
  }

  private async search(ctx: ScenarioActionContext): Promise<ActionTickResult | undefined> {
    const area = recordValue(ctx.step.searchArea)
    if (!area) return undefined
    const events = ctx.capabilities.pathfinderEvents()
    const failedPath = events.pathFailed > this.pathEvents.pathFailed
    this.pathEvents = events
    if (failedPath) this.searchGoal = undefined
    if (this.searchGoal && !failedPath) return running(this.result())
    const goal = this.nextSearchGoal(area)
    if (!goal) return invalidStep('searchArea 无效')
    const result = await ctx.capabilities.setPathfinderGoal({ position: goal, radius: 1 })
    if (ctx.cancelToken.cancelled) {
      ctx.capabilities.clearPathfinderGoal()
      return running(this.result())
    }
    if (result.status === 'unavailable') return failed('PATHFINDER_UNAVAILABLE', 'pathfinder 不可用')
    if (result.status === 'failed') {
      this.pathFailures++
      this.searchGoal = undefined
      return this.pathFailures >= maxPathFailures(ctx.step)
        ? failed('PATH_NOT_FOUND', result.message ?? '无法规划搜敌路径', this.result())
        : running(this.result())
    }
    this.pathfinderGoals++
    this.searchGoal = goal
    return running(this.result())
  }

  private async chase(ctx: ScenarioActionContext, entity: ScenarioEntity): Promise<ActionTickResult | undefined> {
    if (ctx.step.chase !== true || distance(ctx.capabilities.getPosition(), entity.position) <= ATTACK_REACH) {
      if (this.chaseGoal) ctx.capabilities.clearPathfinderGoal()
      this.chaseGoal = undefined
      return undefined
    }
    const events = ctx.capabilities.pathfinderEvents()
    const failedPath = events.pathFailed > this.pathEvents.pathFailed
    this.pathEvents = events
    if (!failedPath && this.chaseGoal && distance(this.chaseGoal, entity.position) < 0.5) return undefined
    const result = await ctx.capabilities.setPathfinderGoal({ position: entity.position, radius: ATTACK_REACH - 0.5 })
    if (ctx.cancelToken.cancelled) {
      ctx.capabilities.clearPathfinderGoal()
      return running(this.result())
    }
    if (result.status === 'unavailable') return failed('PATHFINDER_UNAVAILABLE', 'pathfinder 不可用')
    if (result.status === 'failed' || failedPath) return this.chaseFailed(ctx, result.message)
    this.pathFailures = 0
    this.pathfinderGoals++
    this.chaseGoal = { ...entity.position }
    return undefined
  }

  private chaseFailed(ctx: ScenarioActionContext, message: string | undefined): ActionTickResult | undefined {
    this.pathFailures++
    this.resetTarget(ctx)
    if (recordValue(ctx.step.searchArea) && this.pathFailures < maxPathFailures(ctx.step)) return running(this.result())
    return failed('PATH_NOT_FOUND', message ?? '无法追击目标', this.result())
  }

  private resetTarget(ctx: ScenarioActionContext): void {
    ctx.setLockedEntityId(undefined)
    this.targetEntityId = undefined
    this.chaseGoal = undefined
    this.searchGoal = undefined
    ctx.capabilities.clearPathfinderGoal()
  }

  private nextSearchGoal(area: Record<string, unknown>): ScenarioPosition | undefined {
    if (area.type === 'radius') {
      const center = positionField(area.center)
      const radius = numberField(area.radius)
      if (!center || radius === undefined) return undefined
      const angle = this.searchRandom!.next() * Math.PI * 2
      const length = Math.sqrt(this.searchRandom!.next()) * radius
      return { x: center.x + Math.cos(angle) * length, y: center.y, z: center.z + Math.sin(angle) * length }
    }
    if (area.type === 'waypoints' && Array.isArray(area.waypoints) && area.waypoints.length > 0) {
      const index = this.searchRandom!.int(0, area.waypoints.length - 1)
      return positionField(area.waypoints[index])
    }
    return undefined
  }

  private applyTrustedEvent(
    ctx: ScenarioActionContext,
    signal: ScenarioActionSignal,
    eventType: string | undefined,
    payload: Record<string, unknown>
  ): boolean {
    const probeEvent = recordValue(ctx.step.stop)?.probeEvent
    const matchesProbe = typeof probeEvent === 'string' && eventType === probeEvent
    if (matchesProbe) this.probeEvents.add(probeEvent)
    if (eventType === 'damage' || eventType === 'damage_dealt') {
      this.damage += positiveNumber(payload.damage ?? payload.amount ?? payload.value, 1)
      this.damageEvents++
      this.recordEvidence(signal, payload)
      return true
    }
    if (eventType === 'kill' || eventType === 'entity_killed') {
      this.kills += positiveNumber(payload.count, 1)
      this.recordEvidence(signal, payload)
      return true
    }
    return matchesProbe
  }

  private startObservation(signal: ScenarioActionSignal, payload: Record<string, unknown>): ActionTickResult {
    const startedAt = eventTime(signal, payload, 'observationStartUnixMs')
    if (startedAt === undefined) return running(undefined, 'observation-start 缺少可信时间')
    this.observationStart = startedAt
    this.windowCounts.clear()
    this.evidenceSatisfied = undefined
    return { ...running(this.result()), signalAccepted: true }
  }

  private completeObservation(
    ctx: ScenarioActionContext,
    signal: ScenarioActionSignal,
    payload: Record<string, unknown>
  ): ActionTickResult {
    const stop = recordValue(ctx.step.stop)!
    const windowMs = numberField(stop.evidenceWindowMs)
    const minimum = numberField(stop.minDamageEventsPerWindow)
    if (!windowMs || !minimum || this.observationStart === undefined) {
      return { ...failed('ATTACK_ASSERTION_UNMET', '观察窗口缺少统一起点或证据配置', this.result()), signalAccepted: true }
    }
    const completeAt = eventTime(signal, payload, 'observationCompleteUnixMs')
    if (completeAt === undefined) return running(undefined, 'observation-complete 缺少可信时间')
    const windowCount = Math.floor((completeAt - this.observationStart) / windowMs)
    this.evidenceSatisfied = windowCount > 0 && everyWindowSatisfied(this.windowCounts, windowCount, minimum)
    const result = this.evidenceSatisfied && this.trustedSatisfied(ctx.step)
      ? succeeded(this.result())
      : failed('ATTACK_ASSERTION_UNMET', '观察窗口可信攻击证据不足', this.result())
    return { ...result, signalAccepted: true }
  }

  private recordEvidence(signal: ScenarioActionSignal, payload: Record<string, unknown>): void {
    const start = this.observationStart
    if (start === undefined) return
    const occurredAt = eventTime(signal, payload, 'occurredAtUnixMs')
    if (occurredAt === undefined) return
    const index = Math.floor((occurredAt - start) / (this.evidenceWindowMs ?? 1))
    if (index >= 0) this.windowCounts.set(index, (this.windowCounts.get(index) ?? 0) + 1)
  }

  private trustedSatisfied(step: ScenarioStep): boolean {
    const stop = recordValue(step.stop)!
    const conditions: boolean[] = []
    if (positiveNumber(stop.damageAtLeast, 0) > 0) conditions.push(this.damage >= positiveNumber(stop.damageAtLeast, 0))
    if (positiveNumber(stop.killsAtLeast, 0) > 0) conditions.push(this.kills >= positiveNumber(stop.killsAtLeast, 0))
    if (typeof stop.probeEvent === 'string' && stop.probeEvent !== '') conditions.push(this.probeEvents.has(stop.probeEvent))
    if (positiveNumber(stop.minDamageEventsPerWindow, 0) > 0) conditions.push(this.evidenceSatisfied === true)
    if (conditions.length === 0) return false
    return stop.successPolicy === 'all' ? conditions.every(Boolean) : conditions.some(Boolean)
  }

  private finishAtDeadline(ctx: ScenarioActionContext): ActionTickResult {
    if (ctx.step.legacyDurationSuccess === true || this.trustedSatisfied(ctx.step)) return succeeded(this.result())
    const minClient = positiveNumber(recordValue(ctx.step.stop)?.minClientAttackAttempts, 0)
    if (minClient > 0) {
      return this.clientAttackAttempts >= minClient
        ? succeeded(this.result())
        : failed('ATTACK_ACTIVITY_UNMET', '攻击截止时客户端攻击活跃度不足', this.result())
    }
    return failed('ATTACK_ASSERTION_UNMET', '攻击截止时可信条件未满足', this.result())
  }

  private result(): object {
    return {
      targetEntityId: this.targetEntityId,
      trustedDamage: this.damage,
      trustedKills: this.kills,
      trustedDamageEvents: this.damageEvents,
      clientAttackAttempts: this.clientAttackAttempts,
      respawnCount: this.respawnCount,
      reacquireCount: this.reacquireCount,
      pathfinderGoals: this.pathfinderGoals,
      evidenceSatisfied: this.evidenceSatisfied,
    }
  }
}

class RespawnAndRejoinAction extends BaseScenarioAction {
  private spawnSequence = 0

  async start(ctx: ScenarioActionContext): Promise<ActionStartResult> {
    if (typeof ctx.step.entryStepId !== 'string' || ctx.step.entryStepId === '') return invalidStep('respawn_and_rejoin 缺少 entryStepId')
    this.spawnSequence = ctx.capabilities.spawnEventSeq()
    if (ctx.capabilities.isDead()) ctx.capabilities.respawn()
    return running({ wait: 'respawn', entryStepId: ctx.step.entryStepId })
  }

  async tick(ctx: ScenarioActionContext, now: number): Promise<ActionTickResult> {
    if (ctx.runDeadline !== undefined && now >= ctx.runDeadline) return failed('ACTION_CANCELLED', '运行总截止时间已到')
    const spawned = ctx.capabilities.spawnEventSeq() > this.spawnSequence
    if (!spawned || ctx.capabilities.isDead() || !ctx.capabilities.isSpawned()) return running({ wait: 'respawn' })
    return { ...succeeded({ respawned: true, entryStepId: ctx.step.entryStepId }), jumpToStepId: ctx.step.entryStepId as string }
  }
}

class UnsupportedAction extends BaseScenarioAction {
  constructor(private readonly type: string) { super() }
  async start(): Promise<ActionStartResult> { return invalidStep(`未实现动作类型 ${this.type}`) }
  async tick(): Promise<ActionTickResult> { return invalidStep(`未实现动作类型 ${this.type}`) }
}

export function createScenarioAction(step: ScenarioStep): ScenarioAction {
  switch (step.type) {
    case 'wait_spawn': return new WaitSpawnAction()
    case 'roam_in_area': return new RoamInAreaAction()
    case 'send_command': return new SendCommandAction()
    case 'wait_probe_event': return new WaitProbeEventAction()
    case 'barrier': return new BarrierAction()
    case 'move_to_and_wait': return new MoveToAndWaitAction()
    case 'find_entity': return new FindEntityAction()
    case 'attack_until': return new AttackUntilAction()
    case 'wait': return new WaitAction()
    case 'respawn_and_rejoin': return new RespawnAndRejoinAction()
    default: return new UnsupportedAction(step.type)
  }
}

function validateRoam(ctx: ScenarioActionContext): string | undefined {
  if (ctx.seed === undefined || ctx.botOrdinal === undefined) return 'roam_in_area 缺少稳定 seed 或 botOrdinal'
  if (numberField(ctx.step.durationMs) === undefined) return 'roam_in_area 缺少 durationMs'
  const area = recordValue(ctx.step.area)
  if (!area || (area.type !== 'radius' && area.type !== 'waypoints')) return 'roam_in_area area 无效'
  if (area.type === 'radius' && (!positionField(area.center) || numberField(area.radius) === undefined)) return 'roam radius 缺少 center 或 radius'
  if (area.type === 'waypoints' && (!Array.isArray(area.waypoints) || area.waypoints.length === 0 || area.waypoints.some((item) => !positionField(item)))) return 'roam waypoints 无效'
  return undefined
}

function validateAttack(step: ScenarioStep): string | undefined {
  const stop = recordValue(step.stop)
  if (!recordValue(step.selector) || !stop) return 'attack_until 缺少 selector 或 stop'
  if (numberField(step.attackIntervalMs) === undefined || numberField(stop.durationMs) === undefined) return 'attack_until 攻击间隔或 duration 无效'
  const regex = recordValue(step.selector)?.nameRegex
  if (typeof regex === 'string' && regex.length > 128) return 'selector.nameRegex 过长'
  if (typeof regex === 'string' && regex !== '') {
    try { new RegExp(regex) } catch { return 'selector.nameRegex 无法编译' }
  }
  return undefined
}

function selectEntity(ctx: ScenarioActionContext, selectorValue: unknown): { entity?: ScenarioEntity; error?: string } {
  const selector = recordValue(selectorValue)
  if (!selector) return { error: 'selector 无效' }
  let regex: RegExp | undefined
  if (typeof selector.nameRegex === 'string' && selector.nameRegex !== '') {
    try { regex = new RegExp(selector.nameRegex) } catch { return { error: 'selector.nameRegex 无法编译' } }
  }
  const origin = ctx.capabilities.getPosition()
  const matches = ctx.capabilities.entities().filter((entity) => entityMatches(entity, selector, regex, origin))
  matches.sort((left, right) => compareEntities(ctx, left, right, selector.priority, origin))
  return { entity: matches[0] }
}

function entityMatches(
  entity: ScenarioEntity,
  selector: Record<string, unknown>,
  regex: RegExp | undefined,
  origin: ScenarioPosition | undefined
): boolean {
  if (!entityAlive(entity)) return false
  if (typeof selector.kind === 'string' && selector.kind !== '' && entity.kind !== selector.kind) return false
  const types = Array.isArray(selector.types)
    ? selector.types.filter((item): item is string => typeof item === 'string').map((item) => item.toLowerCase())
    : []
  if (types.length > 0 && !types.includes((entity.type ?? '').toLowerCase())) return false
  if (regex && !regex.test(entity.name ?? '')) return false
  return distance(origin, entity.position) <= (numberField(selector.radius) ?? Number.POSITIVE_INFINITY)
}

function compareEntities(
  ctx: ScenarioActionContext,
  left: ScenarioEntity,
  right: ScenarioEntity,
  priority: unknown,
  origin: ScenarioPosition | undefined
): number {
  if (priority === 'random') {
    const leftScore = stableEntityScore(ctx, left)
    const rightScore = stableEntityScore(ctx, right)
    if (leftScore !== rightScore) return leftScore - rightScore
  }
  if (priority === 'lowest_health') {
    const health = (left.health ?? Number.POSITIVE_INFINITY) - (right.health ?? Number.POSITIVE_INFINITY)
    if (health !== 0) return health
  }
  const distanceDelta = distance(origin, left.position) - distance(origin, right.position)
  return distanceDelta !== 0 ? distanceDelta : String(left.id).localeCompare(String(right.id))
}

function stableEntityScore(ctx: ScenarioActionContext, entity: ScenarioEntity): number {
  return hashSeed(`${ctx.seed ?? 0}|${ctx.botOrdinal ?? 0}|${ctx.step.id}|${String(entity.id)}`)
}

function entityAlive(entity: ScenarioEntity): boolean {
  return !entity.dead && (entity.health === undefined || entity.health > 0)
}

function sameEntityId(left: ScenarioEntityId, right: ScenarioEntityId | undefined): boolean {
  return right !== undefined && String(left) === String(right)
}

function requiresEvidenceComplete(step: ScenarioStep): boolean {
  return positiveNumber(recordValue(step.stop)?.minDamageEventsPerWindow, 0) > 0
}

function everyWindowSatisfied(counts: Map<number, number>, windowCount: number, minimum: number): boolean {
  for (let index = 0; index < windowCount; index++) {
    if ((counts.get(index) ?? 0) < minimum) return false
  }
  return true
}

function eventTime(signal: ScenarioActionSignal, payload: Record<string, unknown>, field: string): number | undefined {
  return numberField(payload[field]) ?? numberField(signal.observedAt)
}

function barrierArrivedResult(ctx: ScenarioActionContext): object {
  return {
    type: 'barrier-arrived', stageIndex: ctx.stageIndex, cohortKey: ctx.cohortKey,
    barrierKey: ctx.step.key, round: ctx.attempt, release: ctx.step.release,
    timeoutPolicy: ctx.step.timeoutPolicy ?? 'fail', deadlineUnixMs: ctx.deadline,
  }
}

function expandTemplate(
  template: string,
  variables: Readonly<Record<string, string | undefined>>
): { value: string; error?: string } {
  let error: string | undefined
  const value = template.replace(TEMPLATE_PATTERN, (_match, name: string) => {
    if (!TEMPLATE_VARIABLES.has(name)) { error = `未知模板变量 ${name}`; return '' }
    const replacement = variables[name]
    if (replacement === undefined || replacement === '') { error = `模板变量 ${name} 缺少值`; return '' }
    return replacement
  })
  if (!error && (value.includes('{{') || value.includes('}}'))) error = '模板表达式格式无效'
  return { value, error }
}

function probeEventType(signal: ScenarioActionSignal): string | undefined {
  if (signal.type !== 'probe') return signal.type
  return recordValue(signal.payload)?.eventType as string | undefined
}

function succeeded(result?: unknown): ScenarioActionResult { return { state: 'succeeded', result } }
function running(result?: unknown, message?: string): ScenarioActionResult { return { state: 'running', result, message } }
function failed(errorCode: string, message: string, result?: unknown): ScenarioActionResult { return { state: 'failed', errorCode, message, result } }
function invalidStep(message: string): ScenarioActionResult { return failed('ACTION_INTERNAL_ERROR', message) }

function numberField(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function positiveNumber(value: unknown, fallback: number): number {
  const parsed = numberField(value)
  return parsed !== undefined && parsed >= 0 ? parsed : fallback
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function positionField(value: unknown): ScenarioPosition | undefined {
  const record = recordValue(value)
  const x = numberField(record?.x)
  const y = numberField(record?.y)
  const z = numberField(record?.z)
  return x === undefined || y === undefined || z === undefined ? undefined : { x, y, z }
}

function intRange(value: unknown): { min: number; max: number } {
  const range = recordValue(value)
  const min = Math.floor(numberField(range?.min) ?? 0)
  const max = Math.floor(numberField(range?.max) ?? min)
  return { min, max }
}

function maxPathFailures(step: ScenarioStep): number { return Math.max(1, Math.floor(numberField(step.maxPathFailures) ?? 3)) }

function arrived(position: ScenarioPosition | undefined, target: ScenarioPosition, radius: number): boolean {
  return distance(position, target) <= radius
}

function distance(left: ScenarioPosition | undefined, right: ScenarioPosition): number {
  if (!left) return Number.POSITIVE_INFINITY
  return Math.hypot(left.x - right.x, left.y - right.y, left.z - right.z)
}

function isBarrierRelease(value: unknown): boolean {
  const release = recordValue(value)
  return release?.type === 'all' || release?.type === 'count' || release?.type === 'percent'
}

class DeterministicRandom {
  private state: number

  constructor(seed: string) { this.state = hashSeed(seed) }

  next(): number {
    this.state += 0x6D2B79F5
    let value = this.state
    value = Math.imul(value ^ value >>> 15, value | 1)
    value ^= value + Math.imul(value ^ value >>> 7, value | 61)
    return ((value ^ value >>> 14) >>> 0) / 4_294_967_296
  }

  int(min: number, max: number): number {
    if (max <= min) return min
    return min + Math.floor(this.next() * (max - min + 1))
  }
}

function hashSeed(value: string): number {
  let hash = 2_166_136_261
  for (let index = 0; index < value.length; index++) {
    hash ^= value.charCodeAt(index)
    hash = Math.imul(hash, 16_777_619)
  }
  return hash >>> 0
}
