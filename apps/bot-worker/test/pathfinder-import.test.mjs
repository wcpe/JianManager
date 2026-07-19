import assert from 'node:assert/strict'
import test from 'node:test'

import {
  PathfinderMover,
  hasPathfinderGoals,
  resolvePathfinderGoals,
} from '../dist/pathfinder/index.js'

const realModule = await import('mineflayer-pathfinder')

test('真实 mineflayer-pathfinder 2.4.5 从 default.goals 解析 GoalFollow 与 GoalBlock', () => {
  const goals = resolvePathfinderGoals(realModule)

  assert.equal(typeof goals?.GoalFollow, 'function')
  assert.equal(typeof goals?.GoalBlock, 'function')
  assert.equal(hasPathfinderGoals(goals), true)
})

test('pathfinder goals 解析兼容 ESM 与 CommonJS 双形态并拒绝空对象', () => {
  const goals = realModule.default.goals

  assert.equal(resolvePathfinderGoals({ goals }), goals)
  assert.equal(resolvePathfinderGoals({ default: { goals } }), goals)
  assert.equal(hasPathfinderGoals({}), false)
  assert.equal(hasPathfinderGoals(null), false)
})

test('PathfinderMover 使用真实模块形态构造 GoalFollow', async () => {
  const installedGoals = []
  const target = { position: { x: 2, y: 64, z: 3 } }
  const bot = {
    players: { Steve: { entity: target } },
    pathfinder: null,
    loadPlugin(plugin) {
      assert.equal(typeof plugin, 'function')
      this.pathfinder = { setGoal: (goal) => installedGoals.push(goal) }
    },
  }
  const mover = new PathfinderMover(bot)

  await mover.init()
  assert.equal(mover.isReady(), true)
  await mover.followPlayer('Steve', 3)
  assert.equal(installedGoals.length, 1)
  assert.equal(installedGoals[0].constructor.name, 'GoalFollow')
})

test('真实 pathfinder 插件加载失败时保持未就绪供行为降级', async () => {
  const bot = {
    pathfinder: null,
    loadPlugin() { throw new Error('模拟插件不可用') },
  }
  const mover = new PathfinderMover(bot)
  const originalError = console.error
  console.error = () => {}
  try {
    await mover.init()
  } finally {
    console.error = originalError
  }

  assert.equal(mover.isReady(), false)
})
