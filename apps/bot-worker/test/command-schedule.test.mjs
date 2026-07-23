import assert from 'node:assert/strict'
import test from 'node:test'

import { CommandScheduler } from '../dist/scheduler/command-schedule.js'

function makeBot() {
  return {
    username: 'test-bot',
    chat(_message) {},
  }
}

function sampleApplyCommand() {
  return {
    cmd: 'command-schedule',
    requestId: 'req-1',
    runId: '42',
    runUuid: '11111111-2222-3333-4444-555555555555',
    botUuid: '00000000-0000-0000-0000-000000000001',
    generation: 7,
    stepId: 'command-schedule',
    scheduleRunId: 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee',
    correlationToken: 'corr-1',
    startMode: 'absolute',
    scheduleStartAtUnixMs: 1_000,
    runDeadlineUnixMs: 5_000,
    jitterSeed: '20260720',
    plan: {
      durationMs: 4_000,
      jitterMs: 20,
      occurrences: [
        { commandId: 'a', occurrence: 0, commandDeclarationIndex: 0, baseAtMs: 0, jitterOffsetMs: -13, actionRunId: '11111111-2222-3333-4444-aaaaaaaaaaaa', command: '/say ready' },
        { commandId: 'b', occurrence: 0, commandDeclarationIndex: 1, baseAtMs: 500, jitterOffsetMs: 10, actionRunId: '11111111-2222-3333-4444-bbbbbbbbbbbb', command: '/list' },
      ],
    },
    skipOccurrences: [],
  }
}

function makeHarness({ chat, bot, now } = {}) {
  let clock = typeof now === 'function' ? now() : (now ?? 1_000)
  const events = []
  const scheduler = new CommandScheduler({
    getBot: () => bot ?? makeBot(),
    chat: chat ?? (() => {}),
    now: () => (typeof now === 'function' ? now() : clock),
    sleep: async () => {},
    logger: { warn() {} },
    // 测试避免写 stdout
    sendEvent: () => {},
  })
  scheduler.on('emit', (payload) => events.push(payload))
  return {
    scheduler,
    events,
    setClock(value) {
      clock = value
    },
  }
}

test('apply absolute 模式接受 → 同步回执 accepted', () => {
  const { scheduler } = makeHarness()
  const res = scheduler.apply(sampleApplyCommand())
  scheduler.shutdown()
  assert.equal(res.ok, true)
})

test('apply 非法 runUuid → rejected', () => {
  const { scheduler } = makeHarness()
  const cmd = sampleApplyCommand()
  cmd.runUuid = 'not-a-uuid'
  const res = scheduler.apply(cmd)
  scheduler.shutdown()
  assert.equal(res.ok, false)
  assert.equal(res.errorCode, 'COMMAND_ARGUMENT_INVALID')
})

test('apply barrier 模式必须 release 后才执行，且 barrierKey 必须匹配', () => {
  const { scheduler } = makeHarness()
  const cmd = sampleApplyCommand()
  cmd.startMode = 'barrier'
  cmd.scheduleStartAtUnixMs = undefined
  cmd.barrierKey = 'main'
  assert.equal(scheduler.apply(cmd).ok, true)
  const wrong = scheduler.release({
    cmd: 'command-schedule-release',
    requestId: 'r',
    runUuid: cmd.runUuid,
    botUuid: cmd.botUuid,
    generation: cmd.generation,
    stepId: cmd.stepId,
    scheduleRunId: cmd.scheduleRunId,
    barrierKey: 'wrong',
    releaseAtUnixMs: 2_000,
  })
  assert.equal(wrong.ok, false)
  const ok = scheduler.release({
    cmd: 'command-schedule-release',
    requestId: 'r2',
    runUuid: cmd.runUuid,
    botUuid: cmd.botUuid,
    generation: cmd.generation,
    stepId: cmd.stepId,
    scheduleRunId: cmd.scheduleRunId,
    barrierKey: 'main',
    releaseAtUnixMs: 2_000,
  })
  assert.equal(ok.ok, true)
  scheduler.shutdown()
})

test('release 重复 releaseAt 返回 alreadyReleased=true；不同 releaseAt 拒绝', () => {
  const { scheduler } = makeHarness()
  const cmd = sampleApplyCommand()
  cmd.startMode = 'barrier'
  cmd.scheduleStartAtUnixMs = undefined
  cmd.barrierKey = 'main'
  scheduler.apply(cmd)
  const first = scheduler.release({
    cmd: 'command-schedule-release',
    requestId: 'r1',
    runUuid: cmd.runUuid,
    botUuid: cmd.botUuid,
    generation: cmd.generation,
    stepId: cmd.stepId,
    scheduleRunId: cmd.scheduleRunId,
    barrierKey: 'main',
    releaseAtUnixMs: 2_000,
  })
  assert.equal(first.ok, true)
  const replay = scheduler.release({
    cmd: 'command-schedule-release',
    requestId: 'r2',
    runUuid: cmd.runUuid,
    botUuid: cmd.botUuid,
    generation: cmd.generation,
    stepId: cmd.stepId,
    scheduleRunId: cmd.scheduleRunId,
    barrierKey: 'main',
    releaseAtUnixMs: 2_000,
  })
  assert.equal(replay.alreadyReleased, true)
  const conflict = scheduler.release({
    cmd: 'command-schedule-release',
    requestId: 'r3',
    runUuid: cmd.runUuid,
    botUuid: cmd.botUuid,
    generation: cmd.generation,
    stepId: cmd.stepId,
    scheduleRunId: cmd.scheduleRunId,
    barrierKey: 'main',
    releaseAtUnixMs: 2_500,
  })
  assert.equal(conflict.ok, false)
  assert.equal(conflict.errorCode, 'COMMAND_SCHEDULE_REJECTED')
  scheduler.shutdown()
})

