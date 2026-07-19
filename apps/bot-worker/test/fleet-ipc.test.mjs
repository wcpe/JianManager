import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { FleetController } from '../dist/ipc/fleet.js'

class FakeClock {
  nowMs = 1_000
  nextId = 1
  timers = new Map()

  now = () => this.nowMs

  setTimeout = (callback, delay) => {
    const id = this.nextId++
    this.timers.set(id, { at: this.nowMs + delay, callback })
    return id
  }

  clearTimeout = (id) => {
    this.timers.delete(id)
  }

  advance(ms) {
    this.nowMs += ms
    let due
    do {
      due = [...this.timers.entries()]
        .filter(([, timer]) => timer.at <= this.nowMs)
        .sort((a, b) => a[1].at - b[1].at)
      for (const [id, timer] of due) {
        this.timers.delete(id)
        timer.callback()
      }
    } while (due.length > 0)
  }
}

function createHarness(maxBots = 50, overrides = {}) {
  const events = []
  const connections = []
  const clock = new FakeClock()
  const { eventObserver, ...controllerOverrides } = overrides
  const controller = new FleetController({
    maxBots,
    workerEpoch: 'epoch-1',
    workerEpochGeneration: 7,
    now: clock.now,
    setTimeout: clock.setTimeout,
    clearTimeout: clock.clearTimeout,
    sendEvent: (event) => {
      const cloned = structuredClone(event)
      events.push(cloned)
      eventObserver?.(cloned)
    },
    createBot: (options) => {
      const bot = new EventEmitter()
      bot.username = options.username
      bot.health = 20
      bot.food = 20
      bot.entity = { position: { x: 1, y: 64, z: 2 } }
      bot.quitCount = 0
      bot.quit = () => { bot.quitCount++ }
      bot.chat = () => {}
      connections.push({ options, bot })
      return bot
    },
    createBehavior: (_botId, name) => ({
      name,
      start() {},
      stop() {},
      setMcBot() {},
      async tick() {},
    }),
    ...controllerOverrides,
  })
  return { controller, events, connections, clock }
}

function bot(id, overrides = {}) {
  return {
    id,
    name: id,
    host: '127.0.0.1',
    port: 25565,
    sessionId: 'session-1',
    generation: 3,
    configHash: `hash-${id}`,
    ...overrides,
  }
}

function latest(events, evt) {
  return events.findLast((event) => event.evt === evt)
}

test('create-bots 限制单批 50 并返回部分容量回执', () => {
  const { controller, events, connections } = createHarness(2)

  const oversized = Array.from({ length: 51 }, (_, index) => bot(`large-${index}`))
  controller.handleCommand({
    cmd: 'create-bots', requestId: 'large-request', batchId: 'large-batch',
    idempotencyKey: 'large-key', bots: oversized,
  })
  assert.equal(connections.length, 0)
  assert.equal(latest(events, 'batch-result').results.length, 51)
  assert.ok(latest(events, 'batch-result').results.every((item) => item.errorCode === 'batch_limit_exceeded'))

  controller.handleCommand({
    cmd: 'create-bots', requestId: 'request-1', batchId: 'batch-1',
    idempotencyKey: 'key-1', bots: [bot('a'), bot('b'), bot('c')],
  })
  const result = latest(events, 'batch-result')
  assert.equal(result.requestId, 'request-1')
  assert.equal(result.batchId, 'batch-1')
  assert.equal(result.idempotencyKey, 'key-1')
  assert.deepEqual(result.results.map((item) => item.status), [
    'accepted', 'accepted', 'capacity_insufficient',
  ])
  assert.equal(connections.length, 2)
})

test('create-bots 单项初始化失败不阻断同批其他 Bot', () => {
  const { controller, events, connections } = createHarness(50, {
    createBehavior: (botId, name) => {
      if (botId === 'bad') throw new Error('行为初始化失败')
      return {
        name,
        start() {},
        stop() {},
        setMcBot() {},
        async tick() {},
      }
    },
  })
  controller.handleCommand({
    cmd: 'create-bots', requestId: 'partial-request', batchId: 'partial-batch',
    idempotencyKey: 'partial-key', bots: [bot('bad'), bot('good')],
  })
  assert.deepEqual(latest(events, 'batch-result').results.map((item) => item.status), [
    'ephemeral_unavailable', 'accepted',
  ])
  assert.equal(connections.length, 1)
})

