import assert from 'node:assert/strict'
import test from 'node:test'

import { ScenarioRunner } from '../../dist/scenario/runner.js'
import { FakeScenarioCapabilities, runnerOptions, scenario, step } from './helpers.mjs'

test('wait_spawn 已 spawn 立即成功，spawn 后成功，end/kicked 前失败 CONNECT_ENDED', async () => {
  const readyCapabilities = new FakeScenarioCapabilities()
  readyCapabilities.spawned = true
  const ready = runnerOptions({
    capabilities: readyCapabilities,
    scenario: scenario([step('spawn', 'wait_spawn')]),
  })
  const readyRunner = new ScenarioRunner(ready.options)
  await readyRunner.start()
  assert.deepEqual(ready.events.map((event) => event.status), ['running', 'succeeded'])

  const delayed = runnerOptions({ scenario: scenario([step('spawn', 'wait_spawn')]) })
  const delayedRunner = new ScenarioRunner(delayed.options)
  await delayedRunner.start()
  delayed.capabilities.spawned = true
  await delayedRunner.tick(delayed.capabilities.now())
  assert.equal(delayed.events.at(-1).status, 'succeeded')

  const ended = runnerOptions({ scenario: scenario([step('spawn', 'wait_spawn')]) })
  const endedRunner = new ScenarioRunner(ended.options)
  await endedRunner.start()
  ended.capabilities.endedReason = 'kicked'
  await endedRunner.connectionEnded('kicked')
  assert.equal(ended.events.at(-1).status, 'failed')
  assert.equal(ended.events.at(-1).errorCode, 'CONNECT_ENDED')
})

test('wait 仅随集中时钟推进且不提前完成', async () => {
  const { options, capabilities, events } = runnerOptions({
    scenario: scenario([step('wait', 'wait', { durationMs: 250 })]),
  })
  const runner = new ScenarioRunner(options)
  await runner.start()
  capabilities.advance(249)
  await runner.tick(capabilities.now())
  assert.equal(events.at(-1).status, 'running')
  capabilities.advance(1)
  await runner.tick(capabilities.now())
  assert.equal(events.at(-1).status, 'succeeded')
})

test('send_command 展开白名单变量并复用 correlationToken', async () => {
  const { options, capabilities, events } = runnerOptions({
    scenario: scenario([
      step('send', 'send_command', { command: '/join {{botName}} {{botUuid}} {{runId}} {{cohortKey}} {{correlationToken}}' }),
      step('probe', 'wait_probe_event', { event: 'room_joined' }),
    ]),
  })
  const runner = new ScenarioRunner(options)
  await runner.start()

  assert.equal(capabilities.chats.length, 1)
  assert.match(capabilities.chats[0], /^\/join BotOne bot-1 run-1 combat /)
  assert.equal(events[1].correlationToken, events[2].correlationToken)
})

test('send_command 对未知变量、缺值和 chat 异常统一结构化失败', async () => {
  for (const [command, configure] of [
    ['/join {{unknown}}', () => {}],
    ['/join {{roomKey}}', () => {}],
    ['/join ok', (capabilities) => { capabilities.chatError = new Error('chat failed') }],
  ]) {
    const { options, capabilities, events } = runnerOptions({
      scenario: scenario([step('send', 'send_command', { command })]),
    })
    configure(capabilities)
    const runner = new ScenarioRunner(options)
    await runner.start()
    assert.equal(events.at(-1).status, 'failed')
    assert.equal(events.at(-1).errorCode, 'ACTION_INTERNAL_ERROR')
  }
})

test('wait_probe_event 只接受完整关联与 eventType，重复和迟到安全跳过', async () => {
  const { options, events } = runnerOptions({
    scenario: scenario([
      step('probe', 'wait_probe_event', { event: 'room_joined' }),
      step('after-probe', 'wait', { durationMs: 100 }),
    ]),
  })
  const runner = new ScenarioRunner(options)
  await runner.start()
  const running = events[0]
  assert.deepEqual(running.result, { type: 'waiting', wait: 'external', eventType: 'room_joined' })

  const base = {
    signalId: 'signal-1', botId: 'bot-1', sessionId: 'run-1', generation: 3,
    actionRunId: running.actionRunId, stepId: 'probe', correlationToken: running.correlationToken,
    type: 'probe', payload: { eventType: 'room_joined', value: 1 },
  }
  for (const override of [
    { sessionId: 'wrong-run' }, { botId: 'wrong-bot' }, { generation: 4 },
    { actionRunId: '00000000-0000-4000-8000-999999999999' }, { correlationToken: 'wrong-token' },
    { type: 'probe', payload: { eventType: 'wrong' } },
  ]) {
    const result = await runner.signal({ ...base, signalId: `bad-${Object.keys(override).join('-')}-${JSON.stringify(override)}`, ...override })
    assert.equal(result.skipped, true)
    assert.ok(result.errorCode)
  }
  assert.equal(events.filter((event) => event.status === 'succeeded').length, 0)

  const accepted = await runner.signal(base)
  const duplicate = await runner.signal(base)
  const late = await runner.signal({ ...base, signalId: 'signal-late' })
  assert.equal(accepted.accepted, true)
  assert.equal(duplicate.skipped, true)
  assert.equal(late.skipped, true)
  assert.equal(events.filter((event) => event.status === 'succeeded').length, 1)
})

test('barrier 上报 Go 兼容 arrived 载荷并按 round/releaseAt 等待且不提前推进', async () => {
  const { options, capabilities, events } = runnerOptions({
    scenario: scenario([step('barrier', 'barrier', {
      key: 'ready', release: { type: 'percent', value: 99 }, timeoutPolicy: 'fail', timeoutMs: 5_000,
    })]),
    stageIndex: 2,
  })
  const runner = new ScenarioRunner(options)
  await runner.start()
  const running = events[0]
  assert.deepEqual(running.result, {
    type: 'barrier-arrived', stageIndex: 2, cohortKey: 'combat', barrierKey: 'ready', round: 1,
    release: { type: 'percent', value: 99 }, timeoutPolicy: 'fail', deadlineUnixMs: 6_000,
  })

  const base = {
    signalId: 'release-1', botId: 'bot-1', sessionId: 'run-1', generation: 3,
    actionRunId: running.actionRunId, stepId: 'barrier', correlationToken: running.correlationToken,
    type: 'barrier-release', payload: { round: 1, releaseAtUnixMs: 1_500 },
  }
  const wrongRound = await runner.signal({ ...base, signalId: 'wrong-round', payload: { round: 2, releaseAtUnixMs: 1_500 } })
  assert.equal(wrongRound.skipped, true)
  const accepted = await runner.signal(base)
  assert.equal(accepted.accepted, true)
  capabilities.nowMs = 1_499
  await runner.tick(capabilities.now())
  assert.equal(events.at(-1).status, 'running')
  capabilities.nowMs = 1_500
  await runner.tick(capabilities.now())
  assert.equal(events.at(-1).status, 'succeeded')
})
