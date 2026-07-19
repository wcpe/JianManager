import assert from 'node:assert/strict'
import test from 'node:test'

import { ScenarioRunner } from '../../dist/scenario/runner.js'
import { runnerOptions, scenario, step } from './helpers.mjs'

let signalCounter = 0
function signalFor(event, overrides = {}) {
  signalCounter++
  return {
    signalId: overrides.signalId ?? `move-signal-${signalCounter}`,
    botId: 'bot-1',
    sessionId: 'run-1',
    generation: 3,
    actionRunId: event.actionRunId,
    stepId: event.stepId,
    correlationToken: event.correlationToken,
    type: 'probe',
    payload: {},
    ...overrides,
  }
}

test('roam radius 使用 seed+botOrdinal+stepId 复现且目标不越界', async () => {
  const roam = step('roam', 'roam_in_area', {
    durationMs: 10_000,
    area: { type: 'radius', center: { x: 10, y: 64, z: -5 }, radius: 8 },
    pauseMs: { min: 500, max: 500 },
  })
  const first = runnerOptions({ scenario: scenario([roam], 'combat', { seed: 42, botOrdinal: 17 }) })
  const second = runnerOptions({ scenario: scenario([roam], 'combat', { seed: 42, botOrdinal: 17 }) })
  await new ScenarioRunner(first.options).start()
  await new ScenarioRunner(second.options).start()

  assert.deepEqual(first.capabilities.pathfinderGoalCalls, second.capabilities.pathfinderGoalCalls)
  const target = first.capabilities.pathfinderGoalCalls[0].position
  assert.ok(Math.hypot(target.x - 10, target.z + 5) <= 8)
  assert.equal(target.y, 64)
})

test('roam 仅在新目标或路径失败时 setGoal，抵达暂停后继续且三次失败才终态', async () => {
  const roamStep = step('roam', 'roam_in_area', {
    durationMs: 10_000,
    area: { type: 'radius', center: { x: 0, y: 64, z: 0 }, radius: 10 },
    pauseMs: { min: 500, max: 500 },
    maxPathFailures: 3,
  })
  const run = runnerOptions({ scenario: scenario([roamStep]) })
  const runner = new ScenarioRunner(run.options)
  await runner.start()
  await runner.tick(run.capabilities.now())
  assert.equal(run.capabilities.pathfinderGoalCalls.length, 1)

  run.capabilities.failPath()
  await runner.tick(run.capabilities.now())
  assert.equal(run.events.at(-1).status, 'running')
  assert.equal(run.capabilities.pathfinderGoalCalls.length, 2)

  run.capabilities.position = { ...run.capabilities.pathfinderGoalCalls.at(-1).position }
  await runner.tick(run.capabilities.now())
  run.capabilities.advance(499)
  await runner.tick(run.capabilities.now())
  assert.equal(run.capabilities.pathfinderGoalCalls.length, 2)
  run.capabilities.advance(1)
  await runner.tick(run.capabilities.now())
  assert.equal(run.capabilities.pathfinderGoalCalls.length, 3)

  for (let count = 1; count <= 3; count++) {
    run.capabilities.failPath()
    await runner.tick(run.capabilities.now())
    if (count < 3) assert.equal(run.events.at(-1).status, 'running')
  }
  assert.equal(run.events.at(-1).status, 'failed')
  assert.equal(run.events.at(-1).errorCode, 'PATH_NOT_FOUND')
})

test('roam waypoints 按确定性起点稳定循环', async () => {
  const waypoints = [
    { x: 1, y: 64, z: 1 },
    { x: 2, y: 64, z: 2 },
    { x: 3, y: 64, z: 3 },
  ]
  const run = runnerOptions({
    scenario: scenario([step('roam', 'roam_in_area', {
      durationMs: 10_000,
      area: { type: 'waypoints', waypoints },
      pauseMs: { min: 0, max: 0 },
    })], 'combat', { seed: 9, botOrdinal: 2 }),
  })
  const runner = new ScenarioRunner(run.options)
  await runner.start()
  for (let index = 0; index < 4; index++) {
    const goal = run.capabilities.pathfinderGoalCalls.at(-1)
    run.capabilities.position = { ...goal.position }
    await runner.tick(run.capabilities.now())
    await runner.tick(run.capabilities.now())
  }
  const xs = run.capabilities.pathfinderGoalCalls.slice(0, 4).map((goal) => goal.position.x)
  assert.equal(new Set(xs.slice(0, 3)).size, 3)
  assert.equal(xs[3], xs[0])
})