test('create-bots 瞬时失败不缓存且同 key 恢复后可成功', () => {
  let fail = true
  const { controller, events, connections } = createHarness(50, {
    createBehavior: (_botId, name) => {
      if (fail) throw new Error('瞬时初始化失败')
      return {
        name,
        start() {},
        stop() {},
        setMcBot() {},
        async tick() {},
      }
    },
  })
  const command = {
    cmd: 'create-bots', requestId: 'transient-1', batchId: 'transient-batch',
    idempotencyKey: 'transient-key', bots: [bot('recoverable')],
  }

  controller.handleCommand(command)
  assert.equal(latest(events, 'batch-result').results[0].status, 'ephemeral_unavailable')
  fail = false
  controller.handleCommand({ ...command, requestId: 'transient-2' })

  assert.equal(latest(events, 'batch-result').results[0].status, 'accepted')
  assert.equal(connections.length, 1)
})

test('create-bots 幂等重放首次结果且不同载荷明确冲突', () => {
  const { controller, events, connections } = createHarness()
  const command = {
    cmd: 'create-bots', requestId: 'request-1', batchId: 'batch-1',
    idempotencyKey: 'same-key', bots: [bot('a')],
  }

  controller.handleCommand(command)
  controller.handleCommand({ ...command, requestId: 'request-2' })
  assert.equal(connections.length, 1)
  assert.equal(latest(events, 'batch-result').requestId, 'request-2')
  assert.equal(latest(events, 'batch-result').results[0].status, 'accepted')

  controller.handleCommand({
    ...command,
    requestId: 'request-3',
    bots: [bot('different')],
  })
  assert.equal(connections.length, 1)
  assert.equal(latest(events, 'batch-result').results[0].status, 'conflict')
  assert.equal(latest(events, 'batch-result').results[0].errorCode, 'idempotency_conflict')
})

test('幂等缓存按上限淘汰，长期运行不会无界增长', () => {
  const { controller, connections } = createHarness(50, { idempotencyCacheSize: 1 })
  const first = {
    cmd: 'create-bots', requestId: 'request-1', batchId: 'batch-1',
    idempotencyKey: 'key-1', bots: [bot('a')],
  }
  controller.handleCommand(first)
  controller.handleCommand({
    cmd: 'create-bots', requestId: 'request-2', batchId: 'batch-2',
    idempotencyKey: 'key-2', bots: [bot('b')],
  })
  controller.handleCommand({
    ...first,
    requestId: 'request-3',
    bots: [bot('a', { generation: 4, configHash: 'hash-a-4' })],
  })
  assert.equal(connections.length, 3)
})

test('create-bots 在 gen4 后拒绝 gen3 且同 key 稳定重放 stale', () => {
  const { controller, events, connections } = createHarness()
  controller.handleCommand({
    cmd: 'create-bots', requestId: 'gen4-request', batchId: 'gen4-batch',
    idempotencyKey: 'gen4-key', bots: [bot('a', { generation: 4, configHash: 'hash-4' })],
  })
  const staleCommand = {
    cmd: 'create-bots', requestId: 'gen3-request', batchId: 'gen3-batch',
    idempotencyKey: 'gen3-key', bots: [bot('a', { generation: 3, configHash: 'hash-3' })],
  }

  controller.handleCommand(staleCommand)
  const first = structuredClone(latest(events, 'batch-result').results[0])
  assert.deepEqual(first, {
    botId: 'a', accepted: false, skipped: true, status: 'stale',
    errorCode: 'stale_generation', error: 'Bot generation 已过期',
  })
  assert.equal(connections.length, 1)
  assert.equal(controller.snapshots()[0].generation, 4)

  controller.handleCommand({ ...staleCommand, requestId: 'gen3-replay' })
  assert.deepEqual(latest(events, 'batch-result').results[0], first)
  assert.equal(connections.length, 1)
})

