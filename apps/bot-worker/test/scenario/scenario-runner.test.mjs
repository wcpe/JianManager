import assert from 'node:assert/strict'
import test from 'node:test'

import { ScenarioRunner } from '../../dist/scenario/runner.js'
import { parseScenarioRuntime } from '../../dist/scenario/types.js'
import { runnerOptions, scenario, step } from './helpers.mjs'

test('Scenario runtime 解包新 envelope 并兼容旧 cohort JSON', () => {
  const cohort = { key: 'combat', percent: 100, steps: [step('wait', 'wait', { durationMs: 100 })] }
  const enveloped = parseScenarioRuntime({ seed: 42, botOrdinal: 7, scenario: cohort })
  const legacy = parseScenarioRuntime({ ...cohort, seed: 42, botOrdinal: 7 })

  assert.deepEqual(enveloped, legacy)
  assert.equal(enveloped.seed, 42)
  assert.equal(enveloped.botOrdinal, 7)
})

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
  const calls = { cancel: 0, dispose: 0, token: null }
  const { options, capabilities, events } = runnerOptions({
    actionFactory: () => ({
      async start() { return { state: 'running' } },
      async tick() { return { state: 'running' } },
      async cancel(context) {
        calls.cancel++
        calls.token = { ...context.cancelToken }
      },
      async dispose() { calls.dispose++ },
    }),
  })
  const runner = new ScenarioRunner(options)
  await runner.start()

  await runner.cancel('用户停止')
  await runner.cancel('重复停止')
  await runner.dispose()

  assert.equal(calls.cancel, 1)
  assert.deepEqual(calls.token, { cancelled: true, reason: '用户停止' })
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

