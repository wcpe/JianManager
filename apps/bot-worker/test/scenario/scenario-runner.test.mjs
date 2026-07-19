import assert from 'node:assert/strict'
import test from 'node:test'

import { ScenarioRunner } from '../../dist/scenario/runner.js'
import { runnerOptions, scenario, step } from './helpers.mjs'

test('Runner 按 start→step→terminal 顺序推进并仅发一次终态', async () => {
  const { options, capabilities, events } = runnerOptions({
    scenario: scenario([
      step('wait', 'wait', { durationMs: 100 }),
      step('send', 'send_command', { command: '/join {{botUuid}} {{correlationToken}}' }),
    ]),
  })
  const runner = new ScenarioRunner(options)

  await runner.start()
  capabilities.advance(100)
  await runner.tick(capabilities.now())
  await runner.tick(capabilities.now())

  assert.deepEqual(events.map(({ stepId, status }) => [stepId, status]), [
    ['wait', 'running'], ['wait', 'succeeded'],
    ['send', 'running'], ['send', 'succeeded'],
  ])
  assert.equal(runner.isTerminal, true)
  assert.equal(runner.currentStepId, undefined)
  assert.equal(capabilities.chats.length, 1)
})

test('Runner 在退避后重试并为每次尝试生成独立 actionRunId', async () => {
  const attempts = []
  const { options, capabilities, events } = runnerOptions({
    scenario: scenario([step('retry', 'wait', { durationMs: 10, maxAttempts: 2, retryBackoffMs: 50 })]),
    actionFactory: (_step, context) => ({
      async start() {
        attempts.push(context.attempt)
        return context.attempt === 1
          ? { state: 'failed', errorCode: 'ACTION_INTERNAL_ERROR', message: '首次失败' }
          : { state: 'succeeded', result: { retried: true } }
      },
      async tick() { return { state: 'running' } },
      async cancel() {},
      async dispose() {},
    }),
  })
  const runner = new ScenarioRunner(options)

  await runner.start()
  capabilities.advance(49)
  await runner.tick(capabilities.now())
  assert.deepEqual(attempts, [1])
  capabilities.advance(1)
  await runner.tick(capabilities.now())

  assert.deepEqual(attempts, [1, 2])
  assert.equal(events.filter((event) => event.status === 'failed').length, 1)
  assert.equal(events.filter((event) => event.status === 'succeeded').length, 1)
  assert.notEqual(events[0].actionRunId, events[2].actionRunId)
  assert.equal(events[2].attempt, 2)
})

test('Runner 对外部等待和屏障使用冻结超时错误码', async () => {
  for (const [type, fields, errorCode] of [
    ['wait_probe_event', { event: 'room_joined' }, 'PROBE_EVENT_TIMEOUT'],
    ['barrier', { key: 'ready', release: { type: 'all' }, timeoutPolicy: 'fail' }, 'BARRIER_TIMEOUT'],
  ]) {
    const { options, capabilities, events } = runnerOptions({
      scenario: scenario([step('timeout', type, { ...fields, timeoutMs: 100 })]),
    })
    const runner = new ScenarioRunner(options)
    await runner.start()
    capabilities.advance(100)
    await runner.tick(capabilities.now())
    assert.equal(events.at(-1).status, 'timed_out')
    assert.equal(events.at(-1).errorCode, errorCode)
  }
})

test('Runner cancel/dispose 清理动作与 pathfinder 且不会重复终态', async () => {
  const calls = { cancel: 0, dispose: 0 }
  const { options, capabilities, events } = runnerOptions({
    actionFactory: () => ({
      async start() { return { state: 'running' } },
      async tick() { return { state: 'running' } },
      async cancel() { calls.cancel++ },
      async dispose() { calls.dispose++ },
    }),
  })
  const runner = new ScenarioRunner(options)
  await runner.start()

  await runner.cancel('用户停止')
  await runner.cancel('重复停止')
  await runner.dispose()

  assert.equal(calls.cancel, 1)
  assert.equal(calls.dispose, 1)
  assert.equal(capabilities.clearGoalCount, 1)
  assert.equal(events.filter((event) => event.status === 'cancelled').length, 1)
  assert.equal(events.at(-1).errorCode, 'ACTION_CANCELLED')
})

test('Runner 将动作异常转为 ACTION_INTERNAL_ERROR', async () => {
  const { options, events } = runnerOptions({
    actionFactory: () => ({
      async start() { throw new Error('boom') },
      async tick() { return { state: 'running' } },
      async cancel() {},
      async dispose() {},
    }),
  })
  const runner = new ScenarioRunner(options)

  await runner.start()

  assert.equal(events.at(-1).status, 'failed')
  assert.equal(events.at(-1).errorCode, 'ACTION_INTERNAL_ERROR')
  assert.match(events.at(-1).message, /boom/)
})

test('Runner 对本段未实现动作结构化失败而不伪完成', async () => {
  const { options, events } = runnerOptions({
    scenario: scenario([step('move', 'move_to_and_wait', { pos: { x: 1, y: 2, z: 3 }, radius: 2 })]),
  })
  const runner = new ScenarioRunner(options)

  await runner.start()

  assert.equal(events.at(-1).status, 'failed')
  assert.equal(events.at(-1).errorCode, 'ACTION_INTERNAL_ERROR')
  assert.match(events.at(-1).message, /本段未实现/)
})

test('Runner 按 resumePolicy 选择恢复入口', async () => {
  const base = scenario([
    step('first', 'wait', { durationMs: 100 }),
    step('second', 'wait', { durationMs: 100, resumePolicy: 'restart_step' }),
  ])
  const restartStep = runnerOptions({ scenario: base, resumeStepId: 'second' })
  const runnerA = new ScenarioRunner(restartStep.options)
  await runnerA.start()
  assert.equal(runnerA.currentStepId, 'second')

  base.steps[1].resumePolicy = 'restart_scenario'
  const restartScenario = runnerOptions({ scenario: base, resumeStepId: 'second' })
  const runnerB = new ScenarioRunner(restartScenario.options)
  await runnerB.start()
  assert.equal(runnerB.currentStepId, 'first')
})

test('Runner 长循环集中 tick 不创建动作级 timer 且结果载荷有界', async () => {
  const largeResult = { value: 'x'.repeat(20_000) }
  const { options, capabilities, events } = runnerOptions({
    scenario: scenario([step('long', 'wait', { durationMs: 50_000, timeoutMs: 60_000 })]),
    actionFactory: () => ({
      async start() { return { state: 'running', result: largeResult } },
      async tick() { return { state: 'running' } },
      async cancel() {},
      async dispose() {},
    }),
  })
  const runner = new ScenarioRunner(options)
  await runner.start()
  for (let index = 0; index < 10_000; index++) {
    capabilities.advance(1)
    await runner.tick(capabilities.now())
  }

  assert.equal(events.length, 1)
  assert.ok(JSON.stringify(events[0].result).length <= 16_384)
})
