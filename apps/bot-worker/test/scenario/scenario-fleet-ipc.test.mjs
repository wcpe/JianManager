import assert from 'node:assert/strict'
import test from 'node:test'

import { FleetController } from '../../dist/ipc/fleet.js'
import { FakeMcBot, scenario, step } from './helpers.mjs'

function createFleetHarness() {
  const events = []
  const bots = []
  const intervals = []
  let now = 1_000
  const fleet = new FleetController({
    maxBots: 50,
    workerEpoch: 'epoch-1',
    workerEpochGeneration: 7,
    sendEvent: (event) => events.push(structuredClone(event)),
    createBot: () => {
      const bot = new FakeMcBot()
      bots.push(bot)
      return bot
    },
    createBehavior: (_botId, name) => ({
      name,
      start() {},
      stop() {},
      setMcBot() {},
      async tick() {},
    }),
    now: () => now,
    setInterval: (callback) => {
      intervals.push(callback)
      return callback
    },
    clearInterval: (callback) => {
      const index = intervals.indexOf(callback)
      if (index >= 0) intervals.splice(index, 1)
    },
  })
  return {
    fleet, events, bots, intervals,
    setNow(value) { now = value },
    async tick(value = now) { now = value; await fleet.tick(value) },
    async flush() { await new Promise((resolve) => setImmediate(resolve)) },
  }
}

function assignment(id, overrides = {}) {
  return {
    id, name: id, host: '127.0.0.1', port: 25565,
    sessionId: 'run-1', generation: 3, configHash: `hash-${id}`, cohortKey: 'combat',
    correlationSeed: `seed-${id}`,
    ...overrides,
  }
}

function actionEvents(events, botId) {
  return events.filter((event) => event.evt === 'action-event' && event.action.botId === botId)
}

test('Apply 有 scenario 时创建 Runner，无 scenario 保持旧 V1 行为', async () => {
  const harness = createFleetHarness()
  harness.fleet.handleCommand({ cmd: 'create-bots', bots: [
    assignment('scenario', { scenario: scenario([step('spawn', 'wait_spawn')]) }),
    assignment('legacy'),
  ] })
  await harness.flush()

  assert.equal(actionEvents(harness.events, 'scenario')[0].action.status, 'running')
  assert.equal(actionEvents(harness.events, 'legacy').length, 0)
  assert.equal(harness.intervals.length, 1)
})

test('replace/stop/child end 会 cancel dispose 场景且保留 ownership/generation 边界', async () => {
  const harness = createFleetHarness()
  const first = assignment('managed', { scenario: scenario([step('wait', 'wait', { durationMs: 10_000 })]) })
  harness.fleet.handleCommand({ cmd: 'create-bots', requestId: 'r1', batchId: 'b1', idempotencyKey: 'k1', bots: [first] })
  await harness.flush()
  harness.fleet.handleCommand({
    cmd: 'create-bots', requestId: 'r2', batchId: 'b2', idempotencyKey: 'k2',
    bots: [{ ...first, generation: 4, configHash: 'hash-new' }],
  })
  await harness.flush()
  assert.equal(actionEvents(harness.events, 'managed').filter((event) => event.action.status === 'cancelled').length, 1)

  harness.fleet.handleCommand({ cmd: 'stop-bots', requestId: 'stop-old', botIds: ['managed'], generation: 3 })
  assert.equal(harness.fleet.metrics().activeBots, 1)
  harness.fleet.handleCommand({ cmd: 'stop-bots', requestId: 'stop-new', botIds: ['managed'], generation: 4 })
  await harness.flush()
  assert.equal(harness.fleet.metrics().activeBots, 0)
  assert.equal(harness.intervals.length, 0)

  harness.fleet.handleCommand({
    cmd: 'create-bots', requestId: 'r3', batchId: 'b3', idempotencyKey: 'k3',
    bots: [assignment('ended', { scenario: scenario([step('spawn', 'wait_spawn')]) })],
  })
  harness.bots.at(-1).emit('end')
  await harness.flush()
  await harness.flush()
  assert.equal(actionEvents(harness.events, 'ended').at(-1).action.errorCode, 'CONNECT_ENDED')
})

test('signal-actions 强关联逐项回执，重复 signalId 幂等且终态后安全 skip', async () => {
  const harness = createFleetHarness()
  harness.fleet.handleCommand({
    cmd: 'create-bots', requestId: 'r1', batchId: 'b1', idempotencyKey: 'k1',
    bots: [assignment('probe', { scenario: scenario([step('probe', 'wait_probe_event', { event: 'room_joined' })]) })],
  })
  await harness.flush()
  const running = actionEvents(harness.events, 'probe')[0].action
  const base = {
    signalId: 'signal-1', botId: 'probe', sessionId: 'run-1', generation: 3,
    actionRunId: running.actionRunId, stepId: 'probe', correlationToken: running.correlationToken,
    type: 'probe', payload: { eventType: 'room_joined' },
  }
  harness.fleet.handleCommand({ cmd: 'signal-actions', requestId: 'signals', signals: [
    { ...base, signalId: 'wrong-run', sessionId: 'wrong' },
    { ...base, signalId: 'wrong-token', correlationToken: 'wrong' },
    base,
    base,
  ] })
  await harness.flush()
  await harness.flush()
  const result = harness.events.findLast((event) => event.evt === 'signal-result')
  assert.deepEqual(result.signalResults.map((item) => [item.accepted, item.skipped, item.errorCode ?? '']), [
    [false, true, 'association_mismatch'],
    [false, true, 'association_mismatch'],
    [true, false, ''],
    [true, true, ''],
  ])

  harness.fleet.handleCommand({ cmd: 'signal-actions', requestId: 'late', signals: [{ ...base, signalId: 'late' }] })
  await harness.flush()
  await harness.flush()
  assert.equal(harness.events.findLast((event) => event.evt === 'signal-result').signalResults[0].skipped, true)
})

test('50 Bot 长循环只复用一个集中 scheduler', async () => {
  const harness = createFleetHarness()
  const assignments = Array.from({ length: 50 }, (_, index) => assignment(`bot-${index}`, {
    scenario: scenario([step('wait', 'wait', { durationMs: 60_000, timeoutMs: 120_000 })]),
  }))
  harness.fleet.handleCommand({ cmd: 'create-bots', bots: assignments })
  await harness.flush()

  assert.equal(harness.fleet.metrics().activeBots, 50)
  assert.equal(harness.intervals.length, 1)
  for (let index = 0; index < 1_000; index++) await harness.tick(1_000 + index)
  assert.equal(harness.intervals.length, 1)
})