test('create-bots 同代同配置幂等跳过、同代异配置冲突且高代才替换', () => {
  const { controller, events, connections } = createHarness()
  controller.handleCommand({ cmd: 'create-bots', bots: [bot('a', { generation: 5, configHash: 'hash-5' })] })

  controller.handleCommand({
    cmd: 'create-bots', requestId: 'same-request', batchId: 'same-batch',
    idempotencyKey: 'same-generation-key', bots: [bot('a', { generation: 5, configHash: 'hash-5' })],
  })
  assert.deepEqual(latest(events, 'batch-result').results[0], {
    botId: 'a', accepted: true, skipped: true, status: 'accepted',
  })
  assert.equal(connections.length, 1)

  const conflictCommand = {
    cmd: 'create-bots', requestId: 'conflict-request', batchId: 'conflict-batch',
    idempotencyKey: 'generation-conflict-key', bots: [bot('a', { generation: 5, configHash: 'other-hash' })],
  }
  controller.handleCommand(conflictCommand)
  const conflict = structuredClone(latest(events, 'batch-result').results[0])
  assert.deepEqual(conflict, {
    botId: 'a', accepted: false, skipped: true, status: 'conflict',
    errorCode: 'config_hash_conflict', error: '相同 generation 的 configHash 不一致',
  })
  controller.handleCommand({ ...conflictCommand, requestId: 'conflict-replay' })
  assert.deepEqual(latest(events, 'batch-result').results[0], conflict)
  assert.equal(connections.length, 1)

  controller.handleCommand({
    cmd: 'create-bots', requestId: 'higher-request', batchId: 'higher-batch',
    idempotencyKey: 'higher-key', bots: [bot('a', { generation: 6, configHash: 'hash-6' })],
  })
  assert.equal(latest(events, 'batch-result').results[0].status, 'accepted')
  assert.equal(connections.length, 2)
  assert.equal(controller.snapshots()[0].generation, 6)
})

test('create-bots 并发不同 key 时旧 generation 不能覆盖新 generation', async () => {
  const { controller, events, connections } = createHarness()
  controller.handleCommand({ cmd: 'create-bots', bots: [bot('a', { generation: 2, configHash: 'hash-2' })] })

  await Promise.all([
    Promise.resolve().then(() => controller.handleCommand({
      cmd: 'create-bots', requestId: 'concurrent-new', batchId: 'concurrent-new-batch',
      idempotencyKey: 'concurrent-new-key', bots: [bot('a', { generation: 4, configHash: 'hash-4' })],
    })),
    Promise.resolve().then(() => controller.handleCommand({
      cmd: 'create-bots', requestId: 'concurrent-old', batchId: 'concurrent-old-batch',
      idempotencyKey: 'concurrent-old-key', bots: [bot('a', { generation: 3, configHash: 'hash-3' })],
    })),
  ])

  assert.equal(connections.length, 2)
  assert.equal(controller.snapshots()[0].generation, 4)
  const stale = events.find((event) => event.evt === 'batch-result' && event.requestId === 'concurrent-old')
  assert.equal(stale.results[0].status, 'stale')
})

test('action-event 路由骨架保留冻结动作字段', () => {
  const { controller, events } = createHarness()
  controller.emitActionEvent({
    botId: 'a',
    sessionId: 'session-1',
    generation: 3,
    actionRunId: 'run-1',
    stepId: 'step-1',
    attempt: 1,
    status: 'running',
    correlationToken: 'token-1',
    observedAt: 1_000,
  })
  assert.deepEqual(latest(events, 'action-event'), {
    evt: 'action-event',
    action: {
      botId: 'a',
      sessionId: 'session-1',
      generation: 3,
      actionRunId: 'run-1',
      stepId: 'step-1',
      attempt: 1,
      status: 'running',
      correlationToken: 'token-1',
      observedAt: 1_000,
    },
  })
})