test('move_to_and_wait 核实真实位置稳定 500ms，离开重置且目标不重复下发', async () => {
  const run = runnerOptions({
    scenario: scenario([step('move', 'move_to_and_wait', {
      pos: { x: 10, y: 64, z: 0 }, radius: 2, timeoutMs: 5_000,
    })]),
  })
  const runner = new ScenarioRunner(run.options)
  await runner.start()
  await runner.tick(run.capabilities.now())
  run.capabilities.reachGoal()
  await runner.tick(run.capabilities.now())
  assert.equal(run.capabilities.pathfinderGoalCalls.length, 1)
  assert.equal(run.events.at(-1).status, 'running')

  run.capabilities.position = { x: 9, y: 64, z: 0 }
  await runner.tick(run.capabilities.now())
  run.capabilities.advance(499)
  await runner.tick(run.capabilities.now())
  assert.equal(run.events.at(-1).status, 'running')
  run.capabilities.position = { x: 20, y: 64, z: 0 }
  await runner.tick(run.capabilities.now())
  run.capabilities.position = { x: 9, y: 64, z: 0 }
  run.capabilities.advance(500)
  await runner.tick(run.capabilities.now())
  assert.equal(run.events.at(-1).status, 'running')
  run.capabilities.advance(500)
  await runner.tick(run.capabilities.now())
  assert.equal(run.events.at(-1).status, 'succeeded')
})

test('move_to_and_wait 的 probe 双门先后顺序均可，areaId 不可信时不推进', async () => {
  for (const probeFirst of [true, false]) {
    const run = runnerOptions({
      scenario: scenario([step('move', 'move_to_and_wait', {
        pos: { x: 5, y: 64, z: 0 }, radius: 1, areaId: 'combat-a',
        requireProbeEvent: 'area_arrived', timeoutMs: 5_000,
      })]),
    })
    const runner = new ScenarioRunner(run.options)
    await runner.start()
    const running = run.events[0]
    const bad = await runner.signal(signalFor(running, {
      signalId: `bad-${probeFirst}`,
      payload: { eventType: 'area_arrived', areaId: 'other' },
    }))
    assert.equal(bad.skipped, true)

    const acceptProbe = () => runner.signal(signalFor(running, {
      signalId: `good-${probeFirst}`,
      payload: { eventType: 'area_arrived', areaId: 'combat-a' },
    }))
    const arrive = async () => {
      run.capabilities.position = { x: 5, y: 64, z: 0 }
      await runner.tick(run.capabilities.now())
      run.capabilities.advance(500)
      await runner.tick(run.capabilities.now())
    }
    if (probeFirst) {
      await acceptProbe()
      assert.equal(run.events.at(-1).status, 'running')
      await arrive()
    } else {
      await arrive()
      assert.equal(run.events.at(-1).status, 'running')
      await acceptProbe()
    }
    assert.equal(run.events.at(-1).status, 'succeeded')
  }
})

