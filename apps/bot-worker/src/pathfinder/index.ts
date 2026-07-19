/**
 * mineflayer-pathfinder 封装。
 * 为行为引擎提供 A* 寻路能力，替代直接 setControlState。
 *
 * 注意：mineflayer-pathfinder 是运行时动态导入的，
 * 因为它在某些环境可能不可用（如旧版 Node.js）。
 */

import type { Bot } from 'mineflayer'
import type { Pathfinder } from 'mineflayer-pathfinder'

// mineflayer-pathfinder 是 CommonJS，ESM 下不暴露具名导出（goals/pathfinder）。
// 全部经运行时 await import 取值，避免静态具名导入在模块加载期即崩溃——
// 该崩溃会连带打挂整个 bot-worker（包括 idle 等不寻路的 Bot）。
type Goals = typeof import('mineflayer-pathfinder').goals

type PathfinderModuleShape = {
  goals?: Goals
  pathfinder?: typeof import('mineflayer-pathfinder').pathfinder
  default?: {
    goals?: Goals
    pathfinder?: typeof import('mineflayer-pathfinder').pathfinder
  }
}

/** 从 ESM 或 CommonJS dynamic import 形态解析 goals。 */
export function resolvePathfinderGoals(module: unknown): Goals | null {
  const candidate = module as PathfinderModuleShape
  return candidate.goals ?? candidate.default?.goals ?? null
}

/** 判断 goals 是否为可用的非空导出对象。 */
export function hasPathfinderGoals(goals: unknown): goals is Goals {
  return goals !== null && typeof goals === 'object' && Object.keys(goals).length > 0
}

function resolvePathfinderPlugin(module: unknown): typeof import('mineflayer-pathfinder').pathfinder | null {
  const candidate = module as PathfinderModuleShape
  return candidate.pathfinder ?? candidate.default?.pathfinder ?? null
}

/** 寻路移动器，封装 pathfinder 的常用操作。 */
export class PathfinderMover {
  private bot: Bot
  private pathfinder: Pathfinder | null = null
  private goals: Goals | null = null
  private initialized = false

  constructor(bot: Bot) {
    this.bot = bot
  }

  /** 初始化 pathfinder 插件（惰性加载）。 */
  async init(): Promise<void> {
    if (this.initialized) return
    try {
      const module = await import('mineflayer-pathfinder')
      const plugin = resolvePathfinderPlugin(module)
      const goals = resolvePathfinderGoals(module)
      if (!plugin || !hasPathfinderGoals(goals)) throw new Error('pathfinder 导出不完整')
      this.bot.loadPlugin(plugin)
      this.pathfinder = this.bot.pathfinder
      this.goals = goals
      this.initialized = true
    } catch (err) {
      console.error(`[pathfinder] 初始化失败: ${err}`)
    }
  }

  /** 是否已初始化。 */
  isReady(): boolean {
    return this.initialized && this.pathfinder !== null && hasPathfinderGoals(this.goals)
  }

  /** 移动到指定坐标（进入 range 范围即视为到达）。 */
  async moveTo(x: number, y: number, z: number, range = 2): Promise<void> {
    if (!this.isReady()) return
    const goal = new this.goals!.GoalNear(x, y, z, range)
    this.pathfinder!.setGoal(goal)
  }

  /** 跟随指定玩家，保持在一定距离内。 */
  async followPlayer(playerName: string, range = 3): Promise<void> {
    if (!this.isReady()) return
    const player = this.bot.players[playerName]
    if (!player || !player.entity) return
    const goal = new this.goals!.GoalFollow(player.entity, range)
    this.pathfinder!.setGoal(goal)
  }

  /** 在指定半径内巡逻（随机漫步）。 */
  async wanderInRadius(cx: number, cy: number, cz: number, radius: number): Promise<void> {
    if (!this.isReady()) return
    const goal = new this.goals!.GoalNear(cx, cy, cz, radius)
    this.pathfinder!.setGoal(goal)
  }

  /** 停止当前寻路。 */
  stop(): void {
    if (this.pathfinder) {
      this.pathfinder.setGoal(null)
    }
  }
}