test('满容量时替换已有 Bot 不占新增容量', () => {
  const { controller, events, connections } = createHarness(2)
  controller.handleCommand({ cmd: 'create-bots', bots: [bot('a'), bot('b')] })
  controller.handleCommand({
    cmd: 'create-bots', requestId: 'replace-request', batchId: 'replace-batch',
    idempotencyKey: 'replace-key', bots: [bot('a', { generation: 4 })],
  })

  assert.equal(connections.length, 3)
  assert.equal(latest(events, 'batch-result').results[0].status, 'accepted')
  assert.equal(controller.metrics().activeBots, 2)
})

test('connectNotBefore 到点才连接，stop 和替换会取消旧定时器', () => {
  const { controller, connections, clock } = createHarness()
  controller.handleCommand({
    cmd: 'create-bots', requestId: 'delayed-request', batchId: 'delayed-batch',
    idempotencyKey: 'delayed-key', bots: [bot('delayed', { connectNotBefore: 1_500 })],
  })
  assert.equal(connections.length, 0)
  assert.equal(controller.metrics().connectingBots, 1)
  clock.advance(499)
  assert.equal(connections.length, 0)
  clock.advance(1)
  assert.equal(connections.length, 1)

  controller.handleCommand({ cmd: 'create-bots', bots: [bot('stop-me', { connectNotBeforeUnixMs: 2_500 })] })
  controller.handleCommand({ cmd: 'stop-bots', requestId: 'stop-request', botIds: ['stop-me'], generation: 3, reason: 'test' })
  clock.advance(1_000)
  assert.equal(connections.length, 1)

  controller.handleCommand({ cmd: 'create-bots', bots: [bot('replace-me', { connectNotBefore: 4_000 })] })
  controller.handleCommand({ cmd: 'create-bots', bots: [bot('replace-me', { generation: 4, connectNotBefore: 5_000 })] })
  clock.advance(1_500)
  assert.equal(connections.length, 1)
  clock.advance(1_000)
  assert.equal(connections.length, 2)
})

test('stop-bots 旧代不停止新 Bot，高代停止先发 stopped 再回执', () => {
  const diagnostics = []
  let batchObservedAfterQuit = false
  const { controller, events, connections } = createHarness(50, {
    onBotStopped: (diagnostic) => diagnostics.push(diagnostic),
    eventObserver: (event) => {
      if (event.evt === 'batch-result' && event.requestId === 'stop-6') {
        batchObservedAfterQuit = connections[0].bot.quitCount === 1
      }
    },
  })
  controller.handleCommand({ cmd: 'create-bots', bots: [bot('a', { generation: 5, configHash: 'hash-5' })] })

  controller.handleCommand({
    cmd: 'stop-bots', requestId: 'stop-4', botIds: ['a'], generation: 4,
    reason: '旧指令 token=不得外泄',
  })
  assert.deepEqual(latest(events, 'batch-result').results[0], {
    botId: 'a', accepted: false, skipped: true, status: 'stale',
    errorCode: 'stale_generation', error: '停止 generation 已过期',
  })
  assert.equal(controller.metrics().activeBots, 1)
  assert.equal(connections[0].bot.quitCount, 0)
  assert.deepEqual(diagnostics, [])

  const eventStart = events.length
  controller.handleCommand({
    cmd: 'stop-bots', requestId: 'stop-6', botIds: ['a'], generation: 6,
    reason: '调度收束 token=不得外泄',
  })
  const stoppedEvents = events.slice(eventStart)
  assert.equal(stoppedEvents[0].evt, 'bot-state')
  assert.equal(stoppedEvents[0].bots[0].status, 'stopped')
  assert.equal(stoppedEvents[0].bots[0].sessionId, 'session-1')
  assert.equal(stoppedEvents[0].bots[0].generation, 5)
  assert.equal(stoppedEvents[0].bots[0].configHash, 'hash-5')
  assert.equal(stoppedEvents[0].bots[0].workerEpoch, 'epoch-1')
  assert.equal(stoppedEvents[0].bots[0].workerEpochGeneration, 7)
  assert.equal(typeof stoppedEvents[0].bots[0].eventSeq, 'number')
  assert.equal(stoppedEvents[0].bots[0].observedAt, 1_000)
  assert.equal(stoppedEvents[1].evt, 'batch-result')
  assert.equal(stoppedEvents[1].requestId, 'stop-6')
  assert.equal(stoppedEvents[1].results[0].status, 'accepted')
  assert.equal(batchObservedAfterQuit, true)
  assert.equal(JSON.stringify(stoppedEvents).includes('token=不得外泄'), false)
  assert.deepEqual(diagnostics, [{ botId: 'a', generation: 6, reason: '调度收束 token=[已脱敏]' }])
  assert.equal(controller.metrics().activeBots, 0)
})

