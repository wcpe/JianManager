import assert from 'node:assert/strict'
import test from 'node:test'

import { ScenarioRunner } from '../../dist/scenario/runner.js'
import { runnerOptions, scenario, step } from './helpers.mjs'

let signalCounter = 0
function signalFor(event, type, payload = {}, observedAt) {
  signalCounter++
  return {
    signalId: `combat-signal-${signalCounter}`,
    botId: 'bot-1', sessionId: 'run-1', generation: 3,
    actionRunId: event.actionRunId, stepId: event.stepId,
    correlationToken: event.correlationToken, type, payload, observedAt,
  }
}

function attackStep(fields = {}) {
  return step('attack', 'attack_until', {
    selector: { kind: 'hostile', types: ['zombie'], radius: 20, priority: 'nearest' },
    attackIntervalMs: 600,
    chase: true,
    reacquire: true,
    targetNotFoundTimeoutMs: 1_000,
    stop: { durationMs: 5_000, damageAtLeast: 100, successPolicy: 'any' },
    timeoutMs: 10_000,
    ...fields,
  })
}

function zombie(id, x, health = 20) {
  return { id, kind: 'hostile', type: 'zombie', name: `zombie-${id}`, health, dead: false, position: { x, y: 64, z: 0 } }
}

test('attack_until 锁定、必要追击并按 attackIntervalMs 攻击', async () => {
  const run = runnerOptions({ scenario: scenario([attackStep()]) })
  run.capabilities.entityValues = [zombie(1, 6)]
  const runner = new ScenarioRunner(run.options)
  await runner.start()
  assert.equal(run.capabilities.pathfinderGoalCalls.length, 1)
  assert.equal(run.capabilities.attackCalls.length, 0)

  run.capabilities.position = { x: 3, y: 64, z: 0 }
  await runner.tick(run.capabilities.now())
  assert.equal(run.capabilities.attackCalls.length, 1)
  run.capabilities.advance(599)
  await runner.tick(run.capabilities.now())
  assert.equal(run.capabilities.attackCalls.length, 1)
  run.capabilities.advance(1)
  await runner.tick(run.capabilities.now())
  assert.equal(run.capabilities.attackCalls.length, 2)
  assert.equal(run.capabilities.pathfinderGoalCalls.length, 1)
})

test('attack_until 目标死亡后按 reacquire 重选，关闭重选时空窗后 TARGET_NOT_FOUND', async () => {
  const reacquire = runnerOptions({ scenario: scenario([attackStep({ attackIntervalMs: 100, chase: false })]) })
  reacquire.capabilities.entityValues = [zombie(1, 2), zombie(2, 4)]
  const reacquireRunner = new ScenarioRunner(reacquire.options)
  await reacquireRunner.start()
  assert.equal(reacquire.capabilities.attackCalls[0].entityId, 1)
  reacquire.capabilities.entityValues[0].dead = true
  reacquire.capabilities.advance(100)
  await reacquireRunner.tick(reacquire.capabilities.now())
  assert.equal(reacquire.capabilities.attackCalls.at(-1).entityId, 2)

  const noReacquire = runnerOptions({
    scenario: scenario([attackStep({ attackIntervalMs: 100, chase: false, reacquire: false, targetNotFoundTimeoutMs: 500 })]),
  })
  noReacquire.capabilities.entityValues = [zombie(1, 2)]
  const noReacquireRunner = new ScenarioRunner(noReacquire.options)
  await noReacquireRunner.start()
  assert.equal(noReacquire.capabilities.attackCalls[0].entityId, 1)
  noReacquire.capabilities.entityValues[0].dead = true
  await noReacquireRunner.tick(noReacquire.capabilities.now())
  noReacquire.capabilities.advance(499)
  await noReacquireRunner.tick(noReacquire.capabilities.now())
  assert.equal(noReacquire.events.at(-1).status, 'running')
  noReacquire.capabilities.advance(1)
  await noReacquireRunner.tick(noReacquire.capabilities.now())
  assert.equal(noReacquire.events.at(-1).errorCode, 'TARGET_NOT_FOUND')
})

