import { EventEmitter } from 'node:events'

export class FakeScenarioCapabilities {
  nowMs = 1_000
  spawned = false
  endedReason = undefined
  chats = []
  clearGoalCount = 0
  chatError = null

  now = () => this.nowMs
  isSpawned = () => this.spawned
  connectionEndReason = () => this.endedReason

  chat = (message) => {
    if (this.chatError) throw this.chatError
    this.chats.push(message)
  }

  clearPathfinderGoal = () => {
    this.clearGoalCount++
  }

  advance(ms) {
    this.nowMs += ms
  }
}

export class FakeMcBot extends EventEmitter {
  username = 'Bot_1'
  health = 20
  food = 20
  entity = { position: { x: 1, y: 64, z: 2 } }
  chats = []
  quitCount = 0
  clearedGoals = 0
  pathfinder = {
    setGoal: (goal) => {
      if (goal === null) this.clearedGoals++
    },
  }

  quit() {
    this.quitCount++
  }

  chat(message) {
    this.chats.push(message)
  }
}

export function scenario(steps, key = 'combat') {
  return { key, percent: 100, steps }
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