test('Runner 对契约外未知动作结构化失败而不伪完成', async () => {
  const { options, events } = runnerOptions({
    scenario: scenario([step('unknown', 'unknown_action')]),
  })
  const runner = new ScenarioRunner(options)

  await runner.start()

  assert.equal(events.at(-1).status, 'failed')
  assert.equal(events.at(-1).errorCode, 'ACTION_INTERNAL_ERROR')
  assert.match(events.at(-1).message, /未实现动作类型/)
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

test('Runner cancel 可打断异步寻路初始化且不会提交旧成功或推进下一步', async () => {
  const run = runnerOptions({
    scenario: scenario([
      step('move', 'move_to_and_wait', { pos: { x: 10, y: 64, z: 0 }, radius: 1 }),
      step('after', 'wait', { durationMs: 1 }),
    ]),
  })
  let releaseGoal
  let goalStarted
  const goalStartedPromise = new Promise((resolve) => { goalStarted = resolve })
  run.capabilities.setPathfinderGoal = async (goal) => {
    run.capabilities.pathfinderGoalCalls.push(structuredClone(goal))
    goalStarted()
    await new Promise((resolve) => { releaseGoal = resolve })
    return { status: 'set' }
  }
  const runner = new ScenarioRunner(run.options)
  const starting = runner.start()
  await goalStartedPromise
  const cancelling = runner.cancel('异步取消')
  releaseGoal()
  await Promise.all([starting, cancelling])

  assert.equal(run.events.filter((event) => event.status === 'succeeded').length, 0)
  assert.equal(run.events.at(-1).status, 'cancelled')
  assert.notEqual(runner.currentStepId, 'after')
  assert.ok(run.capabilities.clearGoalCount >= 1)
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
  assert.ok(Buffer.byteLength(JSON.stringify(events[0].result)) <= 16 * 1024)
})

test('Runner 在 tick 调用动作前裁决 move/attack/barrier 截止时间', async () => {
  const cases = [
    {
      name: 'move',
      step: step('move', 'move_to_and_wait', { pos: { x: 0, y: 64, z: 0 }, radius: 1, timeoutMs: 500 }),
      errorCode: 'MOVE_TIMEOUT',
      prepare: async (run, runner) => {
        await runner.tick(run.capabilities.now())
        run.capabilities.advance(500)
      },
    },
    {
      name: 'attack',
      step: step('attack', 'attack_until', {
        selector: { kind: 'hostile', radius: 16, priority: 'nearest' },
        attackIntervalMs: 100, chase: false, reacquire: true,
        stop: { durationMs: 100, damageAtLeast: 1, successPolicy: 'all' }, timeoutMs: 100,
      }),
      errorCode: 'ATTACK_ASSERTION_UNMET',
      prepare: async (run) => { run.capabilities.advance(100) },
    },
    {
      name: 'barrier',
      step: step('barrier', 'barrier', { key: 'ready', release: { type: 'all' }, timeoutMs: 500 }),
      errorCode: 'BARRIER_TIMEOUT',
      prepare: async (run, runner) => {
        const running = run.events[0]
        run.capabilities.nowMs = 1_400
        await runner.signal({
          signalId: 'release-before-deadline', botId: 'bot-1', sessionId: 'run-1', generation: 3,
          actionRunId: running.actionRunId, stepId: running.stepId, correlationToken: running.correlationToken,
          type: 'barrier-release', payload: { round: 1, releaseAtUnixMs: 1_500 },
        })
        run.capabilities.nowMs = 1_500
      },
    },
  ]

  for (const item of cases) {
    const run = runnerOptions({ scenario: scenario([item.step]) })
    const runner = new ScenarioRunner(run.options)
    await runner.start()
    await item.prepare(run, runner)
    await runner.tick(run.capabilities.now())
    assert.equal(run.events.at(-1).status, 'timed_out', item.name)
    assert.equal(run.events.at(-1).errorCode, item.errorCode, item.name)
    assert.equal(run.events.some((event) => event.status === 'succeeded'), false, item.name)
  }
})

test('Runner 在 signal 调用动作前裁决 step/run deadline 并安全跳过迟到完整信号', async () => {
  for (const useRunDeadline of [false, true]) {
    const run = runnerOptions({
      scenario: scenario([step('probe', 'wait_probe_event', { event: 'room_joined', timeoutMs: 1_000 })]),
      runDeadline: useRunDeadline ? 1_100 : undefined,
    })
    const runner = new ScenarioRunner(run.options)
    await runner.start()
    const running = run.events[0]
    run.capabilities.nowMs = useRunDeadline ? 1_100 : 2_000
    const receipt = await runner.signal({
      signalId: `late-${useRunDeadline}`, botId: 'bot-1', sessionId: 'run-1', generation: 3,
      actionRunId: running.actionRunId, stepId: running.stepId, correlationToken: running.correlationToken,
      type: 'probe', payload: { eventType: 'room_joined' },
    }, run.capabilities.now())

    assert.equal(receipt.accepted, false)
    assert.equal(receipt.skipped, true)
    assert.equal(run.events.at(-1).status, 'timed_out')
    assert.equal(run.events.at(-1).errorCode, 'PROBE_EVENT_TIMEOUT')
    assert.equal(run.events.some((event) => event.status === 'succeeded'), false)
  }
})

test('Runner start 会拒绝已经过期的 runDeadline 且不调用动作 start', async () => {
  let starts = 0
  const run = runnerOptions({
    runDeadline: 999,
    actionFactory: () => ({
      async start() { starts++; return { state: 'succeeded' } },
      async tick() { return { state: 'succeeded' } },
      async cancel() {},
      async dispose() {},
    }),
  })
  const runner = new ScenarioRunner(run.options)
  await runner.start()

  assert.equal(starts, 0)
  assert.equal(run.events.at(-1).status, 'timed_out')
  assert.equal(runner.isTerminal, true)
})

test('Runner 结果统一截断为 16KiB 并保留 UTF-8 安全 preview', async () => {
  const original = { value: '汉'.repeat(10_000) }
  const run = runnerOptions({
    actionFactory: () => ({
      async start() { return { state: 'succeeded', result: original } },
      async tick() { return { state: 'running' } },
      async cancel() {},
      async dispose() {},
    }),
  })
  const runner = new ScenarioRunner(run.options)
  await runner.start()

  const bounded = run.events.at(-1).result
  assert.equal(bounded.truncated, true)
  assert.equal(bounded.originalBytes, Buffer.byteLength(JSON.stringify(original)))
  assert.equal(typeof bounded.preview, 'string')
  assert.equal(bounded.preview.includes('\uFFFD'), false)
  assert.ok(Buffer.byteLength(JSON.stringify(bounded)) <= 16 * 1024)
})