test('move_to_and_wait 区分无 pathfinder、无路径和超时', async () => {
  const unavailable = runnerOptions({ scenario: scenario([step('move', 'move_to_and_wait', { pos: { x: 1, y: 64, z: 1 }, radius: 1 })]) })
  unavailable.capabilities.pathfinderAvailable = false
  await new ScenarioRunner(unavailable.options).start()
  assert.equal(unavailable.events.at(-1).errorCode, 'PATHFINDER_UNAVAILABLE')

  const noPath = runnerOptions({ scenario: scenario([step('move', 'move_to_and_wait', { pos: { x: 1, y: 64, z: 1 }, radius: 1 })]) })
  const noPathRunner = new ScenarioRunner(noPath.options)
  await noPathRunner.start()
  noPath.capabilities.failPath()
  await noPathRunner.tick(noPath.capabilities.now())
  assert.equal(noPath.events.at(-1).errorCode, 'PATH_NOT_FOUND')

  const timeout = runnerOptions({ scenario: scenario([step('move', 'move_to_and_wait', { pos: { x: 1, y: 64, z: 1 }, radius: 1, timeoutMs: 100 })]) })
  const timeoutRunner = new ScenarioRunner(timeout.options)
  await timeoutRunner.start()
  timeout.capabilities.advance(100)
  await timeoutRunner.tick(timeout.capabilities.now())
  assert.equal(timeout.events.at(-1).status, 'timed_out')
  assert.equal(timeout.events.at(-1).errorCode, 'MOVE_TIMEOUT')
})

test('find_entity 支持优先级和锁定，锁定存活时不会切换更近目标', async () => {
  const run = runnerOptions({
    scenario: scenario([
      step('find', 'find_entity', { selector: { kind: 'hostile', types: ['zombie'], radius: 20, priority: 'lowest_health' } }),
      step('attack', 'attack_until', {
        selector: { kind: 'hostile', types: ['zombie'], radius: 20, priority: 'nearest' },
        attackIntervalMs: 500, chase: false, reacquire: true,
        stop: { durationMs: 2_000, damageAtLeast: 100, successPolicy: 'any' },
      }),
    ]),
  })
  run.capabilities.entityValues = [
    { id: 1, kind: 'hostile', type: 'zombie', name: 'zombie-a', health: 10, dead: false, position: { x: 3, y: 64, z: 0 } },
    { id: 2, kind: 'hostile', type: 'zombie', name: 'zombie-b', health: 2, dead: false, position: { x: 3, y: 64, z: 0 } },
  ]
  const runner = new ScenarioRunner(run.options)
  await runner.start()
  assert.equal(run.capabilities.attackCalls[0].entityId, 2)

  run.capabilities.entityValues.push({ id: 3, kind: 'hostile', type: 'zombie', name: 'zombie-c', health: 1, dead: false, position: { x: 1, y: 64, z: 0 } })
  run.capabilities.advance(500)
  await runner.tick(run.capabilities.now())
  assert.equal(run.capabilities.attackCalls.at(-1).entityId, 2)
})

test('respawn_and_rejoin 等新 spawn 后跳转且受重进次数和 run deadline 限制', async () => {
  const steps = [
    step('entry', 'wait', { durationMs: 1 }),
    step('respawn', 'respawn_and_rejoin', { entryStepId: 'entry', maxAttempts: 1, timeoutMs: 1_000 }),
  ]
  const run = runnerOptions({ scenario: scenario(steps), resumeStepId: 'respawn' })
  run.capabilities.dead = true
  const runner = new ScenarioRunner(run.options)
  await runner.start()
  assert.equal(run.capabilities.respawnCalls, 1)
  assert.equal(runner.currentStepId, 'respawn')
  run.capabilities.spawn()
  await runner.tick(run.capabilities.now())
  assert.equal(runner.currentStepId, 'entry')

  run.capabilities.advance(1)
  await runner.tick(run.capabilities.now())
  assert.equal(runner.currentStepId, 'respawn')
  run.capabilities.dead = true
  run.capabilities.spawn()
  await runner.tick(run.capabilities.now())
  assert.equal(run.events.at(-1).status, 'failed')

  const deadline = runnerOptions({ scenario: scenario(steps), resumeStepId: 'respawn', runDeadline: 1_100 })
  deadline.capabilities.dead = true
  const deadlineRunner = new ScenarioRunner(deadline.options)
  await deadlineRunner.start()
  deadline.capabilities.nowMs = 1_101
  deadline.capabilities.spawn()
  await deadlineRunner.tick(deadline.capabilities.now())
  assert.equal(deadlineRunner.isTerminal, true)
  assert.notEqual(deadlineRunner.currentStepId, 'entry')
})