test('stop-bots 对不存在 Bot 返回稳定 already_stopped 成功', () => {
  const { controller, events } = createHarness()

  controller.handleCommand({
    cmd: 'stop-bots', requestId: 'stop-missing', botIds: ['missing'], generation: 3,
    reason: '重试停止',
  })

  assert.deepEqual(latest(events, 'batch-result').results[0], {
    botId: 'missing', accepted: true, skipped: true, status: 'accepted',
    errorCode: 'already_stopped',
  })
  assert.equal(latest(events, 'bot-state').bots[0].status, 'not_found')
})

test('旧 create/stop 不得覆盖或删除 Fleet-managed Bot', () => {
  const { controller, events, connections } = createHarness()
  controller.handleCommand({
    cmd: 'create-bots', requestId: 'fleet-create', batchId: 'fleet-batch',
    idempotencyKey: 'fleet-key', bots: [bot('managed', { generation: 5, configHash: 'fleet-hash' })],
  })
  const legacyConfig = {
    id: 'managed', name: 'legacy-overwrite', host: '127.0.0.1', port: 25565,
  }

  controller.handleCommand({ cmd: 'create-bots', bots: [legacyConfig] })
  assert.equal(connections.length, 1)
  assert.equal(controller.snapshots()[0].generation, 5)
  assert.match(latest(events, 'bot-error').error, /Fleet RPC/)

  controller.handleCommand({ cmd: 'stop-bots', botIds: ['managed'] })
  assert.equal(controller.metrics().activeBots, 1)
  assert.equal(controller.snapshots()[0].generation, 5)
})

test('snapshot 和 bot-state 携带冻结代际字段及单调 eventSeq', () => {
  const { controller, events, connections, clock } = createHarness()
  controller.handleCommand({ cmd: 'create-bots', bots: [bot('a')] })
  const connecting = latest(events, 'bot-state').bots[0]
  connections[0].bot.emit('spawn')
  const connected = latest(events, 'bot-state').bots[0]
  assert.ok(connected.eventSeq > connecting.eventSeq)
  assert.equal(connected.workerEpoch, 'epoch-1')
  assert.equal(connected.workerEpochGeneration, 7)
  assert.equal(connected.sessionId, 'session-1')
  assert.equal(connected.generation, 3)
  assert.equal(connected.configHash, 'hash-a')

  clock.advance(10)
  controller.handleCommand({ cmd: 'get-fleet-snapshot', requestId: 'snapshot-request' })
  const result = latest(events, 'fleet-snapshot-result')
  assert.equal(result.requestId, 'snapshot-request')
  assert.equal(result.bots.length, 1)
  assert.equal(result.bots[0].status, 'connected')
  assert.deepEqual(result.bots[0].position, { x: 1, y: 64, z: 2 })
  assert.ok(result.bots[0].eventSeq > connected.eventSeq)
})

test('新进程 get-fleet-snapshot 明确返回空 bots', () => {
  const { controller, events } = createHarness()

  controller.handleCommand({ cmd: 'get-fleet-snapshot', requestId: 'empty-snapshot' })

  assert.deepEqual(latest(events, 'fleet-snapshot-result'), {
    evt: 'fleet-snapshot-result',
    requestId: 'empty-snapshot',
    bots: [],
  })
})