test('attack_until 实际攻击前目标消失时保持空窗并可重新锁定', async () => {
  const run = runnerOptions({ scenario: scenario([attackStep({ attackIntervalMs: 100, chase: false })]) })
  run.capabilities.entityValues = [zombie(1, 2)]
  const attack = run.capabilities.attack
  run.capabilities.attack = () => {
    run.capabilities.entityValues = []
    return false
  }
  const runner = new ScenarioRunner(run.options)
  await runner.start()
  assert.equal(run.events.at(-1).status, 'running')
  assert.equal(run.events.at(-1).result.clientAttackAttempts, 0)

  run.capabilities.attack = attack
  run.capabilities.entityValues = [zombie(2, 2)]
  run.capabilities.advance(100)
  await runner.tick(run.capabilities.now())
  assert.equal(run.capabilities.attackCalls.at(-1).entityId, 2)
})

test('attack_until 仅用完整关联可信信号满足 any/all，不把 client attack 计成功', async () => {
  for (const policy of ['any', 'all']) {
    const run = runnerOptions({
      scenario: scenario([attackStep({
        chase: false,
        stop: { durationMs: 5_000, damageAtLeast: 5, killsAtLeast: 1, successPolicy: policy },
      })]),
    })
    run.capabilities.entityValues = [zombie(1, 2)]
    const runner = new ScenarioRunner(run.options)
    await runner.start()
    const running = run.events[0]
    assert.equal(run.capabilities.attackCalls.length, 1)
    assert.equal(run.events.filter((event) => event.status === 'succeeded').length, 0)

    const bad = await runner.signal({ ...signalFor(running, 'damage_dealt', { damage: 5 }), correlationToken: 'wrong' })
    assert.equal(bad.skipped, true)
    await runner.signal(signalFor(running, 'kill', { count: 1 }))
    if (policy === 'any') {
      assert.equal(run.events.at(-1).status, 'succeeded')
    } else {
      assert.equal(run.events.at(-1).status, 'running')
      await runner.signal(signalFor(running, 'damage_dealt', { damage: 5 }))
      assert.equal(run.events.at(-1).status, 'succeeded')
    }
  }
})

test('内部 legacy attack 在 duration 截止时成功且不改变公开 V2 规则', async () => {
  const legacy = runnerOptions({
    scenario: scenario([attackStep({
      chase: false,
      targetNotFoundTimeoutMs: 5_000,
      legacyDurationSuccess: true,
      stop: { durationMs: 1_000, successPolicy: 'any' },
    })]),
  })
  const runner = new ScenarioRunner(legacy.options)
  await runner.start()
  legacy.capabilities.advance(1_000)
  await runner.tick(legacy.capabilities.now())
  assert.equal(legacy.events.at(-1).status, 'succeeded')
  assert.equal(legacy.events.at(-1).errorCode, undefined)
})

test('attack_until 支持 probeEvent，duration 截止不自动成功且返回 ATTACK_ASSERTION_UNMET', async () => {
  const probe = runnerOptions({
    scenario: scenario([attackStep({ chase: false, stop: { durationMs: 5_000, probeEvent: 'boss_hit', successPolicy: 'all' } })]),
  })
  probe.capabilities.entityValues = [zombie(1, 2)]
  const probeRunner = new ScenarioRunner(probe.options)
  await probeRunner.start()
  await probeRunner.signal(signalFor(probe.events[0], 'probe', { eventType: 'boss_hit' }))
  assert.equal(probe.events.at(-1).status, 'succeeded')

  const damageProbe = runnerOptions({
    scenario: scenario([attackStep({ chase: false, stop: { durationMs: 5_000, probeEvent: 'damage', successPolicy: 'all' } })]),
  })
  damageProbe.capabilities.entityValues = [zombie(1, 2)]
  const damageProbeRunner = new ScenarioRunner(damageProbe.options)
  await damageProbeRunner.start()
  await damageProbeRunner.signal(signalFor(damageProbe.events[0], 'damage', { damage: 1 }))
  assert.equal(damageProbe.events.at(-1).status, 'succeeded')

  const unmet = runnerOptions({
    scenario: scenario([attackStep({ chase: false, stop: { durationMs: 1_000, damageAtLeast: 2, successPolicy: 'all' } })]),
  })
  unmet.capabilities.entityValues = [zombie(1, 2)]
  const unmetRunner = new ScenarioRunner(unmet.options)
  await unmetRunner.start()
  unmet.capabilities.advance(1_000)
  await unmetRunner.tick(unmet.capabilities.now())
  assert.equal(unmet.events.at(-1).status, 'failed')
  assert.equal(unmet.events.at(-1).errorCode, 'ATTACK_ASSERTION_UNMET')
})

