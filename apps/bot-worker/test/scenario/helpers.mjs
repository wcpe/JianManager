import { EventEmitter } from 'node:events'

export class FakeScenarioCapabilities {
  nowMs = 1_000
  spawned = false
  endedReason = undefined
  chats = []
  clearGoalCount = 0
  chatError = null
  position = { x: 0, y: 64, z: 0 }
  pathfinderAvailable = true
  pathfinderSetError = null
  pathfinderGoalCalls = []
  pathfinderEventsState = { goalReached: 0, pathFailed: 0 }
  entityValues = []
  attackCalls = []
  respawnCalls = 0
  dead = false
  spawnEventSequence = 0

  now = () => this.nowMs
  isSpawned = () => this.spawned
  connectionEndReason = () => this.endedReason
  getPosition = () => this.position ? { ...this.position } : undefined
  pathfinderEvents = () => ({ ...this.pathfinderEventsState })
  entities = () => this.entityValues.map((entity) => structuredClone(entity))
  isDead = () => this.dead
  spawnEventSeq = () => this.spawnEventSequence

  chat = (message) => {
    if (this.chatError) throw this.chatError
    this.chats.push(message)
  }

  setPathfinderGoal = async (goal) => {
    if (!this.pathfinderAvailable) return { status: 'unavailable' }
    if (this.pathfinderSetError) return { status: 'failed', message: this.pathfinderSetError }
    this.pathfinderGoalCalls.push(structuredClone(goal))
    return { status: 'set' }
  }

  clearPathfinderGoal = () => {
    this.clearGoalCount++
  }

  attack = (entityId) => {
    this.attackCalls.push({ entityId, at: this.nowMs })
    return true
  }

  respawn = () => {
    this.respawnCalls++
  }

  advance(ms) {
    this.nowMs += ms
  }

  failPath(count = 1) {
    this.pathfinderEventsState.pathFailed += count
  }

  reachGoal(count = 1) {
    this.pathfinderEventsState.goalReached += count
  }

  spawn() {
    this.dead = false
    this.spawned = true
    this.spawnEventSequence++
  }
}

export class FakeMcBot extends EventEmitter {
  username = 'Bot_1'
  health = 20
  food = 20
  entity = { id: 0, position: { x: 1, y: 64, z: 2 } }
  entities = {}
  chats = []
  quitCount = 0
  clearedGoals = 0
  pathfinderGoals = []
  attacks = []
  respawnCount = 0
  pathfinder = {
    setGoal: (goal) => {
      if (goal === null) this.clearedGoals++
      else this.pathfinderGoals.push(goal)
    },
  }

  quit() {
    this.quitCount++
  }

  chat(message) {
    this.chats.push(message)
  }

  attack(entity) {
    this.attacks.push(entity.id)
  }

  respawn() {
    this.respawnCount++
  }
}

export function scenario(steps, key = 'combat', runtime = {}) {
  return { key, percent: 100, seed: 20260719, botOrdinal: 1, steps, ...runtime }
}

export function step(id, type, fields = {}) {
  return {
    id,
    type,
    timeoutMs: 1_000,
    maxAttempts: 1,
    retryBackoffMs: 0,
    resumePolicy: 'restart_step',
    ...fields,
  }
}

export function runnerOptions(overrides = {}) {
  const capabilities = overrides.capabilities ?? new FakeScenarioCapabilities()
  const events = []
  let uuidCounter = 1
  const options = {
    botId: 'bot-1',
    botName: 'BotOne',
    username: 'BotOne',
    runId: 'run-1',
    generation: 3,
    cohortKey: 'combat',
    correlationSeed: 'seed-1',
    scenario: scenario([step('wait', 'wait', { durationMs: 100 })]),
    capabilities,
    emitActionEvent: (event) => events.push(structuredClone(event)),
    nextActionRunId: () => `00000000-0000-4000-8000-${String(uuidCounter++).padStart(12, '0')}`,
    ...overrides,
  }
  return { options, capabilities, events }
}

export function latest(events, status) {
  return events.findLast((event) => event.status === status)
}