test('取消幂等：未开始 occurrence 立即写 cancelled，第二次返回 alreadyCancelled', () => {
  const { scheduler, events } = makeHarness()
  const cmd = sampleApplyCommand()
  cmd.scheduleRunId = '11111111-aaaa-bbbb-cccc-dddddddddddd'
  cmd.runUuid = '22222222-aaaa-bbbb-cccc-dddddddddddd'
  // barrier 模式：apply 不启动计时器，保证 cancel 前无 occurrence 进入 inflight。
  cmd.startMode = 'barrier'
  cmd.scheduleStartAtUnixMs = undefined
  cmd.barrierKey = 'main'
  assert.equal(scheduler.apply(cmd).ok, true)
  const first = scheduler.cancel({
    cmd: 'command-schedule-cancel',
    requestId: 'c1',
    runUuid: cmd.runUuid,
    botUuid: cmd.botUuid,
    generation: cmd.generation,
    stepId: cmd.stepId,
    scheduleRunId: cmd.scheduleRunId,
    reason: 'manual',
    correlationToken: cmd.correlationToken,
  })
  assert.equal(first.ok, true)
  assert.equal(first.alreadyCancelled, undefined)
  const second = scheduler.cancel({
    cmd: 'command-schedule-cancel',
    requestId: 'c2',
    runUuid: cmd.runUuid,
    botUuid: cmd.botUuid,
    generation: cmd.generation,
    stepId: cmd.stepId,
    scheduleRunId: cmd.scheduleRunId,
    reason: 'manual',
    correlationToken: cmd.correlationToken,
  })
  assert.equal(second.alreadyCancelled, true)
  const cancelled = events.filter((p) => p.kind === 'result').map((p) => p.event.status)
  assert.ok(cancelled.length > 0)
  assert.ok(cancelled.every((s) => s === 'cancelled'), `期望全部 cancelled，实际 ${cancelled.join(',')}`)
  scheduler.shutdown()
})

test('bot.chat 抛错 → 重试 3 次 → failed 并保留 attemptErrors', async () => {
  let count = 0
  const { scheduler, events } = makeHarness({
    chat: () => {
      count += 1
      throw new Error('boom')
    },
  })
  const cmd = sampleApplyCommand()
  cmd.scheduleRunId = '33333333-aaaa-bbbb-cccc-dddddddddddd'
  cmd.runUuid = '44444444-aaaa-bbbb-cccc-dddddddddddd'
  cmd.plan = {
    durationMs: 4_000,
    jitterMs: 20,
    occurrences: [
      { commandId: 'a', occurrence: 0, commandDeclarationIndex: 0, baseAtMs: 0, jitterOffsetMs: 0, actionRunId: '11111111-2222-3333-4444-cccccccccccc', command: '/say hi' },
    ],
  }
  assert.equal(scheduler.apply(cmd).ok, true)
  // 等待 in-flight 完成（chat 同步抛错，sleep 由 noop；scheduler 通过 inflight 推进）
  for (let i = 0; i < 50; i += 1) {
    if (events.some((p) => p.kind === 'result' && p.event.status === 'failed')) break
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
  assert.equal(count, 3, '应尝试 3 次')
  const failed = events.find((p) => p.kind === 'result' && p.event.status === 'failed')
  assert.ok(failed, '应产生 failed 结果事件')
  assert.equal(failed.event.errorCode, 'COMMAND_SEND_FAILED')
  assert.equal(failed.event.attempt, 3)
  assert.ok(failed.event.attemptErrors.length <= 2, 'attemptErrors 最多保留 2 条')
  scheduler.shutdown()
})

test('shutdown 强制取消所有计划，无残留 schedule', () => {
  const { scheduler } = makeHarness()
  for (let i = 0; i < 10; i += 1) {
    const cmd = sampleApplyCommand()
    cmd.scheduleRunId = `00000000-0000-0000-0000-${i.toString().padStart(12, '0')}`
    cmd.runUuid = `aaaaaaaa-0000-0000-0000-${i.toString().padStart(12, '0')}`
    // barrier 避免立即启动计时器
    cmd.startMode = 'barrier'
    cmd.scheduleStartAtUnixMs = undefined
    cmd.barrierKey = 'main'
    scheduler.apply(cmd)
  }
  scheduler.shutdown()
  assert.equal(scheduler.debugState().schedules, 0)
})

test('runDeadline 已过：apply 阶段即拒绝（COMMAND_DEADLINE_EXCEEDED）', () => {
  const scheduler = new CommandScheduler({
    getBot: () => makeBot(),
    chat: () => {},
    now: () => 10_000,
    sleep: async () => {},
    logger: { warn() {} },
    sendEvent: () => {},
  })
  const cmd = sampleApplyCommand()
  cmd.runDeadlineUnixMs = 9_000
  const res = scheduler.apply(cmd)
  assert.equal(res.ok, false)
  assert.equal(res.errorCode, 'COMMAND_DEADLINE_EXCEEDED')
  scheduler.shutdown()
})
