import assert from 'node:assert/strict'
import test from 'node:test'

import {
  OrchestratedBehavior,
  normalizeOrchestrationConfig,
  selectOrchestrationPhase,
} from '../dist/behavior/orchestrated.js'
import { parseBehaviorTargetPoints } from '../dist/behavior/targets.js'

test('selectOrchestrationPhase 在起始时间返回第一个阶段', () => {
  const config = normalizeOrchestrationConfig({
    loop: true,
    phases: [
      { durationMs: 1000, behavior: 'idle' },
      { durationMs: 2000, behavior: 'guard' },
    ],
  })

  const selected = selectOrchestrationPhase(config, 0)

  assert.equal(selected.index, 0)
  assert.equal(selected.phase.behavior, 'idle')
})

test('selectOrchestrationPhase 在经过第一阶段后返回第二个阶段', () => {
  const config = normalizeOrchestrationConfig({
    loop: true,
    phases: [
      { durationMs: 1000, behavior: 'idle' },
      { durationMs: 2000, behavior: 'guard' },
    ],
  })

  const selected = selectOrchestrationPhase(config, 1000)

  assert.equal(selected.index, 1)
  assert.equal(selected.phase.behavior, 'guard')
})

test('selectOrchestrationPhase 在循环模式下回到第一个阶段', () => {
  const config = normalizeOrchestrationConfig({
    loop: true,
    phases: [
      { durationMs: 1000, behavior: 'idle' },
      { durationMs: 2000, behavior: 'guard' },
    ],
  })

  const selected = selectOrchestrationPhase(config, 3000)

  assert.equal(selected.index, 0)
  assert.equal(selected.phase.behavior, 'idle')
})

test('selectOrchestrationPhase 在非循环模式下停留在最后阶段', () => {
  const config = normalizeOrchestrationConfig({
    loop: false,
    phases: [
      { durationMs: 1000, behavior: 'idle' },
      { durationMs: 2000, behavior: 'guard' },
    ],
  })

  const selected = selectOrchestrationPhase(config, 9000)

  assert.equal(selected.index, 1)
  assert.equal(selected.phase.behavior, 'guard')
})

test('normalizeOrchestrationConfig 拒绝空阶段列表和负时长', () => {
  assert.throws(
    () => normalizeOrchestrationConfig({ phases: [] }),
    /phases/
  )
  assert.throws(
    () => normalizeOrchestrationConfig({
      phases: [{ durationMs: -1, behavior: 'idle' }],
    }),
    /durationMs/
  )
})

test('OrchestratedBehavior 等待 startDelayMs 后才启动内部行为', async () => {
  const realNow = Date.now
  const created = []
  const events = []
  let now = 1000
  Date.now = () => now

  try {
    const behavior = new OrchestratedBehavior(
      'bot-1',
      {
        startDelayMs: 500,
        phases: [{ durationMs: 1000, behavior: 'idle' }],
      },
      createRecordingFactory(created),
      (event) => events.push(event)
    )

    behavior.start()
    await behavior.tick()
    assert.equal(created.length, 0)

    now = 1500
    await behavior.tick()
    assert.equal(created.length, 1)
    assert.equal(created[0].behavior, 'idle')
    assert.equal(events[0].type, 'orchestration-phase')
    assert.equal(events[0].data.phaseIndex, 0)
  } finally {
    Date.now = realNow
  }
})

test('OrchestratedBehavior 切换阶段时停止旧行为并复用 custom 配置', async () => {
  const realNow = Date.now
  const created = []
  let now = 0
  Date.now = () => now

  try {
    const customConfig = {
      steps: [{ type: 'chat', message: 'hello' }],
    }
    const behavior = new OrchestratedBehavior(
      'bot-1',
      {
        loop: false,
        phases: [
          { durationMs: 1000, behavior: 'idle' },
          { durationMs: 1000, behavior: 'custom', config: customConfig },
        ],
      },
      createRecordingFactory(created),
      () => {}
    )

    behavior.start()
    await behavior.tick()
    now = 1000
    await behavior.tick()

    assert.equal(created.length, 2)
    assert.equal(created[0].stopped, true)
    assert.equal(created[1].behavior, 'custom')
    assert.deepEqual(created[1].config, customConfig)
  } finally {
    Date.now = realNow
  }
})

test('parseBehaviorTargetPoints 解析 patrol 和 guard 目标点', () => {
  const points = parseBehaviorTargetPoints(' 1,64,2 ; -3,65,4 ; bad ')

  assert.deepEqual(points, [
    { x: 1, y: 64, z: 2 },
    { x: -3, y: 65, z: 4 },
  ])
})

function createRecordingFactory(created) {
  return (_botId, behavior, config) => {
    const record = {
      behavior,
      config,
      started: false,
      stopped: false,
      ticks: 0,
      mcBot: null,
      get name() {
        return behavior
      },
      setMcBot(bot) {
        this.mcBot = bot
      },
      start() {
        this.started = true
      },
      stop() {
        this.stopped = true
      },
      async tick() {
        this.ticks++
      },
    }
    created.push(record)
    return record
  }
}