test('attack_until evidence windows 对齐 observation-start，complete 检查全部完整窗口', async () => {
  for (const complete of [true, false]) {
    const run = runnerOptions({
      scenario: scenario([attackStep({
        chase: false,
        stop: {
          durationMs: 4_000,
          evidenceWindowMs: 1_000,
          minDamageEventsPerWindow: 1,
          successPolicy: 'all',
        },
      })]),
    })
    run.capabilities.entityValues = [zombie(1, 2)]
    const runner = new ScenarioRunner(run.options)
    await runner.start()
    const running = run.events[0]
    await runner.signal(signalFor(running, 'observation-start', { observationStartUnixMs: 1_000 }, 1_000))
    await runner.signal(signalFor(running, 'damage_dealt', { damage: 1, occurredAtUnixMs: 1_100 }, 1_100))
    if (complete) {
      await runner.signal(signalFor(running, 'damage_dealt', { damage: 1, occurredAtUnixMs: 2_100 }, 2_100))
    }
    await runner.signal(signalFor(running, 'observation-complete', { observationCompleteUnixMs: 3_000 }, 3_000))
    assert.equal(run.events.at(-1).status, complete ? 'succeeded' : 'failed')
    if (!complete) assert.equal(run.events.at(-1).errorCode, 'ATTACK_ASSERTION_UNMET')
  }
})

test('attack_until 可信击杀计入窗口且 observation-complete 前不提前成功', async () => {
  const run = runnerOptions({
    scenario: scenario([attackStep({
      chase: false,
      stop: {
        durationMs: 4_000,
        killsAtLeast: 1,
        evidenceWindowMs: 1_000,
        minDamageEventsPerWindow: 1,
        successPolicy: 'any',
      },
    })]),
  })
  run.capabilities.entityValues = [zombie(1, 2)]
  const runner = new ScenarioRunner(run.options)
  await runner.start()
  const running = run.events[0]

  await runner.signal(signalFor(running, 'observation-start', { observationStartUnixMs: 1_000 }, 1_000))
  await runner.signal(signalFor(running, 'kill', { count: 1, occurredAtUnixMs: 1_100 }, 1_100))
  assert.equal(run.events.at(-1).status, 'running')
  await runner.signal(signalFor(running, 'entity_killed', { count: 1, occurredAtUnixMs: 2_100 }, 2_100))
  assert.equal(run.events.at(-1).status, 'running')
  await runner.signal(signalFor(running, 'observation-complete', { observationCompleteUnixMs: 3_000 }, 3_000))
  assert.equal(run.events.at(-1).status, 'succeeded')
})

test('attack_until 死亡后只请求一次重生，新 spawn 后恢复同一动作', async () => {
  const run = runnerOptions({
    scenario: scenario([attackStep({
      chase: false,
      stop: { durationMs: 1_000, minClientAttackAttempts: 2, successPolicy: 'any' },
      respawn: { maxAttempts: 2, retryBackoffMs: 0, timeoutMs: 1_000 },
    })]),
  })
  run.capabilities.entityValues = [zombie(1, 2)]
  const runner = new ScenarioRunner(run.options)
  await runner.start()
  assert.equal(run.capabilities.attackCalls.length, 1)

  run.capabilities.dead = true
  run.capabilities.advance(100)
  await runner.tick(run.capabilities.now())
  await runner.tick(run.capabilities.now())
  assert.equal(run.capabilities.respawnCalls, 1)
  assert.equal(run.capabilities.attackCalls.length, 1)

  run.capabilities.spawn()
  run.capabilities.advance(500)
  await runner.tick(run.capabilities.now())
  assert.equal(run.capabilities.attackCalls.length, 2)

  run.capabilities.advance(500)
  await runner.tick(run.capabilities.now())
  assert.equal(run.events.at(-1).status, 'succeeded')
  assert.equal(run.events.at(-1).result.respawnCount, 1)
})

