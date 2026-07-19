import assert from 'node:assert/strict'
import test from 'node:test'

import { FleetController } from '../../dist/ipc/fleet.js'
import { FakeMcBot, scenario, step } from './helpers.mjs'

function createFleetHarness(overrides = {}) {
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
    createBehavior: overrides.createBehavior ?? ((_botId, name) => ({
      name,
      start() {},
      stop() {},
      setMcBot() {},
      async tick() {},
    })),
    signalRouter: overrides.signalRouter,
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

test('Mineflayer 场景适配只绑定固定监听器，移动目标不重复且路径失败可见', async () => {
  const harness = createFleetHarness()
  harness.fleet.handleCommand({
    cmd: 'create-bots', requestId: 'move-r', batchId: 'move-b', idempotencyKey: 'move-k',
    bots: [assignment('move', { scenario: scenario([
      step('spawn', 'wait_spawn'),
      step('move', 'move_to_and_wait', { pos: { x: 10, y: 64, z: 0 }, radius: 1, timeoutMs: 10_000 }),
    ]) })],
  })
  const bot = harness.bots[0]
  const listenerCount = bot.eventNames().reduce((total, name) => total + bot.listenerCount(name), 0)
  bot.emit('spawn')
  for (let index = 0; index < 20 && bot.pathfinderGoals.length === 0; index++) await harness.flush()
  assert.equal(bot.pathfinderGoals.length, 1)

  for (let index = 0; index < 1_000; index++) await harness.tick(1_000 + index)
  assert.equal(bot.pathfinderGoals.length, 1)
  assert.equal(bot.eventNames().reduce((total, name) => total + bot.listenerCount(name), 0), listenerCount)

  bot.emit('path_update', { status: 'noPath' })
  await harness.tick(2_100)
  assert.equal(actionEvents(harness.events, 'move').at(-1).action.errorCode, 'PATH_NOT_FOUND')
  harness.fleet.handleCommand({ cmd: 'stop-bots', requestId: 'move-stop', botIds: ['move'], generation: 3 })
  await harness.flush()
  assert.ok(bot.clearedGoals >= 1)
  assert.equal(bot.eventNames().reduce((total, name) => total + bot.listenerCount(name), 0), 0)
})

test('Mineflayer 实体与攻击能力适配锁定真实 entity id', async () => {
  const harness = createFleetHarness()
  harness.fleet.handleCommand({
    cmd: 'create-bots', requestId: 'attack-r', batchId: 'attack-b', idempotencyKey: 'attack-k',
    bots: [assignment('attack', { scenario: scenario([
      step('spawn', 'wait_spawn'),
      step('find', 'find_entity', { selector: { kind: 'hostile', types: ['zombie'], radius: 16, priority: 'nearest' } }),
      step('attack', 'attack_until', {
        selector: { kind: 'hostile', types: ['zombie'], radius: 16, priority: 'nearest' },
        attackIntervalMs: 500, chase: false, reacquire: true,
        stop: { durationMs: 2_000, damageAtLeast: 10, successPolicy: 'any' },
      }),
    ]) })],
  })
  const bot = harness.bots[0]
  bot.entities.zombie = {
    id: 35, kind: 'Hostile mobs', mobType: 'zombie', name: 'zombie', health: 20,
    position: { x: 2, y: 64, z: 2 },
  }
  bot.emit('spawn')
  await harness.flush()
  await harness.flush()
  assert.deepEqual(bot.attacks, [35])
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

test('Fleet 解包 runDeadlineUnixMs 传给 Runner，旧 envelope 无 deadline 继续兼容', async () => {
  const deadline = createFleetHarness()
  const cohort = scenario([step('wait', 'wait', { durationMs: 500, timeoutMs: 1_000 })])
  deadline.fleet.handleCommand({ cmd: 'create-bots', bots: [assignment('deadline', {
    scenario: { seed: 42, botOrdinal: 1, runDeadlineUnixMs: 1_100, scenario: cohort },
  })] })
  await deadline.flush()
  await deadline.tick(1_100)
  assert.equal(actionEvents(deadline.events, 'deadline').at(-1).action.status, 'timed_out')

  const legacy = createFleetHarness()
  legacy.fleet.handleCommand({ cmd: 'create-bots', bots: [assignment('legacy-envelope', {
    scenario: { seed: 42, botOrdinal: 1, scenario: cohort },
  })] })
  await legacy.flush()
  await legacy.tick(1_500)
  assert.equal(actionEvents(legacy.events, 'legacy-envelope').at(-1).action.status, 'succeeded')
})

test('V1 legacy_behavior 通过 Fleet adapter 复用旧行为与 custom 边界', async () => {
  const cases = [
    ['follow', 'PlayerOne', undefined],
    ['patrol', '0,64,0;10,64,0', undefined],
    ['guard', '1,64,1', undefined],
    ['roam', '', undefined],
    ['custom', '', { type: 'interact', pos: { x: 1, y: 64, z: 1 } }],
    ['custom', '', { type: 'use_item' }],
  ]
  for (const [behavior, target, legacyStep] of cases) {
    const calls = []
    const harness = createFleetHarness({
      createBehavior: (_botId, name, config) => {
        const call = { name, config, ticks: 0, stopped: 0, bound: 0 }
        calls.push(call)
        return {
          name,
          start() {},
          stop() { call.stopped++ },
          setMcBot() { call.bound++ },
          async tick() { call.ticks++ },
        }
      },
    })
    harness.fleet.handleCommand({ cmd: 'create-bots', requestId: `r-${behavior}-${legacyStep?.type ?? ''}`, batchId: 'b', idempotencyKey: `k-${behavior}-${legacyStep?.type ?? ''}`, bots: [assignment(`legacy-${behavior}-${legacyStep?.type ?? 'behavior'}`, {
      cohortKey: 'legacy',
      scenario: scenario([step('legacy-action', 'legacy_behavior', {
        behavior, target, durationMs: 1_000, step: legacyStep, timeoutMs: 2_000,
      })], 'legacy'),
    })] })
    await harness.flush()
    await harness.tick(1_500)
    await harness.tick(2_000)

    const legacyCall = calls.at(-1)
    assert.equal(legacyCall.name, behavior)
    assert.ok(legacyCall.ticks > 0)
    assert.ok(legacyCall.bound > 0)
    if (legacyStep) assert.deepEqual(legacyCall.config, { steps: [legacyStep] })
    assert.equal(actionEvents(harness.events, `legacy-${behavior}-${legacyStep?.type ?? 'behavior'}`).some((event) => event.action.errorCode === 'ACTION_INTERNAL_ERROR'), false)
  }
})

test('Fleet-managed Scenario Bot 拒绝旧 run-script 且 move 不覆盖 pathfinder', async () => {
  const harness = createFleetHarness()
  harness.fleet.handleCommand({ cmd: 'create-bots', requestId: 'fleet-create', batchId: 'fleet-batch', idempotencyKey: 'fleet-key', bots: [assignment('managed-script', {
    scenario: scenario([step('wait', 'wait', { durationMs: 10_000 })]),
  })] })
  await harness.flush()

  const handled = harness.fleet.handleCommand({
    cmd: 'run-script', scriptId: 'legacy-move', botIds: ['managed-script'],
    steps: [{ action: 'move', pos: { x: 100, y: 64, z: 100 } }],
  })
  assert.equal(handled, true)
  assert.equal(harness.bots[0].pathfinderGoals.length, 0)
  const error = harness.events.findLast((event) => event.evt === 'bot-error')
  assert.equal(error.errorCode, 'fleet_managed')
  assert.match(error.error, /Fleet 场景/)
})

test('signal-actions 超过 100 条整批拒绝且不进入 Promise.all 路由', async () => {
  let routed = 0
  const harness = createFleetHarness({ signalRouter: (signal) => {
    routed++
    return { signalId: signal.signalId, accepted: true, skipped: false, status: 'accepted' }
  } })
  harness.fleet.handleCommand({ cmd: 'create-bots', bots: [assignment('signal-limit')] })
  const signals = Array.from({ length: 101 }, (_, index) => ({
    signalId: `signal-${index}`, botId: 'signal-limit', sessionId: 'run-1', generation: 3,
    actionRunId: 'action', stepId: 'step', type: 'probe',
  }))

  harness.fleet.handleCommand({ cmd: 'signal-actions', requestId: 'too-many', signals })
  await harness.flush()
  const result = harness.events.findLast((event) => event.evt === 'signal-result')
  assert.equal(routed, 0)
  assert.equal(result.signalResults.length, 101)
  assert.ok(result.signalResults.every((item) => item.errorCode === 'batch_limit_exceeded'))
})

test('end/kicked 仅清理当前实例重资源，保留容量账本且替换不释放新实例', async () => {
  for (const eventName of ['end', 'kicked']) {
    const harness = createFleetHarness()
    const first = assignment(`ended-${eventName}`, {
      scenario: scenario([step('move', 'move_to_and_wait', { pos: { x: 10, y: 64, z: 0 }, radius: 1, timeoutMs: 10_000 })]),
    })
    harness.fleet.handleCommand({ cmd: 'create-bots', requestId: `r-${eventName}`, batchId: 'b', idempotencyKey: `k-${eventName}`, bots: [first] })
    const oldBot = harness.bots[0]
    if (eventName === 'kicked') oldBot.emit(eventName, 'bye')
    else oldBot.emit(eventName)
    await harness.flush()
    await harness.flush()

    assert.equal(oldBot.eventNames().reduce((total, name) => total + oldBot.listenerCount(name), 0), 0)
    assert.ok(oldBot.clearedGoals >= 1)
    assert.equal(harness.fleet.metrics().activeBots, 1)
    assert.equal(harness.fleet.snapshots()[0].status, 'disconnected')

    harness.fleet.handleCommand({ cmd: 'create-bots', requestId: `replace-${eventName}`, batchId: 'replace', idempotencyKey: `replace-${eventName}`, bots: [{
      ...first, generation: 4, configHash: `new-${eventName}`,
    }] })
    await harness.flush()
    const newBot = harness.bots[1]
    oldBot.emit('end')
    newBot.emit('spawn')
    await harness.flush()
    assert.equal(harness.fleet.snapshots()[0].generation, 4)
    assert.equal(harness.fleet.snapshots()[0].status, 'connected')
    assert.equal(newBot.listenerCount('spawn') > 0, true)
  }
})
