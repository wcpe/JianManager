import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import test from 'node:test'

import { FleetController } from '../dist/ipc/fleet.js'

class FakeClock {
  nowMs = 1_000
  nextId = 1
  timers = new Map()

  now = () => this.nowMs

  setTimeout = (callback, delay) => {
    const id = this.nextId++
    this.timers.set(id, { at: this.nowMs + Math.max(0, delay), callback })
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

  pendingCount() {
    return this.timers.size
  }
}

function createHarness(overrides = {}) {
  const events = []
  const connections = []
  const clock = new FakeClock()
  const controller = new FleetController({
    maxBots: overrides.maxBots ?? 50,
    workerEpoch: 'epoch-1',
    workerEpochGeneration: 1,
    now: clock.now,
    setTimeout: clock.setTimeout,
    clearTimeout: clock.clearTimeout,
    random: () => 0, // 固定抖动便于断言退避
    maxConcurrentConnecting: overrides.maxConcurrentConnecting,
    sendEvent: (event) => events.push(structuredClone(event)),
    createBot: (options) => {
      const bot = new EventEmitter()
      bot.username = options.username
      bot.health = 20
      bot.food = 20
      bot.entity = { position: { x: 0, y: 64, z: 0 } }
      bot.quitCount = 0
      bot.quit = () => { bot.quitCount++ }
      bot.chat = () => {}
      connections.push({ options, bot })
      return bot
    },
    createBehavior: (_botId, name) => ({
      name, start() {}, stop() {}, setMcBot() {}, async tick() {},
    }),
  })
  return { controller, events, connections, clock }
}

function fleetBot(id, overrides = {}) {
  return {
    id, name: id, host: '127.0.0.1', port: 25565,
    sessionId: 'session-1', generation: 1, configHash: `hash-${id}`,
    ...overrides,
  }
}

function admitRunning(controller, bot) {
  controller.handleCommand({
    cmd: 'create-bots', requestId: 'req-1', batchId: 'b1', idempotencyKey: 'k1',
    bots: [bot],
  })
}

test('断线后指数退避自动重连并累计 reconnectCount', () => {
  const { controller, connections, clock } = createHarness()
  admitRunning(controller, fleetBot('bot-1'))
  clock.advance(0)
  assert.equal(connections.length, 1)
  connections[0].bot.emit('spawn')
  connections[0].bot.emit('end')

  // base=1s, attempt0 → 1000ms（jitter=0）
  assert.equal(connections.length, 1)
  clock.advance(999)
  assert.equal(connections.length, 1)
  clock.advance(1)
  assert.equal(connections.length, 2)

  const snap = controller.snapshots().find((s) => s.id === 'bot-1')
  assert.ok(snap)
  assert.equal(snap.reconnectCount, 1)
})

test('stop 后取消 pending reconnect，观察窗口内不复活', () => {
  const { controller, connections, clock } = createHarness()
  admitRunning(controller, fleetBot('bot-1'))
  clock.advance(0)
  connections[0].bot.emit('spawn')
  connections[0].bot.emit('kicked', 'bye')

  controller.handleCommand({
    cmd: 'stop-bots', requestId: 'stop-1', botIds: ['bot-1'], generation: 1, reason: '人工停止',
  })
  const before = connections.length
  // 远超最大退避，不得再 spawn
  clock.advance(120_000)
  assert.equal(connections.length, before)
  assert.equal(controller.snapshots().length, 0)
  assert.equal(clock.pendingCount(), 0)
})

test('100 个 Bot 同时断线时 connecting 并发不超过配置', () => {
  const maxConcurrent = 5
  const { controller, connections, clock } = createHarness({
    maxBots: 100, maxConcurrentConnecting: maxConcurrent,
  })
  const bots = Array.from({ length: 100 }, (_, i) => fleetBot(`bot-${i}`, {
    configHash: `hash-${i}`, connectNotBeforeUnixMs: clock.now(),
  }))
  // 分批 create（单批上限 50）
  for (let start = 0; start < bots.length; start += 50) {
    const chunk = bots.slice(start, start + 50)
    controller.handleCommand({
      cmd: 'create-bots',
      requestId: `req-mass-${start}`,
      batchId: `mass-${start}`,
      idempotencyKey: `mass-key-${start}`,
      bots: chunk,
    })
  }
  // 信号量限制同时 createBot 发起数；同步 createBot 返回即放槽，advance 后应全部发起。
  clock.advance(0)
  assert.equal(connections.length, 100)
  for (const item of connections) item.bot.emit('spawn')

  // 同时 end
  for (const item of connections) item.bot.emit('end')
  const afterKick = connections.length
  // 退避 1s 后重连：同时发起数受 semaphore 限制；因 createBot 同步返回会立即排空队列。
  clock.advance(1_000)
  const reconnected = connections.length - afterKick
  assert.equal(reconnected, 100, '退避后应全部重连发起')
  const sem = controller.connectSemaphore()
  assert.ok(sem.used <= maxConcurrent)
  assert.equal(sem.waiting, 0)
})

test('连续失败进入 degraded 后 delay 固定为 max', () => {
  const { controller, connections, clock } = createHarness({ maxConcurrentConnecting: 1 })
  admitRunning(controller, fleetBot('bot-d'))
  clock.advance(0)
  // 不 spawn，直接 end 模拟连不上：需要先有 mcBot
  connections[0].bot.emit('end')
  // 连续 10 次失败后 delay 应为 60s
  for (let i = 0; i < 10; i++) {
    const before = connections.length
    // 等待当前排队的 reconnect
    clock.advance(60_000)
    assert.ok(connections.length > before, `第 ${i + 1} 次应触发重连`)
    connections[connections.length - 1].bot.emit('end')
  }
  const before = connections.length
  clock.advance(59_999)
  assert.equal(connections.length, before, 'degraded 前 60s 不得提前重连')
  clock.advance(1)
  assert.equal(connections.length, before + 1)
})