test('attack_until 支持稳定随机目标与 searchArea 搜敌', async () => {
  const fields = {
    chase: false,
    selector: { types: ['zombie'], radius: 20, priority: 'random' },
    stop: { durationMs: 1_000, minClientAttackAttempts: 1, successPolicy: 'any' },
  }
  const first = runnerOptions({ scenario: scenario([attackStep(fields)]) })
  const second = runnerOptions({ scenario: scenario([attackStep(fields)]) })
  first.capabilities.entityValues = [zombie(1, 2), zombie(2, 2), zombie(3, 2)]
  second.capabilities.entityValues = [zombie(3, 2), zombie(1, 2), zombie(2, 2)]
  await new ScenarioRunner(first.options).start()
  await new ScenarioRunner(second.options).start()
  assert.equal(first.capabilities.attackCalls[0].entityId, second.capabilities.attackCalls[0].entityId)

  const search = runnerOptions({
    scenario: scenario([attackStep({
      chase: true,
      selector: { types: ['zombie'], radius: 20, priority: 'nearest' },
      searchArea: { type: 'radius', center: { x: 0, y: 64, z: 0 }, radius: 8 },
      targetNotFoundTimeoutMs: 10,
      stop: { durationMs: 1_000, minClientAttackAttempts: 1, successPolicy: 'any' },
    })]),
  })
  const runner = new ScenarioRunner(search.options)
  await runner.start()
  search.capabilities.advance(20)
  await runner.tick(search.capabilities.now())
  assert.equal(search.events.at(-1).status, 'running')
  assert.ok(search.capabilities.pathfinderGoalCalls.length >= 1)

  search.capabilities.entityValues = [zombie(9, 2)]
  search.capabilities.advance(600)
  await runner.tick(search.capabilities.now())
  assert.equal(search.capabilities.attackCalls.at(-1).entityId, 9)
})

test('cancel 后攻击、追击与动作资源全部停止', async () => {
  const run = runnerOptions({ scenario: scenario([attackStep({ attackIntervalMs: 100 })]) })
  run.capabilities.entityValues = [zombie(1, 6)]
  const runner = new ScenarioRunner(run.options)
  await runner.start()
  const goals = run.capabilities.pathfinderGoalCalls.length
  const attacks = run.capabilities.attackCalls.length
  await runner.cancel('停止测试')
  run.capabilities.advance(1_000)
  await runner.tick(run.capabilities.now())
  assert.equal(run.capabilities.pathfinderGoalCalls.length, goals)
  assert.equal(run.capabilities.attackCalls.length, attacks)
  assert.equal(run.capabilities.clearGoalCount, 1)
})

test('长动作保留完整已接受 signalId，1001 和多千信号后重放首条不重复计数', async () => {
  for (const count of [1_001, 3_000]) {
    const run = runnerOptions({
      scenario: scenario([attackStep({
        chase: false, targetNotFoundTimeoutMs: 60_000,
        stop: { durationMs: 60_000, damageAtLeast: count + 1, successPolicy: 'all' },
        timeoutMs: 120_000,
      })]),
    })
    const runner = new ScenarioRunner(run.options)
    await runner.start()
    const running = run.events[0]
    let first
    for (let index = 0; index < count; index++) {
      const signal = signalFor(running, 'damage', { damage: 1 })
      if (index === 0) first = signal
      const receipt = await runner.signal(signal)
      assert.equal(receipt.accepted, true)
    }

    const replay = await runner.signal(first)
    assert.equal(replay.skipped, true)
    assert.equal(run.events.some((event) => event.status === 'succeeded'), false)
    await runner.signal(signalFor(running, 'damage', { damage: 1 }))
    assert.equal(run.events.at(-1).status, 'succeeded')
    assert.equal(run.events.at(-1).result.trustedDamage, count + 1)
  }
})
