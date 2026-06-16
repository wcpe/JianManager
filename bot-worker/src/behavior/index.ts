/**
 * 行为引擎基类。
 * 所有行为模式继承此类，实现 tick 方法。
 * follow/patrol/guard 行为接入 mineflayer-pathfinder 寻路（A* 移动）。
 */

import type { Bot } from 'mineflayer'
import { PathfinderMover } from '../pathfinder/index.js'
import { CustomBehavior, type CustomBehaviorConfig } from './custom.js'

export { CustomBehavior } from './custom.js'
export type { BehaviorStep, CustomBehaviorConfig } from './custom.js'

export abstract class Behavior {
  protected botId: string
  protected running = false
  protected mcBot: Bot | null = null
  /** 寻路移动器，首次 tick 时惰性初始化。 */
  protected mover: PathfinderMover | null = null
  private moverInitialized = false

  constructor(botId: string) {
    this.botId = botId
  }

  /** 绑定 Mineflayer Bot 实例。 */
  setMcBot(bot: Bot): void {
    this.mcBot = bot
  }

  /** 启动行为。 */
  start(): void {
    this.running = true
  }

  /** 停止行为。 */
  stop(): void {
    this.running = false
    if (this.mover) {
      this.mover.stop()
    }
  }

  /** 每 250ms 调用一次。 */
  abstract tick(): Promise<void>

  /** 行为名称。 */
  abstract get name(): string

  /** 确保 pathfinder 已初始化（惰性加载）。 */
  protected async ensureMover(): Promise<void> {
    if (this.moverInitialized || !this.mcBot) return
    this.mover = new PathfinderMover(this.mcBot)
    await this.mover.init()
    this.moverInitialized = true
  }

  /** 寻路是否就绪。 */
  protected isPathfinderReady(): boolean {
    return this.moverInitialized && this.mover !== null && this.mover.isReady()
  }
}

/**
 * 空闲行为：Bot 站立不动。
 */
export class IdleBehavior extends Behavior {
  get name() { return 'idle' }

  async tick() {
    // 什么都不做
  }
}

/**
 * 跟随行为：Bot 通过 pathfinder 跟随指定玩家。
 * 优先使用 A* 寻路，pathfinder 不可用时降级为简单前进。
 */
export class FollowBehavior extends Behavior {
  private target: string

  constructor(botId: string, target: string) {
    super(botId)
    this.target = target
  }

  get name() { return 'follow' }

  async tick() {
    if (!this.running || !this.mcBot) return

    const player = this.mcBot.players[this.target]
    if (!player || !player.entity) return

    const pos = player.entity.position
    this.mcBot.lookAt(pos)

    await this.ensureMover()

    if (this.isPathfinderReady() && this.mover) {
      // A* 寻路跟随
      await this.mover.followPlayer(this.target, 3)
    } else {
      // 降级：简单前进
      const dist = this.mcBot.entity.position.distanceTo(pos)
      if (dist > 3) {
        this.mcBot.setControlState('forward', true)
      } else {
        this.mcBot.setControlState('forward', false)
      }
    }
  }
}

/**
 * 巡逻行为：Bot 通过 pathfinder 在航点间巡逻。
 * 优先使用 A* 寻路，pathfinder 不可用时降级为简单移动。
 */
export class PatrolBehavior extends Behavior {
  private waypoints: Array<{ x: number; y: number; z: number }> = []
  private currentIndex = 0

  constructor(botId: string) {
    super(botId)
  }

  get name() { return 'patrol' }

  setWaypoints(points: Array<{ x: number; y: number; z: number }>) {
    this.waypoints = points
    this.currentIndex = 0
  }

  async tick() {
    if (!this.running || !this.mcBot || this.waypoints.length === 0) return

    const target = this.waypoints[this.currentIndex]
    const currentPos = this.mcBot.entity.position
    const dx = target.x - currentPos.x
    const dy = target.y - currentPos.y
    const dz = target.z - currentPos.z
    const dist = Math.sqrt(dx * dx + dy * dy + dz * dz)

    if (dist < 2) {
      this.currentIndex = (this.currentIndex + 1) % this.waypoints.length
      return
    }

    await this.ensureMover()

    if (this.isPathfinderReady() && this.mover) {
      // A* 寻路移动到下一个航点
      await this.mover.moveTo(target.x, target.y, target.z, 2)
    } else {
      // 降级：简单移动
      this.mcBot.setControlState('forward', true)
      this.mcBot.setControlState('sprint', dist > 10)
    }
  }
}

/**
 * 守卫行为：Bot 在固定位置警戒，攻击敌对实体。
 * 远离守卫位置时通过 pathfinder 返回。
 */
export class GuardBehavior extends Behavior {
  private guardPos: { x: number; y: number; z: number } | null = null

  constructor(botId: string) {
    super(botId)
  }

  get name() { return 'guard' }

  setGuardPosition(pos: { x: number; y: number; z: number }) {
    this.guardPos = pos
  }

  async tick() {
    if (!this.running || !this.mcBot) return

    // 检测附近敌对实体
    const hostile = this.mcBot.nearestEntity((entity) => {
      return entity.kind === 'Hostile mobs'
    })

    if (hostile) {
      this.mcBot.attack(hostile)
    }

    // 如果远离守卫位置则返回
    if (this.guardPos) {
      const currentPos = this.mcBot.entity.position
      const dx = this.guardPos.x - currentPos.x
      const dy = this.guardPos.y - currentPos.y
      const dz = this.guardPos.z - currentPos.z
      const dist = Math.sqrt(dx * dx + dy * dy + dz * dz)

      if (dist > 10) {
        await this.ensureMover()
        if (this.isPathfinderReady() && this.mover) {
          await this.mover.moveTo(this.guardPos.x, this.guardPos.y, this.guardPos.z, 2)
        } else {
          this.mcBot.setControlState('forward', true)
        }
      }
    }
  }
}

/**
 * 创建行为实例。
 * custom 类型需要额外的 config 参数。
 */
export function createBehavior(
  botId: string,
  type: string,
  targetOrConfig?: string | CustomBehaviorConfig
): Behavior {
  switch (type) {
    case 'follow':
      return new FollowBehavior(botId, typeof targetOrConfig === 'string' ? targetOrConfig : '')
    case 'patrol':
      return new PatrolBehavior(botId)
    case 'guard':
      return new GuardBehavior(botId)
    case 'custom': {
      const config = targetOrConfig && typeof targetOrConfig === 'object'
        ? targetOrConfig as CustomBehaviorConfig
        : { steps: [] }
      return new CustomBehavior(botId, config)
    }
    case 'idle':
    default:
      return new IdleBehavior(botId)
  }
}