test('signal-actions 使用冻结命令名并逐项回执', () => {
  const { controller, events } = createHarness()
  controller.handleCommand({ cmd: 'create-bots', bots: [bot('a')] })
  const handled = controller.handleCommand({
    cmd: 'signal-actions',
    requestId: 'signal-request',
    signals: [
      { signalId: 'signal-1', botId: 'a', sessionId: 'session-1', generation: 3, actionRunId: 'run-1', stepId: 'step-1', type: 'probe' },
      { signalId: 'signal-2', botId: 'missing', sessionId: 'session-1', generation: 3, actionRunId: 'run-2', stepId: 'step-2', type: 'cancel' },
    ],
  })
  assert.equal(handled, true)
  assert.deepEqual(latest(events, 'signal-result').signalResults.map((item) => item.status), [
    'accepted', 'ephemeral_unavailable',
  ])
  assert.equal(controller.handleCommand({ cmd: 'signal-bot-actions' }), false)
})

test('Node IPC schema 与 Go 冻结 JSON 字段保持一致', () => {
  const goIPC = readFileSync(new URL('../../../internal/worker/bot/ipc.go', import.meta.url), 'utf8')
  const goManager = readFileSync(new URL('../../../internal/worker/bot/manager.go', import.meta.url), 'utf8')

  assert.match(goManager, /SignalActionsCommand\{Cmd: "signal-actions"/)
  assert.match(goIPC, /ConnectNotBefore\s+int64\s+`json:"connectNotBefore,omitempty"`/)
  assert.doesNotMatch(goIPC, /json:"connectNotBeforeUnixMs/)
  assert.match(goIPC, /type StopBotsCommand struct \{[\s\S]*Generation\s+int64\s+`json:"generation,omitempty"`[\s\S]*Reason\s+string\s+`json:"reason,omitempty"`/)
  assert.match(goManager, /Results\s+\[\]BotItemResult\s+`json:"results,omitempty"`/)
  assert.match(goManager, /SignalResults\s+\[\]SignalItemResult\s+`json:"signalResults,omitempty"`/)
  assert.match(goManager, /Bots\s+\[\]BotState\s+`json:"bots,omitempty"`/)
  assert.match(goIPC, /type SignalItemResult struct \{[\s\S]*Status\s+string\s+`json:"status,omitempty"`/)
})

test('旧 create/stop 消息继续工作且同步 stop 回显 requestId', () => {
  const { controller, events, connections } = createHarness()
  controller.handleCommand({ cmd: 'create-bots', bots: [bot('legacy')] })
  assert.equal(connections.length, 1)
  assert.equal(latest(events, 'bot-state').bots[0].status, 'connecting')

  controller.handleCommand({ cmd: 'stop-bots', requestId: 'stop-request', botIds: ['legacy'] })
  assert.equal(latest(events, 'batch-result').requestId, 'stop-request')
  assert.equal(latest(events, 'batch-result').results[0].status, 'accepted')
  assert.equal(controller.metrics().activeBots, 0)

  controller.handleCommand({ cmd: 'stop-bots', botIds: ['missing'] })
  assert.equal(latest(events, 'bot-state').bots[0].status, 'not_found')
})

test('worker-ready/heartbeat capability 完整且 shutdown 清理延迟连接', () => {
  const { controller, connections, clock } = createHarness()
  assert.deepEqual(controller.workerReady('0.4.0'), {
    evt: 'worker-ready',
    workerEpoch: 'epoch-1',
    workerEpochGeneration: 7,
    botWorkerVersion: '0.4.0',
    maxBots: 50,
    features: ['fleet-v1'],
    capacityGeneration: 1,
  })
  const heartbeat = controller.heartbeat(12.5)
  assert.equal(heartbeat.activeBots, 0)
  assert.equal(heartbeat.connectingBots, 0)
  assert.equal(heartbeat.eventLoopP95Ms, 12.5)
  assert.equal(typeof heartbeat.rssBytes, 'number')
  assert.equal(heartbeat.droppedEvents, 0)

  controller.handleCommand({ cmd: 'create-bots', bots: [bot('late', { connectNotBefore: 2_000 })] })
  controller.shutdown()
  clock.advance(2_000)
  assert.equal(connections.length, 0)
  assert.equal(controller.metrics().activeBots, 0)
})
