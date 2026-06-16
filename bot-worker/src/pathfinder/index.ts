/**
 * mineflayer-pathfinder 封装。
 * 为行为引擎提供 A* 寻路能力，替代直接 setControlState。
 *
 * 注意：mineflayer-pathfinder 是运行时动态导入的，
 * 因为它在某些环境可能不可用（如旧版 Node.js）。
 */

import type { Bot } from 'mineflayer'
import type { Pathfinder } from 'mineflayer-pathfinder'
import { goals } from 'mineflayer-pathfinder'

/** 寻路移动器，封装 pathfinder 的常用操作。 */
export class PathfinderMover {
  private bot: Bot
  private pathfinder: Pathfinder | null = null
  private initialized = false

  constructor(bot: Bot) {
    this.bot = bot
  }

  /** 初始化 pathfinder 插件（惰性加载）。 */
  async init(): Promise<void> {
    if (this.initialized) return
    try {
      const { pathfinder } = await import('mineflayer-pathfinder')
      this.bot.loadPlugin(pathfinder)
      this.pathfinder = this.bot.pathfinder
      this.initialized = true
    } catch (err) {
      console.error(`[pathfinder] 初始化失败: ${err}`)
    }
  }

  /** 是否已初始化。 */
  isReady(): boolean {
    return this.initialized && this.pathfinder !== null
  }

  /** 移动到指定坐标。 */
  async moveTo(x: number, y: number, z: number, range = 2): Promise<void> {
    if (!this.isReady()) return
    const goal = new goals.GoalBlock(x, y, z)
    this.pathfinder!.setGoal(goal)
  }

  /** 跟随指定玩家，保持在一定距离内。 */
  async followPlayer(playerName: string, range = 3): Promise<void> {
    if (!this.isReady()) return
    const player = this.bot.players[playerName]
    if (!player || !player.entity) return
    const goal = new goals.GoalFollow(player.entity, range)
    this.pathfinder!.setGoal(goal)
  }

  /** 在指定半径内巡逻（随机漫步）。 */
  async wanderInRadius(cx: number, cy: number, cz: number, radius: number): Promise<void> {
    if (!this.isReady()) return
    const goal = new goals.GoalNear(cx, cy, cz, radius)
    this.pathfinder!.setGoal(goal)
  }

  /** 停止当前寻路。 */
  stop(): void {
    if (this.pathfinder) {
      this.pathfinder.setGoal(null)
    }
  }
}
